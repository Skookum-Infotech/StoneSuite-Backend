package fabrication

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Create inserts a new fabrication job spawned from a sales order, seeds its 16
// checklist steps, and starts it at ORCV (order received). The site snapshot
// defaults from the sales order's shipping address unless overridden. All in
// one transaction (spec §2.2, §5).
func Create(ctx context.Context, pool *pgxpool.Pool, in CreateJobInput, actorEmployeeID int) (*Job, error) {
	if strings.TrimSpace(in.SalesOrderUUID) == "" {
		return nil, ClientError{Msg: "A sales order is required to open a fabrication job."}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create fabrication job: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Resolve the originating sales order and its customer (the job inherits both).
	var soInternalID, customerInternalID int
	var soShipName, soShipLine1, soShipLine2, soShipCity, soShipZip, soShipPhone string
	var soShipState *int
	err = tx.QueryRow(ctx, `
		SELECT so.sales_order_id, so.sales_order_customer_id,
		       so.sales_order_ship_customer_name, so.sales_order_ship_addr_line1, so.sales_order_ship_addr_line2,
		       so.sales_order_ship_addr_city, so.sales_order_ship_addr_state, so.sales_order_ship_addr_zip, so.sales_order_ship_phone
		FROM sales_order so
		WHERE so.sales_order_uuid = $1 AND so.sales_order_deleted_at IS NULL`, in.SalesOrderUUID).Scan(
		&soInternalID, &customerInternalID,
		&soShipName, &soShipLine1, &soShipLine2, &soShipCity, &soShipState, &soShipZip, &soShipPhone)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ClientError{Msg: "The referenced sales order does not exist."}
	}
	if err != nil {
		return nil, fmt.Errorf("resolve sales order: %w", err)
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, fjobRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve FJOB record type: %w", err)
	}
	initialStatusID, err := statusIDByCode(ctx, tx, recordTypeID, StatusOrderReceived)
	if err != nil {
		return nil, fmt.Errorf("resolve ORCV status: %w", err)
	}

	// Site snapshot: caller value wins, else the SO shipping address.
	site := SiteAddress{
		CustomerName: firstNonEmpty(in.SiteCustomerName, soShipName),
		AddrLine1:    firstNonEmpty(in.SiteAddrLine1, soShipLine1),
		AddrLine2:    firstNonEmpty(in.SiteAddrLine2, soShipLine2),
		City:         firstNonEmpty(in.SiteCity, soShipCity),
		Zip:          firstNonEmpty(in.SiteZip, soShipZip),
		Phone:        firstNonEmpty(in.SitePhone, soShipPhone),
	}
	site.StateID = in.SiteStateID
	if site.StateID == nil {
		site.StateID = soShipState
	}

	ownerEmployeeID := actorEmployeeID
	if in.OwnerEmployeeID != nil && *in.OwnerEmployeeID > 0 {
		ownerEmployeeID = *in.OwnerEmployeeID
	}
	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}

	var jobInternalID int
	var newUUID string
	err = tx.QueryRow(ctx, `
		INSERT INTO fabrication_job (
			record_type, fabrication_job_status, sales_order_id, fabrication_job_customer_id,
			job_site_customer_name, job_site_addr_line1, job_site_addr_line2, job_site_addr_city,
			job_site_addr_state, job_site_addr_zip, job_site_phone,
			job_template_date, job_fabrication_start, job_promised_install_date,
			job_owner_id, job_templater_id, job_fabricator_id, job_install_crew_id,
			job_notes, job_custom_fields, fabrication_job_created_by
		) VALUES ($1,$2,$3,$4, $5,$6,$7,$8, $9,$10,$11, $12::date,$13::date,$14::date,
			$15,$16,$17,$18, $19,$20,$21)
		RETURNING fabrication_job_id, fabrication_job_uuid`,
		recordTypeID, initialStatusID, soInternalID, customerInternalID,
		site.CustomerName, site.AddrLine1, site.AddrLine2, site.City,
		site.StateID, site.Zip, site.Phone,
		nullableDate(in.TemplateDate), nullableDate(in.FabricationStart), nullableDate(in.PromisedInstallDate),
		nullableInt(ownerEmployeeID), nullableIntPtr(in.TemplaterEmployeeID),
		nullableIntPtr(in.FabricatorEmployeeID), nullableIntPtr(in.InstallCrewEmployeeID),
		in.Notes, custom, nullableInt(actorEmployeeID)).Scan(&jobInternalID, &newUUID)
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, ClientError{Msg: "One of the referenced ids (state, or an employee) does not exist."}
		}
		return nil, fmt.Errorf("insert fabrication job: %w", err)
	}

	number := FormatNumber(int64(jobInternalID))
	if _, err := tx.Exec(ctx,
		`UPDATE fabrication_job SET fabrication_job_number = $1 WHERE fabrication_job_id = $2`, number, jobInternalID); err != nil {
		return nil, fmt.Errorf("set job number: %w", err)
	}

	pieceIDs, err := insertPieces(ctx, tx, jobInternalID, soInternalID, in.Pieces, actorEmployeeID)
	if err != nil {
		return nil, err
	}

	if err := seedSteps(ctx, tx, jobInternalID, pieceIDs); err != nil {
		return nil, err
	}

	writeHistory(ctx, tx, jobInternalID, "create", nil, &initialStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create fabrication job: %w", err)
	}
	return Get(ctx, pool, newUUID)
}

// insertPieces inserts fabricated pieces and returns their internal ids (for
// seeding piece-grain steps). A piece may reference a sales_order_item.
func insertPieces(ctx context.Context, tx pgx.Tx, jobInternalID, soInternalID int, pieces []PieceInput, actorEmployeeID int) ([]int, error) {
	var ids []int
	for i, p := range pieces {
		pieceNumber := p.PieceNumber
		if pieceNumber <= 0 {
			pieceNumber = i + 1
		}
		var soItemID any
		if strings.TrimSpace(p.SalesOrderItemUUID) != "" {
			var id int
			err := tx.QueryRow(ctx, `
				SELECT soi.sales_order_item_id FROM sales_order_item soi
				WHERE soi.sales_order_item_uuid = $1 AND soi.sales_order_id = $2 AND soi.item_deleted_at IS NULL`,
				p.SalesOrderItemUUID, soInternalID).Scan(&id)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, ClientError{Msg: fmt.Sprintf("Piece %d references a line that is not on this sales order.", pieceNumber)}
			}
			if err != nil {
				return nil, fmt.Errorf("resolve piece sales order item: %w", err)
			}
			soItemID = id
		}
		var pieceID int
		err := tx.QueryRow(ctx, `
			INSERT INTO fabrication_job_item (
				fabrication_job_id, sales_order_item_id, piece_number, piece_name, piece_type,
				piece_length_mm, piece_width_mm, piece_thickness_mm,
				sink_cutout_count, cooktop_cutout_count, seam_count, item_created_by
			) VALUES ($1,$2,$3,$4,$5, $6,$7,$8, $9,$10,$11, $12)
			RETURNING fabrication_job_item_id`,
			jobInternalID, soItemID, pieceNumber, p.PieceName, p.PieceType,
			p.LengthMM, p.WidthMM, p.ThicknessMM,
			p.SinkCutoutCount, p.CooktopCutoutCount, p.SeamCount, nullableInt(actorEmployeeID)).Scan(&pieceID)
		if err != nil {
			if isCheckViolation(err) {
				return nil, ClientError{Msg: fmt.Sprintf("Piece %d: one or more values are out of range.", pieceNumber)}
			}
			return nil, fmt.Errorf("insert piece: %w", err)
		}
		ids = append(ids, pieceID)
	}
	return ids, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

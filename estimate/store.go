package estimate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// ErrNotFound is returned when an estimate uuid matches nothing live.
var ErrNotFound = errors.New("estimate not found")

// ClientError signals a client-caused failure (validation, bad input, an
// illegal transition) that a controller maps to HTTP 400/409, mirroring
// salesorder.ClientError.
type ClientError struct{ Msg string }

func (e ClientError) Error() string { return e.Msg }

// IsClientError reports whether err is a ClientError.
func IsClientError(err error) bool {
	var ce ClientError
	return errors.As(err, &ce)
}

// estmRecordTypeCode is the lkp_record_type code for Estimate (spec §1).
const estmRecordTypeCode = "ESTM"

// draftStatusCode is the status every new estimate starts at (spec §7).
const draftStatusCode = "DRFT"

// nullableInt converts a non-positive id to SQL NULL.
func nullableInt(v int) any {
	if v <= 0 {
		return nil
	}
	return v
}

// nullableDate returns the given "yyyy-mm-dd" string as a nullable date arg.
func nullableDate(d string) any {
	if d == "" {
		return nil
	}
	return d
}

// orNow returns the given "yyyy-mm-dd" date string, or today when blank.
func orNow(d string) string {
	if d == "" {
		return "now"
	}
	return d
}

// isForeignKeyViolation reports whether err is a PostgreSQL FK-constraint
// violation (code 23503) — an invalid caller-supplied reference id.
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// colVal pairs a column name with its bind value (and an optional type cast
// suffix, e.g. "::date") so an INSERT/UPDATE's column list and argument list
// are always built from the same slice.
type colVal struct {
	col  string
	val  any
	cast string
}

// buildInsert renders an INSERT ... VALUES (...) RETURNING statement from
// column/value pairs, numbering placeholders by position.
func buildInsert(table string, cv []colVal, returning string) (string, []any) {
	cols := make([]string, len(cv))
	phs := make([]string, len(cv))
	args := make([]any, len(cv))
	for i, c := range cv {
		cols[i] = c.col
		args[i] = c.val
		phs[i] = fmt.Sprintf("$%d%s", i+1, c.cast)
	}
	sql := "INSERT INTO " + table + " (" + strings.Join(cols, ", ") + ") VALUES (" + strings.Join(phs, ", ") + ")"
	if returning != "" {
		sql += " RETURNING " + returning
	}
	return sql, args
}

// buildUpdateSet renders an "UPDATE ... SET col=$n, ... WHERE <where>"
// statement. leadingArgs are bound first; cv's placeholders continue after.
func buildUpdateSet(table string, leadingArgs []any, cv []colVal, extraSets []string, where string) (string, []any) {
	sets := make([]string, 0, len(cv)+len(extraSets))
	args := make([]any, 0, len(leadingArgs)+len(cv))
	args = append(args, leadingArgs...)
	for _, c := range cv {
		args = append(args, c.val)
		sets = append(sets, fmt.Sprintf("%s = $%d%s", c.col, len(args), c.cast))
	}
	sets = append(sets, extraSets...)
	sql := "UPDATE " + table + " SET " + strings.Join(sets, ", ") + " WHERE " + where
	return sql, args
}

// recordTypeIDByCode resolves a lkp_record_type code to its internal id.
func recordTypeIDByCode(ctx context.Context, q workflow.Querier, code string) (int, error) {
	var id int
	err := q.QueryRow(ctx,
		`SELECT record_type_id FROM lkp_record_type WHERE record_type_code = $1`, code).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("record type %q: %w", code, err)
	}
	return id, nil
}

// statusIDByCode resolves a lkp_record_status code (scoped to a record type)
// to its internal id.
func statusIDByCode(ctx context.Context, q workflow.Querier, recordTypeID int, code string) (int, error) {
	var id int
	err := q.QueryRow(ctx, `
		SELECT record_status_id FROM lkp_record_status
		WHERE record_status_record_type = $1 AND record_status_code = $2`, recordTypeID, code).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("status %q: %w", code, err)
	}
	return id, nil
}

// customerSnapshot loads a customer's internal id, name, and default
// billing/shipping address blocks for the create-time snapshot (spec AD-4).
func customerSnapshot(ctx context.Context, q workflow.Querier, customerUUID string) (id int, name string, billing, shipping AddressInput, err error) {
	var (
		billLine1, billLine2, billSuite, billCity string
		billState, billCountry                    *int
		billZip                                   string
		shipLine1, shipLine2, shipSuite, shipCity string
		shipState, shipCountry                    *int
		shipZip                                   string
	)
	err = q.QueryRow(ctx, `
		SELECT customer_id, customer_name,
		       customer_bill_addr_line1, customer_bill_addr_line2, customer_bill_addr_suitenum,
		       customer_bill_addr_city, customer_bill_addr_state, customer_bill_addr_zip, customer_bill_addr_country,
		       customer_ship_addr_line1, customer_ship_addr_line2, customer_ship_addr_suitenum,
		       customer_ship_addr_city, customer_ship_addr_state, customer_ship_addr_zip, customer_ship_addr_country
		FROM customer WHERE customer_uuid = $1 AND customer_deleted_at IS NULL`, customerUUID).Scan(
		&id, &name,
		&billLine1, &billLine2, &billSuite, &billCity, &billState, &billZip, &billCountry,
		&shipLine1, &shipLine2, &shipSuite, &shipCity, &shipState, &shipZip, &shipCountry,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", AddressInput{}, AddressInput{}, ClientError{Msg: "Unknown customer."}
	}
	if err != nil {
		return 0, "", AddressInput{}, AddressInput{}, fmt.Errorf("load customer snapshot: %w", err)
	}
	billing = AddressInput{
		CustomerName: name, AddrLine1: billLine1, AddrLine2: billLine2, SuiteUnit: billSuite,
		City: billCity, StateID: billState, Zip: billZip, CountryID: billCountry,
	}
	shipping = AddressInput{
		CustomerName: name, AddrLine1: shipLine1, AddrLine2: shipLine2, SuiteUnit: shipSuite,
		City: shipCity, StateID: shipState, Zip: shipZip, CountryID: shipCountry,
	}
	return id, name, billing, shipping, nil
}

// customerSnapshotByInternalID resolves an internal customer id back to its
// uuid, then delegates to customerSnapshot (used by Update, which only has
// the internal id on hand from the estimate row).
func customerSnapshotByInternalID(ctx context.Context, q workflow.Querier, custInternalID int) (id int, name string, billing, shipping AddressInput, err error) {
	var uuid string
	if err := q.QueryRow(ctx, `SELECT customer_uuid FROM customer WHERE customer_id = $1`, custInternalID).Scan(&uuid); err != nil {
		return 0, "", AddressInput{}, AddressInput{}, fmt.Errorf("resolve customer uuid: %w", err)
	}
	return customerSnapshot(ctx, q, uuid)
}

// overrideAddress layers a caller-supplied partial address over a default
// (e.g. the customer's snapshot), preferring the override for any non-empty field.
func overrideAddress(def, override AddressInput) AddressInput {
	out := def
	if override.CustomerName != "" {
		out.CustomerName = override.CustomerName
	}
	if override.Attention != "" {
		out.Attention = override.Attention
	}
	if override.AddrLine1 != "" {
		out.AddrLine1 = override.AddrLine1
	}
	if override.AddrLine2 != "" {
		out.AddrLine2 = override.AddrLine2
	}
	if override.SuiteUnit != "" {
		out.SuiteUnit = override.SuiteUnit
	}
	if override.City != "" {
		out.City = override.City
	}
	if override.StateID != nil {
		out.StateID = override.StateID
	}
	if override.Zip != "" {
		out.Zip = override.Zip
	}
	if override.CountryID != nil {
		out.CountryID = override.CountryID
	}
	if override.Phone != "" {
		out.Phone = override.Phone
	}
	if override.Fax != "" {
		out.Fax = override.Fax
	}
	if override.Email != "" {
		out.Email = override.Email
	}
	return out
}

// addrColVals returns the 12 (column, value) pairs for a billing/shipping
// address block, in the exact column order the schema declares (state before
// zip). prefix is "estimate_bill" or "estimate_ship".
func addrColVals(prefix string, a AddressInput) []colVal {
	return []colVal{
		{prefix + "_customer_name", a.CustomerName, ""},
		{prefix + "_attention", a.Attention, ""},
		{prefix + "_addr_line1", a.AddrLine1, ""},
		{prefix + "_addr_line2", a.AddrLine2, ""},
		{prefix + "_addr_suitenum", a.SuiteUnit, ""},
		{prefix + "_addr_city", a.City, ""},
		{prefix + "_addr_state", a.StateID, ""},
		{prefix + "_addr_zip", a.Zip, ""},
		{prefix + "_addr_country", a.CountryID, ""},
		{prefix + "_phone", a.Phone, ""},
		{prefix + "_fax", a.Fax, ""},
		{prefix + "_email", a.Email, ""},
	}
}

// itemSnapshot is what a line needs from its catalog item at add time.
type itemSnapshot struct {
	internalID int
	sku        string
	name       string
	desc       string
	unitID     *int
	unitCode   string
	unitPrice  float64
	taxRateID  *int
}

// resolveInventoryItem loads a catalog item's snapshot fields by its external
// uuid. Returns ClientError when the uuid does not resolve to a live item.
func resolveInventoryItem(ctx context.Context, q workflow.Querier, uuid string) (*itemSnapshot, error) {
	var s itemSnapshot
	err := q.QueryRow(ctx, `
		SELECT ii.inventory_item_id, ii.inventory_item_sku, ii.inventory_item_name, ii.inventory_item_description,
		       ii.inventory_item_unit_id, COALESCE(u.unit_code,''), ii.inventory_item_unit_price, ii.inventory_item_tax_rate_id
		FROM inventory_item ii
		LEFT JOIN lkp_unit u ON u.unit_id = ii.inventory_item_unit_id
		WHERE ii.inventory_item_uuid = $1 AND ii.inventory_item_deleted_at IS NULL`, uuid).Scan(
		&s.internalID, &s.sku, &s.name, &s.desc, &s.unitID, &s.unitCode, &s.unitPrice, &s.taxRateID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ClientError{Msg: "Unknown inventory item: " + uuid}
	}
	if err != nil {
		return nil, fmt.Errorf("load inventory item: %w", err)
	}
	return &s, nil
}

// taxPercentForRate loads a named tax rate's percent by internal id.
func taxPercentForRate(ctx context.Context, q workflow.Querier, taxRateID int) (float64, error) {
	var pct float64
	if err := q.QueryRow(ctx,
		`SELECT tax_rate_percent FROM lkp_tax_rate WHERE tax_rate_id = $1`, taxRateID).Scan(&pct); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ClientError{Msg: "Unknown tax rate."}
		}
		return 0, fmt.Errorf("load tax rate: %w", err)
	}
	return pct, nil
}

// estimateSelect is the base SELECT shared by Get and Search. Column order
// must match scanEstimate's Scan(...) arg order exactly.
const estimateSelect = `
	SELECT est.estimate_uuid, COALESCE(est.estimate_number,''),
	       rs.record_status_name, rs.record_status_code,
	       est.estimate_approval_status,
	       c.customer_uuid, c.customer_name,
	       COALESCE(ou.id::text,''),
	       to_char(est.estimate_date,'YYYY-MM-DD'),
	       COALESCE(to_char(est.estimate_valid_until,'YYYY-MM-DD'),''),
	       est.estimate_po_number, est.estimate_reference_number, est.estimate_memo,
	       est.estimate_notes, est.estimate_internal_notes, est.estimate_terms_conditions,
	       est.estimate_payment_terms, est.estimate_price_level, est.estimate_currency,
	       est.estimate_sales_rep_id, est.estimate_owner_id, est.estimate_sales_tax_percent,
	       est.estimate_ship_same_as_bill,
	       est.estimate_bill_customer_name, est.estimate_bill_attention,
	       est.estimate_bill_addr_line1, est.estimate_bill_addr_line2, est.estimate_bill_addr_suitenum,
	       est.estimate_bill_addr_city, est.estimate_bill_addr_state, est.estimate_bill_addr_zip,
	       est.estimate_bill_addr_country, est.estimate_bill_phone, est.estimate_bill_fax, est.estimate_bill_email,
	       est.estimate_ship_customer_name, est.estimate_ship_attention,
	       est.estimate_ship_addr_line1, est.estimate_ship_addr_line2, est.estimate_ship_addr_suitenum,
	       est.estimate_ship_addr_city, est.estimate_ship_addr_state, est.estimate_ship_addr_zip,
	       est.estimate_ship_addr_country, est.estimate_ship_phone, est.estimate_ship_fax, est.estimate_ship_email,
	       est.estimate_custom_fields,
	       est.estimate_subtotal, est.estimate_discount_total, est.estimate_tax_total,
	       est.estimate_shipping_charge, est.estimate_adjustment, est.estimate_grand_total,
	       est.estimate_created_at, est.estimate_updated_at,
	       est.estimate_status, est.estimate_customer_id
	FROM estimate est
	JOIN lkp_record_status rs ON rs.record_status_id = est.estimate_status
	JOIN customer c ON c.customer_id = est.estimate_customer_id
	LEFT JOIN employee oe ON oe.employee_id = est.estimate_owner_id
	LEFT JOIN users ou ON ou.id = oe.employee_user_id`

// estimateMeta carries the internal numeric ids an estimate row has but the
// API response does not expose. Search needs them to mint a keyset cursor for
// sorts that run on those columns (`status`, `customer_id`); without it the
// cursor is built from the wrong field and every page after the first is
// wrong. Mirrors invoice.invoiceMeta.
type estimateMeta struct {
	statusID   int
	customerID int
}

func scanEstimate(row pgx.Row) (*Estimate, estimateMeta, error) {
	var e Estimate
	var meta estimateMeta
	var customRaw []byte
	if err := row.Scan(
		&e.ID, &e.Number, &e.Status, &e.StatusCode, &e.ApprovalStatus,
		&e.Customer.ID, &e.Customer.Name, &e.OwnerUserID,
		&e.EstimateDate, &e.ValidUntil,
		&e.PONumber, &e.ReferenceNumber, &e.Memo,
		&e.Notes, &e.InternalNotes, &e.TermsConditions,
		&e.PaymentTermsID, &e.PriceLevelID, &e.CurrencyID,
		&e.SalesRepEmployeeID, &e.OwnerEmployeeID, &e.SalesTaxPercent,
		&e.ShipSameAsBilling,
		&e.Billing.CustomerName, &e.Billing.Attention,
		&e.Billing.AddrLine1, &e.Billing.AddrLine2, &e.Billing.SuiteUnit,
		&e.Billing.City, &e.Billing.StateID, &e.Billing.Zip,
		&e.Billing.CountryID, &e.Billing.Phone, &e.Billing.Fax, &e.Billing.Email,
		&e.Shipping.CustomerName, &e.Shipping.Attention,
		&e.Shipping.AddrLine1, &e.Shipping.AddrLine2, &e.Shipping.SuiteUnit,
		&e.Shipping.City, &e.Shipping.StateID, &e.Shipping.Zip,
		&e.Shipping.CountryID, &e.Shipping.Phone, &e.Shipping.Fax, &e.Shipping.Email,
		&customRaw,
		&e.Subtotal, &e.DiscountTotal, &e.TaxTotal,
		&e.ShippingCharge, &e.Adjustment, &e.GrandTotal,
		&e.CreatedAt, &e.UpdatedAt,
		&meta.statusID, &meta.customerID,
	); err != nil {
		return nil, estimateMeta{}, err
	}
	if len(customRaw) > 0 {
		_ = json.Unmarshal(customRaw, &e.CustomFields)
	}
	return &e, meta, nil
}

// itemSelect is the base SELECT for an estimate's live lines. Column order
// must match scanLine's Scan(...) arg order exactly.
const itemSelect = `
	SELECT ei.estimate_item_uuid, ei.line_number,
	       ii.inventory_item_uuid,
	       ei.sku, ei.item_name, ei.description, COALESCE(ei.unit_code,''),
	       ei.quantity, ei.unit_price, ei.discount_percent, ei.tax_percent,
	       ei.line_subtotal, ei.line_discount, ei.line_tax, ei.line_total
	FROM estimate_item ei
	LEFT JOIN inventory_item ii ON ii.inventory_item_id = ei.inventory_item_id
	WHERE ei.estimate_id = $1 AND ei.item_deleted_at IS NULL
	ORDER BY ei.line_number`

func scanLine(row pgx.Rows) (Line, error) {
	var l Line
	err := row.Scan(
		&l.ID, &l.LineNumber, &l.InventoryItemID,
		&l.SKU, &l.ItemName, &l.Description, &l.UnitCode,
		&l.Quantity, &l.UnitPrice, &l.DiscountPercent, &l.TaxPercent,
		&l.LineSubtotal, &l.LineDiscount, &l.LineTax, &l.LineTotal,
	)
	return l, err
}

// loadLines fetches an estimate's live lines by its external uuid.
func loadLines(ctx context.Context, q workflow.Querier, uuid string) ([]Line, error) {
	var internalID int
	if err := q.QueryRow(ctx,
		`SELECT estimate_id FROM estimate WHERE estimate_uuid = $1`, uuid).Scan(&internalID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("resolve estimate id: %w", err)
	}
	rows, err := q.Query(ctx, itemSelect, internalID)
	if err != nil {
		return nil, fmt.Errorf("load estimate items: %w", err)
	}
	defer rows.Close()
	out := []Line{}
	for rows.Next() {
		l, err := scanLine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Get loads a single live estimate by its external uuid, including its lines.
func Get(ctx context.Context, pool *pgxpool.Pool, uuid string) (*Estimate, error) {
	e, _, err := scanEstimate(pool.QueryRow(ctx, estimateSelect+`
		WHERE est.estimate_uuid = $1 AND est.estimate_deleted_at IS NULL`, uuid))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get estimate: %w", err)
	}
	items, err := loadLines(ctx, pool, uuid)
	if err != nil {
		return nil, err
	}
	e.Items = items
	return e, nil
}

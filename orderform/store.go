package orderform

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/inventory"
	"stonesuite-backend/salesorder"
	"stonesuite-backend/workflow"
)

// Create places an order: it stores the form, makes sure the kits and items it
// names exist, and drafts the Sales Order — all in one transaction, so a failure
// anywhere leaves no form, no draft and no stray catalogue rows. now is the
// caller's clock (UTC).
//
// Nothing about the customer comes from the request: who is the session's
// resolved portal login (see portal.ResolveSession).
func Create(ctx context.Context, pool *pgxpool.Pool, who Submitter, raw Input, now time.Time) (*Result, error) {
	in, err := Normalize(raw, now)
	if err != nil {
		return nil, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create order form: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Only Active customers can be used on a new Sales Order. Say so up front,
	// in the customer's terms, rather than surfacing the staff-facing message.
	msg, err := workflow.CustomerNotUsableByID(ctx, tx, who.CustomerID)
	if err != nil {
		return nil, fmt.Errorf("check customer status: %w", err)
	}
	if msg != "" {
		return nil, ErrCustomerNotActive
	}

	cust, err := loadCustomerDefaults(ctx, tx, who.CustomerID)
	if err != nil {
		return nil, err
	}
	form, err := insertForm(ctx, tx, who, in)
	if err != nil {
		return nil, err
	}

	cat, err := inventory.EnsureCatalog(ctx, tx, catalogRequest(in))
	if err != nil {
		return nil, err
	}
	soIn, err := buildOrder(in, who, cust, cat)
	if err != nil {
		return nil, err
	}
	// No actor: a portal login is not an employee. Stock is not reserved for the
	// draft (its lines are placeholders), and the ship-to is the job site as
	// given, not layered over the customer's own shipping address.
	created, err := salesorder.CreateInTx(ctx, tx, soIn, 0, salesorder.CreateOptions{
		DeferStockReservation: true,
		ShippingAsGiven:       true,
	})
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE customer_order_form SET sales_order_id = $2 WHERE customer_order_form_id = $1`,
		form.internalID, created.InternalID); err != nil {
		return nil, fmt.Errorf("link order form to sales order: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit order form: %w", err)
	}

	res := &Result{
		Form:           Form{ID: form.uuid, Status: StatusSubmitted, CreatedAt: form.createdAt, Editable: true, Input: in},
		SalesOrderUUID: created.UUID,
		Synced:         true,
	}
	for _, k := range cat.Kits {
		if k.Created {
			res.CreatedKits = append(res.CreatedKits, k.ID)
		}
	}
	for _, i := range cat.Items {
		if i.Created {
			res.CreatedItems = append(res.CreatedItems, i.ID)
		}
	}
	return res, nil
}

// loadCustomerDefaults reads what the staff form would pre-fill from the customer.
func loadCustomerDefaults(ctx context.Context, q workflow.Querier, customerID int) (customerDefaults, error) {
	var c customerDefaults
	var tax *float64
	var first, last, contact, accts string
	err := q.QueryRow(ctx, `
		SELECT customer_name, customer_crm_owner_user_id, customer_sales_rep_user_id,
		       customer_payment_terms, customer_price_level, customer_currency,
		       customer_sales_tax_percent::float8,
		       customer_authorized_person_fname, customer_authorized_person_lname,
		       customer_primary_phonenum, customer_faxnum,
		       customer_contact_email, customer_accounts_email
		FROM customer
		WHERE customer_id = $1 AND customer_deleted_at IS NULL`, customerID,
	).Scan(&c.name, &c.ownerEmployeeID, &c.salesRepEmployeeID,
		&c.paymentTermsID, &c.priceLevelID, &c.currencyID, &tax,
		&first, &last, &c.billPhone, &c.billFax, &contact, &accts)
	if err != nil {
		return customerDefaults{}, fmt.Errorf("load customer defaults: %w", err)
	}
	if tax != nil {
		c.salesTaxPercent = *tax
	}
	c.billAttention = strings.TrimSpace(first + " " + last)
	c.billEmail = accts
	if c.billEmail == "" {
		c.billEmail = contact
	}
	return c, nil
}

// storedForm is the identity of a form row just inserted.
type storedForm struct {
	internalID int
	uuid       string
	createdAt  time.Time
}

// insertForm writes the header and one row per area.
func insertForm(ctx context.Context, tx pgx.Tx, who Submitter, in Input) (storedForm, error) {
	var f storedForm
	err := tx.QueryRow(ctx, `
		INSERT INTO customer_order_form (
			customer_id, portal_user_id, order_form_status,
			order_form_builder, order_form_community, order_form_plan,
			order_form_phone, order_form_email,
			order_form_install_address, order_form_city, order_form_zip,
			order_form_measure_date, order_form_install_date, order_form_special_instructions
		) VALUES ($1,$2,$3, $4,$5,$6, $7,$8, $9,$10,$11, $12::date,$13::date,$14)
		RETURNING customer_order_form_id, customer_order_form_uuid::text, order_form_created_at`,
		who.CustomerID, who.PortalUserID, StatusSubmitted,
		in.Builder, in.Community, in.Plan, in.Phone, in.Email,
		in.InstallAddress, in.City, in.Zip, in.MeasureDate, in.InstallDate, in.SpecialInstructions,
	).Scan(&f.internalID, &f.uuid, &f.createdAt)
	if err != nil {
		return storedForm{}, fmt.Errorf("insert order form: %w", err)
	}
	if err := insertAreas(ctx, tx, f.internalID, in.Areas); err != nil {
		return storedForm{}, err
	}
	return f, nil
}

// insertAreas writes one row per area, in the order the customer listed them.
func insertAreas(ctx context.Context, tx pgx.Tx, formID int, areas []Area) error {
	for i, a := range areas {
		var height any
		if a.BacksplashHeight != "" {
			v, perr := strconv.ParseFloat(a.BacksplashHeight, 64)
			if perr != nil {
				return fmt.Errorf("order form area %q: backsplash height: %w", a.Name, perr)
			}
			height = v
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO customer_order_form_area (
				customer_order_form_id, area_position, area_kind, area_name,
				area_color, area_finish, area_edge, area_backsplash_height_in,
				area_sink_model, area_cooktop_model, area_waterfall_qty, area_pop_up_outlet_qty,
				area_half_wall, area_location, area_faucet_spread, area_notes
			) VALUES ($1,$2,$3,$4, $5,$6,$7,$8, $9,$10,$11,$12, $13,$14,$15,$16)`,
			formID, i+1, a.Kind, a.Name,
			a.Color, a.Finish, a.Edge, height,
			a.SinkModel, a.CooktopModel, a.WaterfallQty, a.PopUpOutlet,
			a.HalfWall, a.Location, a.FaucetSpread, a.Notes,
		); err != nil {
			return fmt.Errorf("insert order form area %q: %w", a.Name, err)
		}
	}
	return nil
}

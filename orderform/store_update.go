package orderform

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/inventory"
	"stonesuite-backend/salesorder"
	"stonesuite-backend/workflow"
)

// lockedForm is a form row held FOR UPDATE for the rest of the transaction,
// with everything an edit needs to compare against.
type lockedForm struct {
	form         Form
	internalID   int
	portalUserID string
	salesOrderID *int
}

// Update changes an order the customer filed, and brings its Sales Order along,
// all in one transaction. It is allowed only while that Sales Order is still a
// Draft; once staff move it on the order is theirs and this returns
// ErrNotEditable. The lines staff added or finished on the draft are kept (see
// merge.go).
//
// Like Create, nothing about the customer comes from the request: the order is
// found by the session's customer id, and another customer's order is
// ErrNotFound. now is the caller's clock (UTC).
func Update(ctx context.Context, pool *pgxpool.Pool, who Submitter, id string, raw Input, now time.Time) (*Result, error) {
	id = strings.TrimSpace(id)
	if !uuidPattern.MatchString(id) {
		return nil, ErrNotFound
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin update order form: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := lockForm(ctx, tx, who.CustomerID, id)
	if err != nil {
		return nil, err
	}
	in, err := NormalizeEdit(raw, now, cur.form.InstallDate)
	if err != nil {
		return nil, err
	}
	// The same rule as placing an order: a customer whose account is on hold
	// cannot change one either.
	msg, err := workflow.CustomerNotUsableByID(ctx, tx, who.CustomerID)
	if err != nil {
		return nil, fmt.Errorf("check customer status: %w", err)
	}
	if msg != "" {
		return nil, ErrCustomerNotActive
	}

	soUUID, snap, err := editableSalesOrder(ctx, tx, cur)
	if err != nil {
		return nil, err
	}
	// No actor: a portal login is not an employee.
	cat, err := syncSalesOrder(ctx, tx, cur, in, soUUID, snap, 0, portalEdit(who), now)
	if err != nil {
		return nil, err
	}
	updatedAt, err := rewriteForm(ctx, tx, cur.internalID, in)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update order form: %w", err)
	}
	return editResult(cur, in, soUUID, updatedAt, true, cat), nil
}

// StaffEditor is the staff member changing a form from its Sales Order.
type StaffEditor struct {
	// EmployeeID is the editor's employee profile, 0 when the login has none.
	EmployeeID int
	// Label names them in the memo: "Name (email)".
	Label string
}

// StaffUpdate changes the form behind a Sales Order from the tenant side. The
// caller has already established that the person may (a super admin) and may
// see the order; this does the write.
//
// While the Sales Order is still a Draft it is brought along exactly as a
// customer's own change is, keeping what staff did to it. Once it has moved on
// only the stored form changes — the Sales Order is staff's, and they edit its
// lines directly — and Result.Synced says which happened.
//
// ErrNotFound when the order has no form. now is the caller's clock (UTC).
func StaffUpdate(ctx context.Context, pool *pgxpool.Pool, soUUID string, raw Input, by StaffEditor, now time.Time) (*Result, error) {
	soUUID = strings.TrimSpace(soUUID)
	if !uuidPattern.MatchString(soUUID) {
		return nil, ErrNotFound
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin staff update order form: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := lockFormBySalesOrder(ctx, tx, soUUID)
	if err != nil {
		return nil, err
	}
	in, err := NormalizeEdit(raw, now, cur.form.InstallDate)
	if err != nil {
		return nil, err
	}
	snap, err := salesorder.LoadForEdit(ctx, tx, soUUID)
	if errors.Is(err, salesorder.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	var cat *inventory.CatalogResult
	editable := snap.StatusCode == salesorder.DraftStatusCode
	if editable {
		cat, err = syncSalesOrder(ctx, tx, cur, in, soUUID, snap, by.EmployeeID, staffEdit(by.Label), now)
		if err != nil {
			return nil, err
		}
	}
	updatedAt, err := rewriteForm(ctx, tx, cur.internalID, in)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit staff update order form: %w", err)
	}
	return editResult(cur, in, soUUID, updatedAt, editable, cat), nil
}

// syncSalesOrder brings a Draft Sales Order along with a changed form: it makes
// sure the new entries exist in the catalogue, merges the lines (merge.go) and
// saves the order — all inside the caller's transaction. actor is the employee
// to record, 0 for none.
func syncSalesOrder(ctx context.Context, tx pgx.Tx, cur lockedForm, in Input, soUUID string,
	snap *salesorder.EditSnapshot, actor int, edit editInfo, now time.Time) (*inventory.CatalogResult, error) {
	orig, err := loadSubmitter(ctx, tx, cur.portalUserID)
	if err != nil {
		return nil, err
	}
	req := catalogRequest(in)
	req.ActorEmployeeID = actor
	cat, err := inventory.EnsureCatalog(ctx, tx, req)
	if err != nil {
		return nil, err
	}
	lines, err := planLines(cur.form.Areas, in.Areas, snap.Input.Items, cat)
	if err != nil {
		return nil, err
	}
	upd := mergeHeader(snap.Input, cur.form.Input, in, orig, edit, now)
	upd.Items = lines
	// The ship-to as stored: it is the job site, not the customer's own address
	// (see CreateOptions.ShippingAsGiven).
	if err := salesorder.UpdateInTx(ctx, tx, soUUID, upd, actor, salesorder.UpdateOptions{ShippingAsGiven: true}); err != nil {
		return nil, err
	}
	return cat, nil
}

// editResult is what a committed edit returns. cat is nil when the Sales Order
// was not touched.
func editResult(cur lockedForm, in Input, soUUID string, updatedAt time.Time, editable bool, cat *inventory.CatalogResult) *Result {
	res := &Result{
		Form: Form{
			ID: cur.form.ID, Status: cur.form.Status, CreatedAt: cur.form.CreatedAt,
			UpdatedAt: &updatedAt, Editable: editable, Input: in,
		},
		SalesOrderUUID: soUUID,
		Synced:         cat != nil,
	}
	if cat == nil {
		return res
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
	return res
}

// lockForm finds the customer's form and holds its row, so two edits (two
// logins of one customer) cannot interleave. ErrNotFound for anything that is
// not this customer's.
func lockForm(ctx context.Context, tx pgx.Tx, customerID int, id string) (lockedForm, error) {
	return lockFormWhere(ctx, tx,
		`f.customer_id = $1 AND f.customer_order_form_uuid = $2::uuid`, customerID, id)
}

// lockFormBySalesOrder finds the form a Sales Order was drafted from and holds
// its row. ErrNotFound for an order that has none (it was not placed from the
// portal).
func lockFormBySalesOrder(ctx context.Context, tx pgx.Tx, soUUID string) (lockedForm, error) {
	return lockFormWhere(ctx, tx,
		`f.sales_order_id = (SELECT sales_order_id FROM sales_order WHERE sales_order_uuid = $1::uuid)`, soUUID)
}

// lockFormWhere holds the one form row where selects, and loads it with its
// areas. where is a constant written in this file, never request text.
func lockFormWhere(ctx context.Context, tx pgx.Tx, where string, args ...any) (lockedForm, error) {
	var l lockedForm
	var formID int
	err := tx.QueryRow(ctx, `
		SELECT f.customer_order_form_id, f.portal_user_id::text, f.sales_order_id
		FROM customer_order_form f
		WHERE `+where+`
		ORDER BY f.customer_order_form_id
		LIMIT 1
		FOR UPDATE OF f`, args...).Scan(&formID, &l.portalUserID, &l.salesOrderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedForm{}, ErrNotFound
	}
	if err != nil {
		return lockedForm{}, fmt.Errorf("lock order form: %w", err)
	}
	f, internalID, err := scanForm(tx.QueryRow(ctx, formSelect+` WHERE f.customer_order_form_id = $1`, formID))
	if err != nil {
		return lockedForm{}, fmt.Errorf("load order form: %w", err)
	}
	f.Areas = []Area{}
	forms := []Form{f}
	if err := attachAreas(ctx, tx, forms, []int{internalID}); err != nil {
		return lockedForm{}, err
	}
	l.form, l.internalID = forms[0], internalID
	return l, nil
}

// editableSalesOrder locks the form's Sales Order and returns it when it is still
// a Draft. Locking before checking is the point: a staff submit that is racing
// this edit either finishes first (so this is refused) or waits for it.
func editableSalesOrder(ctx context.Context, tx pgx.Tx, cur lockedForm) (string, *salesorder.EditSnapshot, error) {
	if cur.salesOrderID == nil {
		return "", nil, ErrNotEditable
	}
	var soUUID string
	err := tx.QueryRow(ctx,
		`SELECT sales_order_uuid::text FROM sales_order WHERE sales_order_id = $1`, *cur.salesOrderID).Scan(&soUUID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, ErrNotEditable
	}
	if err != nil {
		return "", nil, fmt.Errorf("resolve sales order: %w", err)
	}
	snap, err := salesorder.LoadForEdit(ctx, tx, soUUID)
	if errors.Is(err, salesorder.ErrNotFound) {
		return "", nil, ErrNotEditable // staff deleted the draft
	}
	if err != nil {
		return "", nil, err
	}
	if snap.StatusCode != salesorder.DraftStatusCode {
		return "", nil, ErrNotEditable
	}
	return soUUID, snap, nil
}

// loadSubmitter reads who placed the order, to rebuild the memo the portal wrote.
// A login that no longer exists reads as nobody: the memo then no longer matches
// what was written and is appended to rather than replaced, which loses nothing.
func loadSubmitter(ctx context.Context, q workflow.Querier, portalUserID string) (Submitter, error) {
	var s Submitter
	err := q.QueryRow(ctx,
		`SELECT full_name, email FROM customer_portal_user WHERE id = $1::uuid`, portalUserID,
	).Scan(&s.FullName, &s.Email)
	if errors.Is(err, pgx.ErrNoRows) {
		return Submitter{}, nil
	}
	if err != nil {
		return Submitter{}, fmt.Errorf("load order submitter: %w", err)
	}
	return s, nil
}

// rewriteForm replaces the stored form with the edited one and returns when.
func rewriteForm(ctx context.Context, tx pgx.Tx, formID int, in Input) (time.Time, error) {
	var updatedAt time.Time
	err := tx.QueryRow(ctx, `
		UPDATE customer_order_form SET
			order_form_builder = $2, order_form_community = $3, order_form_plan = $4,
			order_form_phone = $5, order_form_email = $6,
			order_form_install_address = $7, order_form_city = $8, order_form_zip = $9,
			order_form_measure_date = $10::date, order_form_install_date = $11::date,
			order_form_special_instructions = $12,
			order_form_updated_at = CURRENT_TIMESTAMP
		WHERE customer_order_form_id = $1
		RETURNING order_form_updated_at`,
		formID, in.Builder, in.Community, in.Plan, in.Phone, in.Email,
		in.InstallAddress, in.City, in.Zip, in.MeasureDate, in.InstallDate, in.SpecialInstructions,
	).Scan(&updatedAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("update order form: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM customer_order_form_area WHERE customer_order_form_id = $1`, formID); err != nil {
		return time.Time{}, fmt.Errorf("clear order form areas: %w", err)
	}
	if err := insertAreas(ctx, tx, formID, in.Areas); err != nil {
		return time.Time{}, err
	}
	return updatedAt, nil
}

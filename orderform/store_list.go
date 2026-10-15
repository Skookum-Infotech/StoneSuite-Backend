package orderform

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/query"
	"stonesuite-backend/salesorder"
	"stonesuite-backend/workflow"
)

// listResolver is the whole filter/sort whitelist of the portal's order list:
// the id (the keyset tiebreaker) and the creation time (the default, newest-first
// sort). The list takes a cursor and a limit and nothing else.
type listResolver struct{}

// Resolve implements query.FieldResolver.
func (listResolver) Resolve(key string) (string, query.DataType, bool) {
	switch key {
	case "id":
		return "f.customer_order_form_uuid::text", query.TypeString, true
	case "created_at":
		return "f.order_form_created_at", query.TypeDate, true
	}
	return "", "", false
}

var _ query.FieldResolver = listResolver{}

// formSelect reads a form's own columns and whether the customer may still
// change it: its Sales Order exists and is still a Draft.
const formSelect = `
	SELECT f.customer_order_form_id, f.customer_order_form_uuid::text, f.order_form_status,
	       f.order_form_created_at, f.order_form_updated_at,
	       f.order_form_builder, f.order_form_community, f.order_form_plan, f.order_form_phone, f.order_form_email,
	       f.order_form_install_address, f.order_form_city, f.order_form_zip,
	       to_char(f.order_form_measure_date, 'YYYY-MM-DD'), to_char(f.order_form_install_date, 'YYYY-MM-DD'),
	       f.order_form_special_instructions,
	       EXISTS (
	           SELECT 1 FROM sales_order so
	           JOIN lkp_record_status rs ON rs.record_status_id = so.sales_order_status
	           WHERE so.sales_order_id = f.sales_order_id
	             AND so.sales_order_deleted_at IS NULL
	             AND rs.record_status_code = '` + salesorder.DraftStatusCode + `'
	       )
	FROM customer_order_form f`

// scanForm reads one row of formSelect, returning the form (without its areas)
// and its internal id.
func scanForm(row interface{ Scan(dest ...any) error }) (Form, int, error) {
	var (
		f       Form
		id      int
		updated *time.Time
	)
	err := row.Scan(&id, &f.ID, &f.Status, &f.CreatedAt, &updated,
		&f.Builder, &f.Community, &f.Plan, &f.Phone, &f.Email,
		&f.InstallAddress, &f.City, &f.Zip, &f.MeasureDate, &f.InstallDate,
		&f.SpecialInstructions, &f.Editable)
	f.UpdatedAt = updated
	return f, id, err
}

// List returns one customer's orders, newest first, with keyset pagination.
//
// The customer is a parameter taken from the portal session, never from the
// request: it is the boundary that keeps one customer from reading another's
// orders, the same way the portal document queries work (see
// salesorder.PortalSearch). A request's own filters can only narrow it further.
func List(ctx context.Context, pool *pgxpool.Pool, customerID int, req query.Request) (Page, error) {
	built, err := query.Build(req, listResolver{}, 2)
	if err != nil {
		return Page{}, err
	}
	where := []string{"f.customer_id = $1"}
	if built.Where != "" {
		where = append(where, built.Where)
	}
	if built.Keyset != "" {
		where = append(where, built.Keyset)
	}
	args := append([]any{customerID}, built.Args...)

	rows, err := pool.Query(ctx, formSelect+" WHERE "+strings.Join(where, " AND ")+
		" ORDER BY "+built.OrderBy+" LIMIT "+strconv.Itoa(built.EffLimit+1), args...)
	if err != nil {
		return Page{}, fmt.Errorf("list order forms: %w", err)
	}
	defer rows.Close()

	forms := []Form{}
	internalIDs := []int{}
	for rows.Next() {
		f, id, err := scanForm(rows)
		if err != nil {
			return Page{}, fmt.Errorf("scan order form: %w", err)
		}
		f.Areas = []Area{}
		forms = append(forms, f)
		internalIDs = append(internalIDs, id)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("list order forms: %w", err)
	}
	rows.Close()

	page := Page{Orders: forms}
	if len(forms) > built.EffLimit {
		page.HasMore = true
		page.Orders = forms[:built.EffLimit]
		internalIDs = internalIDs[:built.EffLimit]
		last := page.Orders[built.EffLimit-1]
		page.NextCursor = query.NextCursor(last.ID, built.Sort, last.CreatedAt)
	}
	if err := attachAreas(ctx, pool, page.Orders, internalIDs); err != nil {
		return Page{}, err
	}
	return page, nil
}

// attachAreas fills each form's Areas, in the order the customer listed them,
// with one query for the whole page. forms and ids are parallel.
func attachAreas(ctx context.Context, q workflow.Querier, forms []Form, ids []int) error {
	if len(forms) == 0 {
		return nil
	}
	index := make(map[int]int, len(ids))
	for i, id := range ids {
		index[id] = i
	}
	rows, err := q.Query(ctx, `
		SELECT customer_order_form_id, area_kind, area_name, area_color, area_finish, area_edge,
		       area_backsplash_height_in::float8, area_sink_model, area_cooktop_model,
		       area_waterfall_qty, area_pop_up_outlet_qty, area_half_wall, area_location,
		       area_faucet_spread, area_notes
		FROM customer_order_form_area
		WHERE customer_order_form_id = ANY($1::int[])
		ORDER BY customer_order_form_id, area_position`, ids)
	if err != nil {
		return fmt.Errorf("list order form areas: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			formID int
			a      Area
			height *float64
		)
		if err := rows.Scan(&formID, &a.Kind, &a.Name, &a.Color, &a.Finish, &a.Edge, &height,
			&a.SinkModel, &a.CooktopModel, &a.WaterfallQty, &a.PopUpOutlet, &a.HalfWall,
			&a.Location, &a.FaucetSpread, &a.Notes); err != nil {
			return fmt.Errorf("scan order form area: %w", err)
		}
		if height != nil {
			a.BacksplashHeight = strconv.FormatFloat(*height, 'f', -1, 64)
		}
		if a.FaucetSpread == nil {
			a.FaucetSpread = []string{}
		}
		if i, ok := index[formID]; ok {
			forms[i].Areas = append(forms[i].Areas, a)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list order form areas: %w", err)
	}
	return nil
}

// ForSalesOrder returns the form a Sales Order was drafted from, with its areas,
// for staff looking at that order. ErrNotFound when the order was not placed
// from the portal. The caller has already checked the person may see the Sales
// Order (permission and scope); nothing here is the customer's session.
func ForSalesOrder(ctx context.Context, q workflow.Querier, soUUID string) (*Form, error) {
	soUUID = strings.TrimSpace(soUUID)
	if !uuidPattern.MatchString(soUUID) {
		return nil, ErrNotFound
	}
	f, internalID, err := scanForm(q.QueryRow(ctx, formSelect+`
		WHERE f.sales_order_id = (SELECT sales_order_id FROM sales_order WHERE sales_order_uuid = $1::uuid)
		ORDER BY f.customer_order_form_id
		LIMIT 1`, soUUID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get order form for sales order: %w", err)
	}
	f.Areas = []Area{}
	forms := []Form{f}
	if err := attachAreas(ctx, q, forms, []int{internalID}); err != nil {
		return nil, err
	}
	return &forms[0], nil
}

// ErrNotFound means no order with that id belongs to the customer. An order that
// is another customer's is indistinguishable from one that does not exist, so
// ids cannot be probed.
var ErrNotFound = errors.New("order not found")

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Get returns one of the customer's orders with its areas. The customer is the
// session's, as in List; a malformed id is not-found rather than a driver error.
func Get(ctx context.Context, pool *pgxpool.Pool, customerID int, id string) (*Form, error) {
	id = strings.TrimSpace(id)
	if !uuidPattern.MatchString(id) {
		return nil, ErrNotFound
	}
	f, internalID, err := scanForm(pool.QueryRow(ctx, formSelect+`
		WHERE f.customer_id = $1 AND f.customer_order_form_uuid = $2::uuid`, customerID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get order form: %w", err)
	}
	f.Areas = []Area{}
	forms := []Form{f}
	if err := attachAreas(ctx, pool, forms, []int{internalID}); err != nil {
		return nil, err
	}
	return &forms[0], nil
}

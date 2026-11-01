package vendortransactions

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/dateonly"
)

// Keys of the chain's purchase order and its downstream document types.
const (
	keyPurchaseOrder = "purchase_order"
	keyItemReceipt   = "item_receipt"
)

// ErrPurchaseOrderNotFound is returned when the purchase order is not live,
// belongs to another vendor, or is outside the caller's read scope; handlers map
// it to 404 so ids cannot be enumerated.
var ErrPurchaseOrderNotFound = errors.New("purchase order not found")

// ErrPurchaseOrderForbidden is returned when the caller holds no purchase_order
// read grant at all; handlers log a permission denial but answer the same 404 as
// ErrPurchaseOrderNotFound.
var ErrPurchaseOrderForbidden = errors.New("purchase order read not permitted")

// ChainDoc is one readable document in the chain, with the registry's route
// segments so the client can link to it. Amount is nil for types without money.
type ChainDoc struct {
	ID         string         `json:"id"`
	Number     string         `json:"number"`
	Status     string         `json:"status"`
	StatusCode string         `json:"statusCode"`
	Date       *dateonly.Date `json:"date"`
	Amount     *float64       `json:"amount,omitempty"`
	Domain     string         `json:"domain"`
	Module     string         `json:"module"`

	balance *float64 // bill balance due, read only by deriveChainStatus
}

// ChainGroup is one downstream type: the documents the caller may read and how
// many linked live documents they may not (never described).
type ChainGroup struct {
	Items  []ChainDoc `json:"items"`
	Hidden int        `json:"hidden"`
}

// Chain is a purchase order's receipt, bill and payment trail.
type Chain struct {
	Status      string     `json:"status"`
	PO          ChainDoc   `json:"po"`
	Receipts    ChainGroup `json:"receipts"`
	Bills       ChainGroup `json:"bills"`
	Payments    ChainGroup `json:"payments"`
	BillsHidden bool       `json:"billsHidden"`
}

// GetChain returns the receipt -> bill -> payment trail of one purchase order of
// the vendor. The order must be live, the vendor's own and readable under the
// caller's purchase_order grant (own-scoped included), else
// ErrPurchaseOrderNotFound; a caller with no purchase_order read grant gets
// ErrPurchaseOrderForbidden. Each downstream type lists only what the caller may
// read; Hidden counts linked documents only for a type the caller has no read
// grant on (an own-scoped reader is never told how many others' records exist).
func GetChain(ctx context.Context, pool *pgxpool.Pool, identityID, vendorUUID, poUUID string) (Chain, error) {
	if !uuidRe.MatchString(poUUID) {
		return Chain{}, &InvalidParamError{Param: "po", Msg: "must be a purchase order id"}
	}
	sc, err := loadScope(ctx, pool, identityID, vendorUUID)
	if err != nil {
		return Chain{}, err
	}
	poSrc, ok := sc.byKey(keyPurchaseOrder)
	if !ok {
		return Chain{}, ErrPurchaseOrderForbidden
	}
	var out Chain
	var poID int
	sql, args := buildChainPOSQL(poSrc, sc.vendorID, sc.employee, poUUID)
	err = pool.QueryRow(ctx, sql, args...).Scan(&out.PO.ID, &out.PO.Number, &out.PO.Status, &out.PO.StatusCode, &poID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Chain{}, ErrPurchaseOrderNotFound
	}
	if err != nil {
		return Chain{}, fmt.Errorf("load chain purchase order: %w", err)
	}
	out.PO.Domain, out.PO.Module = poSrc.Domain, poSrc.Module

	for _, t := range []struct {
		key string
		dst *ChainGroup
	}{{keyItemReceipt, &out.Receipts}, {keyVendorBill, &out.Bills}, {keyVendorPayment, &out.Payments}} {
		if *t.dst, err = loadChainGroup(ctx, pool, sc, t.key, poID); err != nil {
			return Chain{}, err
		}
	}

	// Hidden is non-zero only for a type with no read grant, so own-scoped callers
	// never feed records they cannot see into the status.
	out.BillsHidden = out.Bills.Hidden > 0
	facts := chainFacts{
		poCode:      out.PO.StatusCode,
		receipts:    len(out.Receipts.Items) + out.Receipts.Hidden,
		billsHidden: out.BillsHidden,
	}
	for _, b := range out.Bills.Items {
		facts.bills = append(facts.bills, chainBill{statusCode: b.StatusCode, balanceDue: b.balance})
	}
	out.Status = deriveChainStatus(facts)
	return out, nil
}

// loadChainGroup reads one downstream type: the readable linked documents plus
// the count of linked documents the caller cannot see. The count is only taken
// when the caller has no read grant (everything is hidden) or reads all records;
// an own-scoped caller always gets Hidden == 0.
func loadChainGroup(ctx context.Context, pool *pgxpool.Pool, sc scope, key string, poID int) (ChainGroup, error) {
	g := ChainGroup{Items: []ChainDoc{}}
	src, ok := sc.byKey(key)
	if !ok {
		total, err := countChain(ctx, pool, key, sc.vendorID, poID)
		if err != nil {
			return ChainGroup{}, err
		}
		g.Hidden = total
		return g, nil
	}
	var sql string
	var args []any
	sql, args = buildChainItemsSQL(src, sc.vendorID, sc.employee, poID)
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return ChainGroup{}, fmt.Errorf("list chain %s: %w", key, err)
	}
	defer rows.Close()
	for rows.Next() {
		d := ChainDoc{Domain: src.Domain, Module: src.Module}
		if err := rows.Scan(&d.ID, &d.Number, &d.Status, &d.StatusCode, &d.Date, &d.Amount, &d.balance); err != nil {
			return ChainGroup{}, fmt.Errorf("scan chain %s: %w", key, err)
		}
		g.Items = append(g.Items, d)
	}
	if err := rows.Err(); err != nil {
		return ChainGroup{}, fmt.Errorf("list chain %s: %w", key, err)
	}
	if src.own {
		return g, nil
	}
	total, err := countChain(ctx, pool, key, sc.vendorID, poID)
	if err != nil {
		return ChainGroup{}, err
	}
	g.Hidden = total - len(g.Items)
	if g.Hidden < 0 {
		g.Hidden = 0
	}
	return g, nil
}

// countChain counts every live document of the type linked to the purchase order.
func countChain(ctx context.Context, pool *pgxpool.Pool, key string, vendorID, poID int) (int, error) {
	var total int
	sql, args := buildChainCountSQL(key, vendorID, poID)
	if err := pool.QueryRow(ctx, sql, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("count chain %s: %w", key, err)
	}
	return total, nil
}

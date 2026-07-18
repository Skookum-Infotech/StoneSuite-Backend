package salesorder

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/workflow"
)

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

// resolvedLine is a line after catalog/free-text resolution, ready to price
// and insert.
type resolvedLine struct {
	lineNumber      int
	inventoryItemID *int // internal FK, nil for free-text
	warehouseID     *int
	sku, name, desc string
	unitID          *int
	unitCode        string
	quantity        float64
	unitPrice       float64
	discountPercent float64
	taxRateID       *int
	taxPercent      float64
	money           LineMoney
}

// resolveLines validates and resolves every input line against the catalog
// (or free text), computing each line's stored money (spec §9). headerTax is
// the header's default tax percent, used when a line has no tax rate.
func resolveLines(ctx context.Context, q workflow.Querier, items []LineInput2, headerTax float64) ([]resolvedLine, error) {
	if len(items) == 0 {
		return nil, ClientError{Msg: "At least one line item is required."}
	}
	out := make([]resolvedLine, 0, len(items))
	seenLine := map[int]bool{}
	for _, in := range items {
		if in.LineNumber <= 0 {
			return nil, ClientError{Msg: "Each line item needs a positive line number."}
		}
		if seenLine[in.LineNumber] {
			return nil, ClientError{Msg: fmt.Sprintf("Duplicate line number %d.", in.LineNumber)}
		}
		seenLine[in.LineNumber] = true
		if in.Quantity <= 0 {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: quantity must be greater than zero.", in.LineNumber)}
		}
		if in.UnitPrice < 0 {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: unit price cannot be negative.", in.LineNumber)}
		}
		if in.DiscountPercent < 0 || in.DiscountPercent > 100 {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: discount percent must be between 0 and 100.", in.LineNumber)}
		}

		rl := resolvedLine{
			lineNumber:      in.LineNumber,
			warehouseID:     in.WarehouseID,
			quantity:        in.Quantity,
			unitPrice:       in.UnitPrice,
			discountPercent: in.DiscountPercent,
			taxRateID:       in.TaxRateID,
		}

		if in.InventoryItemUUID != "" {
			item, err := resolveInventoryItem(ctx, q, in.InventoryItemUUID)
			if err != nil {
				return nil, err
			}
			id := item.internalID
			rl.inventoryItemID = &id
			rl.sku, rl.name, rl.desc = item.sku, item.name, item.desc
			rl.unitID, rl.unitCode = item.unitID, item.unitCode
			if rl.unitPrice == 0 {
				rl.unitPrice = item.unitPrice
			}
			if rl.taxRateID == nil {
				rl.taxRateID = item.taxRateID
			}
		} else if strings.TrimSpace(in.Description) == "" {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: either an inventory item or a description is required.", in.LineNumber)}
		} else {
			rl.desc = in.Description
			rl.sku = strings.TrimSpace(in.SKU)
			rl.name = strings.TrimSpace(in.ItemName)
			rl.unitCode = strings.TrimSpace(in.UnitCode)
		}

		if rl.taxRateID != nil {
			pct, err := taxPercentForRate(ctx, q, *rl.taxRateID)
			if err != nil {
				return nil, err
			}
			rl.taxPercent = pct
		} else if in.TaxPercent != nil {
			if *in.TaxPercent < 0 || *in.TaxPercent > 100 {
				return nil, ClientError{Msg: fmt.Sprintf("Line %d: tax percent must be between 0 and 100.", in.LineNumber)}
			}
			rl.taxPercent = *in.TaxPercent
		} else {
			rl.taxPercent = headerTax
		}

		rl.money = ComputeLine(LineInput{
			Quantity: rl.quantity, UnitPrice: rl.unitPrice,
			DiscountPercent: rl.discountPercent, TaxPercent: rl.taxPercent,
		})
		out = append(out, rl)
	}
	return out, nil
}

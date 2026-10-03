package docextractjob

import (
	"context"
	"fmt"
	"strings"

	"stonesuite-backend/docextract"
	"stonesuite-backend/duplicate"
)

// SQL expressions the item lookup normalizes with; they must stay in step with
// NormalizeSKU and duplicate.Key.
const (
	itemSKUExpr  = `regexp_replace(lower(i.inventory_item_sku), '[\s\-_.]', '', 'g')`
	itemNameExpr = `lower(btrim(regexp_replace(i.inventory_item_name, '\s+', ' ', 'g')))`
)

// itemRow is a catalog item with the unit data and comparison keys.
type itemRow struct {
	info     ItemInfo
	unitName string
	skuKey   string
	nameKey  string
}

// lkpToDocCategory maps lkp_unit.unit_category onto docextract's categories
// where the names differ.
var lkpToDocCategory = map[string]string{"weight": "mass"}

// itemIndex holds the batch lookup results.
type itemIndex struct {
	byUUID map[string]*itemRow
	bySKU  map[string][]*itemRow
	byName map[string][]*itemRow
}

// ResolveItems resolves every product/addon line against the catalog in one
// alias query and one item query. Learned aliases (scoped to partyUUID) win,
// then an exact normalized SKU, then an exact normalized name; an ambiguous
// key is flagged rather than guessed. Doc-vs-catalog UoM conversion keeps the
// line amount unchanged; the document price is kept and flagged when it
// differs from the catalog.
func (r *Resolver) ResolveItems(ctx context.Context, partyUUID string, lines []docextract.Line) ([]LineMatch, error) {
	var skus, names, aliasKeys []string
	for _, l := range lines {
		if !isItemLine(l) {
			continue
		}
		if k := NormalizeSKU(l.SKU.Value); k != "" {
			skus = append(skus, k)
			aliasKeys = append(aliasKeys, aliasPrefixSKU+k)
		}
		if k := duplicate.Key(l.Description.Value); k != "" {
			names = append(names, k)
			aliasKeys = append(aliasKeys, aliasPrefixDesc+k)
		}
	}
	aliases, err := r.itemAliases(ctx, partyUUID, aliasKeys)
	if err != nil {
		return nil, err
	}
	aliasUUIDs := make([]string, 0, len(aliases))
	for _, u := range aliases {
		aliasUUIDs = append(aliasUUIDs, u)
	}
	idx, err := r.loadItems(ctx, skus, names, aliasUUIDs)
	if err != nil {
		return nil, err
	}

	out := make([]LineMatch, 0, len(lines))
	for i, l := range lines {
		if isItemLine(l) {
			out = append(out, matchLine(i, l, idx, aliases))
		}
	}
	return out, nil
}

// isItemLine reports whether a parsed line becomes a flat sales-order item line.
func isItemLine(l docextract.Line) bool {
	return l.Kind == docextract.KindProduct || l.Kind == docextract.KindAddon
}

// itemAliases returns the learned alias_key -> item uuid map for this party.
func (r *Resolver) itemAliases(ctx context.Context, partyUUID string, keys []string) (map[string]string, error) {
	out := map[string]string{}
	if !validUUID(partyUUID) || len(keys) == 0 {
		return out, nil
	}
	rows, err := r.db.Query(ctx, `
		SELECT alias_key, item_uuid::text FROM document_item_alias
		WHERE party_uuid = $1::uuid AND alias_key = ANY($2)`, partyUUID, keys)
	if err != nil {
		return nil, fmt.Errorf("query item aliases: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, u string
		if err := rows.Scan(&k, &u); err != nil {
			return nil, fmt.Errorf("scan item alias: %w", err)
		}
		out[k] = u
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate item aliases: %w", err)
	}
	return out, nil
}

// loadItems fetches every item matching any SKU key, name key or alias uuid.
func (r *Resolver) loadItems(ctx context.Context, skus, names, uuids []string) (itemIndex, error) {
	idx := itemIndex{byUUID: map[string]*itemRow{}, bySKU: map[string][]*itemRow{}, byName: map[string][]*itemRow{}}
	if len(skus)+len(names)+len(uuids) == 0 {
		return idx, nil
	}
	rows, err := r.db.Query(ctx, `
		SELECT i.inventory_item_uuid::text, i.inventory_item_name, i.inventory_item_sku,
		       u.unit_code, u.unit_name, u.unit_category,
		       ROUND(i.inventory_item_unit_price * 100)::bigint, i.inventory_item_is_active,
		       `+itemSKUExpr+`, `+itemNameExpr+`
		FROM inventory_item i
		JOIN lkp_unit u ON u.unit_id = i.inventory_item_unit_id
		WHERE i.inventory_item_deleted_at IS NULL
		  AND (`+itemSKUExpr+` = ANY($1) OR `+itemNameExpr+` = ANY($2) OR i.inventory_item_uuid = ANY($3::uuid[]))`,
		skus, names, uuids)
	if err != nil {
		return idx, fmt.Errorf("query items: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		it := &itemRow{}
		if err := rows.Scan(&it.info.UUID, &it.info.Name, &it.info.SKU, &it.info.UnitCode, &it.unitName,
			&it.info.UnitCategory, &it.info.CatalogPriceCents, &it.info.Active, &it.skuKey, &it.nameKey); err != nil {
			return idx, fmt.Errorf("scan item: %w", err)
		}
		idx.byUUID[it.info.UUID] = it
		idx.bySKU[it.skuKey] = append(idx.bySKU[it.skuKey], it)
		idx.byName[it.nameKey] = append(idx.byName[it.nameKey], it)
	}
	if err := rows.Err(); err != nil {
		return idx, fmt.Errorf("iterate items: %w", err)
	}
	return idx, nil
}

// matchLine resolves one line against the loaded index.
func matchLine(i int, l docextract.Line, idx itemIndex, aliases map[string]string) LineMatch {
	m := LineMatch{Index: i}
	skuKey, descKey := NormalizeSKU(l.SKU.Value), duplicate.Key(l.Description.Value)

	var item *itemRow
	ambiguous := false
	for _, ak := range []string{aliasPrefixSKU + skuKey, aliasPrefixDesc + descKey} {
		if u, ok := aliases[ak]; ok {
			if it := idx.byUUID[u]; it != nil {
				item, m.MatchedBy, m.Source = it, MatchAlias, docextract.SourceLearned
				break
			}
		}
	}
	if item == nil && skuKey != "" {
		item, ambiguous = pickUnique(idx.bySKU[skuKey])
		if item != nil {
			m.MatchedBy, m.Source = MatchSKU, docextract.SourceCatalog
		}
	}
	if item == nil && !ambiguous && descKey != "" {
		item, ambiguous = pickUnique(idx.byName[descKey])
		if item != nil {
			m.MatchedBy, m.Source = MatchName, docextract.SourceCatalog
		}
	}
	switch {
	case item != nil:
		decorate(&m, l, item)
	case ambiguous:
		m.Flags = append(m.Flags, LineFlagItemAmbiguous)
	default:
		m.Flags = append(m.Flags, LineFlagItemUnmatched)
	}
	return m
}

// pickUnique returns the only row, or ambiguous=true when several share the key.
func pickUnique(rows []*itemRow) (item *itemRow, ambiguous bool) {
	switch len(rows) {
	case 0:
		return nil, false
	case 1:
		return rows[0], false
	default:
		return nil, true
	}
}

// decorate attaches the item, UoM conversion and price/active flags to m.
func decorate(m *LineMatch, l docextract.Line, it *itemRow) {
	info := it.info
	m.Item = &info
	if !info.Active {
		m.Flags = append(m.Flags, LineFlagItemInactive)
	}
	conv, flag := convertUoM(l, it)
	if flag != "" {
		m.Flags = append(m.Flags, flag)
	}
	m.Converted = conv

	price := int64(l.UnitPriceCents)
	if conv != nil {
		price = conv.UnitPriceCents
	}
	if l.UnitPrice.Found() && !hasLineFlag(l.Flags, docextract.FlagPriceUnparsed) && price != info.CatalogPriceCents {
		m.Flags = append(m.Flags, LineFlagPriceDiffers)
	}
}

// convertUoM converts the line into the item's unit when the document unit
// differs but shares its category. It returns a flag instead when the units
// cannot be reconciled; an absent document UoM is neither.
func convertUoM(l docextract.Line, it *itemRow) (*UnitConversion, string) {
	docUoM := strings.TrimSpace(l.UoM.Value)
	if docUoM == "" || l.QtyMilli <= 0 {
		return nil, ""
	}
	dCanon, dCat, ok := docextract.NormalizeUnit(docUoM)
	if !ok {
		return nil, LineFlagUoMUnknown
	}
	iCanon, iCat, iok := docextract.NormalizeUnit(it.info.UnitCode)
	if !iok {
		iCanon, iCat, iok = docextract.NormalizeUnit(it.unitName)
	}
	if !iok {
		return nil, LineFlagUoMMismatch
	}
	if iCanon == dCanon {
		return nil, ""
	}
	lkpCat := it.info.UnitCategory
	if mapped, has := lkpToDocCategory[lkpCat]; has {
		lkpCat = mapped
	}
	if iCat != dCat || lkpCat != dCat {
		return nil, LineFlagUoMMismatch
	}
	qty, price, cok := docextract.Convert(l.QtyMilli, l.UnitPriceCents, docUoM, iCanon)
	if !cok {
		return nil, LineFlagUoMMismatch
	}
	return &UnitConversion{
		FromUoM: docUoM, ToUoM: it.info.UnitCode, FromQtyMilli: l.QtyMilli,
		QtyMilli: qty, UnitPriceCents: int64(price),
	}, ""
}

// hasLineFlag reports whether flags contains f.
func hasLineFlag(flags []string, f string) bool {
	for _, x := range flags {
		if x == f {
			return true
		}
	}
	return false
}

package inventory

// catalog_ensure.go — find-or-create of kits and items by name, inside the
// caller's transaction. It exists for documents built from free text rather
// than picked from the catalogue (the customer portal's order form): every area
// becomes a kit and every entry an item, so staff finish the details on real
// catalogue rows instead of on free-text lines.
//
// Names are matched the way the duplicate guards match them (case-insensitive,
// whitespace collapsed — duplicate.Key), so an entry never creates a second
// "Serene" next to one a person already added.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/duplicate"
)

const (
	// MaxItemNameLen matches the inventory_item.inventory_item_name column.
	MaxItemNameLen = 150

	// ensureUnitCode is the unit a created item gets: "Each". The tenant picks
	// the real one when it completes the item.
	ensureUnitCode = "EA"

	catalogLockKitPrefix  = "catalog:kit:"
	catalogLockItemPrefix = "catalog:item:"

	// maxSKUPrefixLen leaves room in the 50-character SKU for the number.
	maxSKUPrefixLen = 20
)

// CatalogKit is one kit to ensure and the names of the items it must hold.
type CatalogKit struct {
	Name  string
	Items []string
}

// CatalogRequest is what EnsureCatalog is asked for.
type CatalogRequest struct {
	Kits []CatalogKit
	// SKUPrefix starts the SKU of every item that has to be created ("PORTAL-");
	// the item's id follows, so it can never collide with another created item.
	SKUPrefix string
	// KitDescription / ItemDescription are written on a kit or item that has to
	// be created, so staff can tell where it came from.
	KitDescription  string
	ItemDescription string
	ActorEmployeeID int
}

// CatalogRef is a kit or item the catalogue now holds.
type CatalogRef struct {
	// ID is the external uuid.
	ID string
	// Name is the catalogue's own spelling, which may differ from the request's
	// in case or spacing when an existing row was reused.
	Name string
	// Created is true when this call created it.
	Created bool
}

// CatalogResult maps duplicate.Key(name) to what the request's names resolved to.
type CatalogResult struct {
	Kits  map[string]CatalogRef
	Items map[string]CatalogRef
}

// ensured is a CatalogRef with its internal id, which only this file needs.
type ensured struct {
	ref CatalogRef
	id  int
}

// catalogPlan is the request with its names de-duplicated by key.
type catalogPlan struct {
	kitKeys   []string
	kitNames  map[string]string
	itemKeys  []string
	itemNames map[string]string
	members   map[string][]string // kit key -> item keys, in request order
}

func planCatalog(req CatalogRequest) (catalogPlan, error) {
	if len(req.SKUPrefix) > maxSKUPrefixLen {
		return catalogPlan{}, fmt.Errorf("ensure catalog: SKU prefix %q is too long", req.SKUPrefix)
	}
	p := catalogPlan{
		kitNames: map[string]string{}, itemNames: map[string]string{}, members: map[string][]string{},
	}
	for _, k := range req.Kits {
		kitName := strings.Join(strings.Fields(k.Name), " ")
		kitKey := duplicate.Key(kitName)
		if kitKey == "" {
			return p, ClientError{Msg: "A kit name is required."}
		}
		if utf8.RuneCountInString(kitName) > MaxKitNameLen {
			return p, ClientError{Msg: fmt.Sprintf("A kit name can be at most %d characters.", MaxKitNameLen)}
		}
		if _, seen := p.kitNames[kitKey]; !seen {
			p.kitNames[kitKey] = kitName
			p.kitKeys = append(p.kitKeys, kitKey)
		}
		for _, raw := range k.Items {
			itemName := strings.Join(strings.Fields(raw), " ")
			itemKey := duplicate.Key(itemName)
			if itemKey == "" {
				continue
			}
			if utf8.RuneCountInString(itemName) > MaxItemNameLen {
				return p, ClientError{Msg: fmt.Sprintf("An item name can be at most %d characters.", MaxItemNameLen)}
			}
			if _, seen := p.itemNames[itemKey]; !seen {
				p.itemNames[itemKey] = itemName
				p.itemKeys = append(p.itemKeys, itemKey)
			}
			if !containsString(p.members[kitKey], itemKey) {
				p.members[kitKey] = append(p.members[kitKey], itemKey)
			}
		}
	}
	return p, nil
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// EnsureCatalog makes sure every kit and item the request names exists, creating
// the missing ones, and files each item into the kits that list it. Everything
// happens in tx, so a caller that rolls back leaves the catalogue untouched.
//
// Reused rows are left exactly as they are, apart from a kit gaining members. A
// kit that already holds MaxKitMembers items simply stops growing; the document
// still gets its lines.
func EnsureCatalog(ctx context.Context, tx pgx.Tx, req CatalogRequest) (*CatalogResult, error) {
	plan, err := planCatalog(req)
	if err != nil {
		return nil, err
	}
	if err := lockCatalogNames(ctx, tx, plan); err != nil {
		return nil, err
	}
	kits, err := ensureKits(ctx, tx, plan, req)
	if err != nil {
		return nil, err
	}
	items, err := ensureItems(ctx, tx, plan, req)
	if err != nil {
		return nil, err
	}
	if err := fileItemsInKits(ctx, tx, plan, kits, items, req.ActorEmployeeID); err != nil {
		return nil, err
	}
	out := &CatalogResult{Kits: map[string]CatalogRef{}, Items: map[string]CatalogRef{}}
	for k, e := range kits {
		out.Kits[k] = e.ref
	}
	for k, e := range items {
		out.Items[k] = e.ref
	}
	return out, nil
}

// lockCatalogNames serialises concurrent callers on the names they are about to
// create. There is no unique index on an item's name, so without this two orders
// submitted together would each create "Serene". Locks are taken in sorted order
// so two callers can never wait on each other.
func lockCatalogNames(ctx context.Context, tx pgx.Tx, plan catalogPlan) error {
	keys := make([]string, 0, len(plan.kitKeys)+len(plan.itemKeys))
	for _, k := range plan.kitKeys {
		keys = append(keys, catalogLockKitPrefix+k)
	}
	for _, k := range plan.itemKeys {
		keys = append(keys, catalogLockItemPrefix+k)
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	if _, err := tx.Exec(ctx, `
		SELECT pg_advisory_xact_lock(hashtext(s.k))
		FROM (SELECT k FROM unnest($1::text[]) AS k ORDER BY k) s`, keys); err != nil {
		return fmt.Errorf("lock catalog names: %w", err)
	}
	return nil
}

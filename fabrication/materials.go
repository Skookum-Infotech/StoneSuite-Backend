package fabrication

// materials.go — what a job needs in slab material against what it holds.
//
// A job is spawned from a sales order, whose lines say what the customer bought
// (say 45 sq ft of Absolute Black). That is only an estimate: the fabricators
// measure the space (the template) and draw the pieces, and the pieces' areas are
// what must actually be cut. So each material shows both — what the order calls
// for and what the blueprint needs — next to the slab area allocated to cover it,
// and cutting is refused while the allocation falls short.
//
// Only slab-tracked items appear: they are the ones cut from individually
// allocated slabs. A quantity-tracked line (a sink, say) is reserved by the sales
// order and deducted when it is filled.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/inventory"
)

// Where a material's Needed figure comes from.
const (
	// BasisBlueprint: the pieces drawn for the job — what will really be cut.
	BasisBlueprint = "blueprint"
	// BasisOrder: no pieces yet, so the quantity the sales order asked for.
	BasisOrder = "order"
)

// materialEpsilon is half a unit at the DECIMAL(14,3) scale quantities are stored
// at; a gap smaller than this is rounding, not a shortfall.
const materialEpsilon = 0.0005

// Material is one slab-tracked material on a job.
type Material struct {
	ItemID   string `json:"itemId"` // inventory_item_uuid
	SKU      string `json:"sku"`
	Name     string `json:"name"`
	UnitCode string `json:"unitCode"`

	// Ordered is what the job's sales order calls for.
	Ordered float64 `json:"ordered"`
	// Needed is what cutting must be covered for: the area of the pieces drawn
	// for this material, or Ordered while there are none. Basis says which.
	Needed     float64 `json:"needed"`
	Basis      string  `json:"basis"`
	PieceCount int     `json:"pieceCount"`

	// Allocated is the area of slab held for this job (reserved or already cut);
	// Consumed is the part of that already cut. InStock is the area of this
	// material's slabs on the shelf and free to allocate.
	Allocated float64 `json:"allocated"`
	Consumed  float64 `json:"consumed"`
	InStock   float64 `json:"inStock"`

	// Shortfall is Needed - Allocated, never negative: how much more slab must be
	// allocated before the job can be cut.
	Shortfall float64 `json:"shortfall"`
}

type materialAcc struct {
	Material
	itemID   int
	category string
}

// LoadMaterials returns the job's slab-tracked materials, or ErrNotFound.
func LoadMaterials(ctx context.Context, q querier, jobUUID string) ([]Material, error) {
	var jobID int
	err := q.QueryRow(ctx, `
		SELECT fabrication_job_id FROM fabrication_job
		WHERE fabrication_job_uuid = $1 AND fabrication_job_deleted_at IS NULL`, jobUUID).Scan(&jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("resolve job for materials: %w", err)
	}
	return loadMaterials(ctx, q, jobID)
}

// loadMaterials computes the materials of one job from its sales order lines,
// its pieces and its slab allocations.
func loadMaterials(ctx context.Context, q querier, jobID int) ([]Material, error) {
	// 1. Materials: the slab-tracked items on the order's live lines.
	rows, err := q.Query(ctx, `
		SELECT ii.inventory_item_id, ii.inventory_item_uuid, ii.inventory_item_sku, ii.inventory_item_name,
		       u.unit_code, u.unit_category, SUM(soi.quantity)
		FROM fabrication_job fj
		JOIN sales_order_item soi ON soi.sales_order_id = fj.sales_order_id AND soi.item_deleted_at IS NULL
		JOIN inventory_item ii ON ii.inventory_item_id = soi.inventory_item_id
		JOIN lkp_unit u ON u.unit_id = ii.inventory_item_unit_id
		WHERE fj.fabrication_job_id = $1 AND ii.inventory_item_tracking = 'serialized'
		GROUP BY ii.inventory_item_id, ii.inventory_item_uuid, ii.inventory_item_sku, ii.inventory_item_name,
		         u.unit_code, u.unit_category
		ORDER BY ii.inventory_item_sku`, jobID)
	if err != nil {
		return nil, fmt.Errorf("load job materials: %w", err)
	}
	var mats []*materialAcc
	byID := map[int]*materialAcc{}
	for rows.Next() {
		m := &materialAcc{}
		if err := rows.Scan(&m.itemID, &m.ItemID, &m.SKU, &m.Name, &m.UnitCode, &m.category, &m.Ordered); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan job material: %w", err)
		}
		mats = append(mats, m)
		byID[m.itemID] = m
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(mats) == 0 {
		return []Material{}, nil
	}

	// 2. The blueprint: each piece's area, credited to the material of the order
	// line it is linked to. An unlinked piece belongs to the order's only
	// material when there is exactly one; with several it cannot be attributed
	// and is left out rather than guessed.
	prows, err := q.Query(ctx, `
		SELECT fi.piece_length_mm, fi.piece_width_mm, soi.inventory_item_id
		FROM fabrication_job_item fi
		LEFT JOIN sales_order_item soi ON soi.sales_order_item_id = fi.sales_order_item_id
		WHERE fi.fabrication_job_id = $1 AND fi.item_deleted_at IS NULL`, jobID)
	if err != nil {
		return nil, fmt.Errorf("load job pieces for materials: %w", err)
	}
	for prows.Next() {
		var length, width float64
		var linkedItem *int
		if err := prows.Scan(&length, &width, &linkedItem); err != nil {
			prows.Close()
			return nil, fmt.Errorf("scan job piece for materials: %w", err)
		}
		var target *materialAcc
		switch {
		case linkedItem != nil && byID[*linkedItem] != nil:
			target = byID[*linkedItem]
		case linkedItem == nil && len(mats) == 1:
			target = mats[0]
		}
		if target == nil {
			continue
		}
		area, aerr := inventory.AreaFor(length, width, target.UnitCode, target.category)
		if aerr != nil {
			continue // a piece in a unit that has no area is not slab material
		}
		target.Needed += area
		target.PieceCount++
	}
	prows.Close()
	if err := prows.Err(); err != nil {
		return nil, err
	}

	// 3. What the job holds.
	arows, err := q.Query(ctx, `
		SELECT s.inventory_item_id,
		       COALESCE(SUM(s.slab_area) FILTER (WHERE fjs.allocation_status IN ('reserved','consumed')), 0),
		       COALESCE(SUM(s.slab_area) FILTER (WHERE fjs.allocation_status = 'consumed'), 0)
		FROM fabrication_job_slab fjs
		JOIN inventory_slab s ON s.inventory_slab_id = fjs.inventory_slab_id
		WHERE fjs.fabrication_job_id = $1
		GROUP BY s.inventory_item_id`, jobID)
	if err != nil {
		return nil, fmt.Errorf("load job allocations: %w", err)
	}
	for arows.Next() {
		var itemID int
		var allocated, consumed float64
		if err := arows.Scan(&itemID, &allocated, &consumed); err != nil {
			arows.Close()
			return nil, fmt.Errorf("scan job allocation: %w", err)
		}
		if m := byID[itemID]; m != nil {
			m.Allocated, m.Consumed = allocated, consumed
		}
	}
	arows.Close()
	if err := arows.Err(); err != nil {
		return nil, err
	}

	// 4. What is on the shelf.
	ids := make([]int, 0, len(mats))
	for _, m := range mats {
		ids = append(ids, m.itemID)
	}
	srows, err := q.Query(ctx, `
		SELECT inventory_item_id, COALESCE(SUM(slab_area), 0)
		FROM inventory_slab
		WHERE inventory_item_id = ANY($1) AND slab_status = 'available' AND inspection_status IN ('accepted','legacy') AND slab_deleted_at IS NULL
		GROUP BY inventory_item_id`, ids)
	if err != nil {
		return nil, fmt.Errorf("load slab stock for materials: %w", err)
	}
	for srows.Next() {
		var itemID int
		var area float64
		if err := srows.Scan(&itemID, &area); err != nil {
			srows.Close()
			return nil, fmt.Errorf("scan slab stock for materials: %w", err)
		}
		if m := byID[itemID]; m != nil {
			m.InStock = area
		}
	}
	srows.Close()
	if err := srows.Err(); err != nil {
		return nil, err
	}

	out := make([]Material, 0, len(mats))
	for _, m := range mats {
		m.Basis = BasisBlueprint
		if m.PieceCount == 0 {
			m.Needed, m.Basis = m.Ordered, BasisOrder
		}
		m.Needed = roundArea(m.Needed)
		m.Allocated, m.Consumed, m.InStock = roundArea(m.Allocated), roundArea(m.Consumed), roundArea(m.InStock)
		m.Shortfall = shortfall(m.Needed, m.Allocated)
		out = append(out, m.Material)
	}
	return out, nil
}

// roundArea rounds to the three decimals areas are stored at, so what is
// compared and shown is what the database holds.
func roundArea(v float64) float64 {
	return float64(int64(v*1000+0.5*sign(v))) / 1000
}

func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}

// shortfall is how much more must be allocated to cover need; zero when covered
// (within rounding).
func shortfall(needed, allocated float64) float64 {
	gap := needed - allocated
	if gap <= materialEpsilon {
		return 0
	}
	return roundArea(gap)
}

// requireMaterialAllocated refuses cutting while any material's allocated slab
// area falls short of what it needs. Consuming slabs is the step that deducts
// stock and cannot be undone, so this is the last point to catch a job that was
// never given the stone to do the work.
func requireMaterialAllocated(ctx context.Context, q querier, jobID int) error {
	mats, err := loadMaterials(ctx, q, jobID)
	if err != nil {
		return err
	}
	var short []string
	for _, m := range mats {
		if m.Shortfall > 0 {
			short = append(short, fmt.Sprintf("%s needs %s %s but only %s is allocated",
				m.Name, formatQty(m.Needed), m.UnitCode, formatQty(m.Allocated)))
		}
	}
	if len(short) == 0 {
		return nil
	}
	sort.Strings(short)
	return ClientError{Msg: "Cutting cannot start until enough material is allocated: " +
		strings.Join(short, "; ") + ". Allocate more slabs on the Materials tab first."}
}

func formatQty(v float64) string {
	return strconv.FormatFloat(roundArea(v), 'f', -1, 64)
}

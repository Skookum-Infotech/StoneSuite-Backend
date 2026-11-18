package controllers

import (
	"errors"
	"math"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

// The edge types the drawing editor's Edges tool paints onto a piece's sides
// (drawing_edge_type). Each carries a name, a price per linear unit, a short
// alias, the colour it is drawn in and the 3D finish (profile) it will show
// later. A shop starts with four (Ease, Appliance, Finished, Unfinished) and
// adds its own.

const (
	maxEdgeTypeNameRunes  = 64
	maxEdgeTypeAliasRunes = 8
	// maxEdgeTypePrice matches the column's NUMERIC(12,2) with room to spare.
	maxEdgeTypePrice = 9999999999.99
	edgeTypePricing  = "linear"
)

// allowedEdgeFinishes mirrors the frontend's EdgeFinish union (src/types/drawing.ts)
// and the table's CHECK constraint.
var allowedEdgeFinishes = map[string]bool{
	"none": true, "raw": true, "ogee": true, "beveled": true, "eased": true,
	"waterfall": true, "halfBullnose": true, "bullnose": true,
}

var (
	edgeTypeColorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	edgeTypeIDPattern    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

const edgeTypeColumns = `id::text, name, pricing_type, price::float8, alias, color, finish, system_key IS NOT NULL`

type edgeType struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	PricingType string  `json:"pricingType"`
	Price       float64 `json:"price"`
	Alias       string  `json:"alias"`
	Color       string  `json:"color"`
	Finish      string  `json:"finish"`
	// IsSystem marks the four types every tenant starts with.
	IsSystem bool `json:"isSystem"`
}

type edgeTypeInput struct {
	Name        string  `json:"name"`
	PricingType string  `json:"pricingType"`
	Price       float64 `json:"price"`
	Alias       string  `json:"alias"`
	Color       string  `json:"color"`
	Finish      string  `json:"finish"`
}

// clean trims the text fields and returns the first thing wrong with the
// input, or "" when it can be saved.
func (in *edgeTypeInput) clean() string {
	in.Name = strings.TrimSpace(in.Name)
	in.Alias = strings.TrimSpace(in.Alias)
	in.Color = strings.ToLower(strings.TrimSpace(in.Color))
	if in.PricingType == "" {
		in.PricingType = edgeTypePricing
	}
	if in.Finish == "" {
		in.Finish = "none"
	}
	switch {
	case in.Name == "":
		return "Enter a name for the edge."
	case utf8.RuneCountInString(in.Name) > maxEdgeTypeNameRunes:
		return "The name is too long."
	case utf8.RuneCountInString(in.Alias) > maxEdgeTypeAliasRunes:
		return "The alias is too long."
	case in.PricingType != edgeTypePricing:
		return "That pricing type is not available."
	case math.IsNaN(in.Price) || math.IsInf(in.Price, 0) || in.Price < 0 || in.Price > maxEdgeTypePrice:
		return "Enter a price of zero or more."
	case !edgeTypeColorPattern.MatchString(in.Color):
		return "Pick a colour."
	case !allowedEdgeFinishes[in.Finish]:
		return "That finish is not available."
	}
	return ""
}

func scanEdgeType(row pgx.Row) (edgeType, error) {
	var e edgeType
	err := row.Scan(&e.ID, &e.Name, &e.PricingType, &e.Price, &e.Alias, &e.Color, &e.Finish, &e.IsSystem)
	return e, err
}

// authorizeEdgeTypeWrite answers 401/403/500 itself and returns the tenant pool
// only for a caller allowed to change drawings (sales_order:update) — adding or
// repricing an edge type is drawing tooling, not a separate admin area.
func authorizeEdgeTypeWrite(w http.ResponseWriter, r *http.Request) (*pgxpool.Pool, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, false
	}
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceSalesOrder, authz.ActionUpdate)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceSalesOrder), "action", string(authz.ActionUpdate))
		fail(w, http.StatusForbidden, "You do not have permission to change edge types.")
		return nil, false
	}
	return pool, true
}

// GetEdgeTypes GET /api/tenant/settings/edge-types — the edge types the Edges
// tool offers, in the order the shop reads them. Read-only for any
// authenticated tenant user, like the other drawing lookups.
func GetEdgeTypes(w http.ResponseWriter, r *http.Request) {
	if _, err := middleware.GetUserFromContext(r.Context()); err != nil {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}
	rows, err := pool.Query(r.Context(),
		`SELECT `+edgeTypeColumns+` FROM drawing_edge_type ORDER BY sort_order, LOWER(name)`)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load edge types.")
		return
	}
	defer rows.Close()

	types := []edgeType{}
	for rows.Next() {
		e, err := scanEdgeType(rows)
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to load edge types.")
			return
		}
		types = append(types, e)
	}
	if err := rows.Err(); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load edge types.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "edgeTypes": types})
}

// CreateEdgeType POST /api/tenant/settings/edge-types
func CreateEdgeType(w http.ResponseWriter, r *http.Request) {
	pool, ok := authorizeEdgeTypeWrite(w, r)
	if !ok {
		return
	}
	var in edgeTypeInput
	if _, ok := readJSONBody(w, r, &in); !ok {
		return
	}
	if msg := in.clean(); msg != "" {
		fail(w, http.StatusBadRequest, msg)
		return
	}
	created, err := scanEdgeType(pool.QueryRow(r.Context(), `
		INSERT INTO drawing_edge_type (name, pricing_type, price, alias, color, finish, sort_order)
		VALUES ($1, $2, $3, $4, $5, $6, COALESCE((SELECT MAX(sort_order) FROM drawing_edge_type), 0) + 1)
		RETURNING `+edgeTypeColumns,
		in.Name, in.PricingType, in.Price, in.Alias, in.Color, in.Finish))
	if isUniqueViolation(err) {
		fail(w, http.StatusConflict, "An edge with that name already exists.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to save the edge type.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "edgeType": created})
}

// UpdateEdgeType PUT /api/tenant/settings/edge-types/{id}
func UpdateEdgeType(w http.ResponseWriter, r *http.Request) {
	pool, ok := authorizeEdgeTypeWrite(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !edgeTypeIDPattern.MatchString(id) {
		fail(w, http.StatusNotFound, "Edge type not found.")
		return
	}
	var in edgeTypeInput
	if _, ok := readJSONBody(w, r, &in); !ok {
		return
	}
	if msg := in.clean(); msg != "" {
		fail(w, http.StatusBadRequest, msg)
		return
	}
	updated, err := scanEdgeType(pool.QueryRow(r.Context(), `
		UPDATE drawing_edge_type
		SET name = $2, pricing_type = $3, price = $4, alias = $5, color = $6, finish = $7, updated_at = NOW()
		WHERE id = $1::uuid
		RETURNING `+edgeTypeColumns,
		id, in.Name, in.PricingType, in.Price, in.Alias, in.Color, in.Finish))
	if errors.Is(err, pgx.ErrNoRows) {
		fail(w, http.StatusNotFound, "Edge type not found.")
		return
	}
	if isUniqueViolation(err) {
		fail(w, http.StatusConflict, "An edge with that name already exists.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to save the edge type.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "edgeType": updated})
}

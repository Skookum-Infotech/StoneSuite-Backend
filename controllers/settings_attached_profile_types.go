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

// The attached-profile types the drawing editor's Profiles tool paints onto a
// piece's sides (drawing_attached_profile_type). Each carries a name, a price
// per linear unit, a short alias, the colour it is drawn in, the preset
// widths/heights it can be painted at, the angle it stands at relative to the
// parent piece in 3D, and whether two painted sides meeting at a corner get a
// mitered or square joint. A shop starts with four (Backsplash, Lamination,
// Miter Edge, Waterfall) and adds its own. Named "attached profile" to stay
// distinct from lkp_edge_code's "edge profile" and EdgeFinish's 3D
// cross-section shape.

const (
	maxProfileTypeNameRunes      = 64
	maxProfileTypeAliasRunes     = 8
	maxProfileMeasurementPresets = 12
	// maxProfileTypePrice matches the column's NUMERIC(12,2) with room to spare.
	maxProfileTypePrice = 9999999999.99
	// maxProfileMeasurementMm is a generous upper bound (about 79") so a typo
	// can't produce an unusable preset.
	maxProfileMeasurementMm = 2000.0
	profileTypePricing      = "linear"
)

// allowedProfileFinishes mirrors the frontend's ProfileFinish union
// (src/types/drawing.ts) and the table's CHECK constraint.
var allowedProfileFinishes = map[string]bool{"none": true, "mitered": true}

// allowedProfileAngles mirrors the frontend's ProfileAngle union and the
// table's CHECK constraint.
var allowedProfileAngles = map[int]bool{90: true, 180: true, 270: true}

var (
	profileTypeColorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	profileTypeIDPattern    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

const profileTypeColumns = `id::text, name, pricing_type, price::float8, alias, color, measurement_presets_mm::float8[], angle_deg, finish, system_key IS NOT NULL`

type attachedProfileType struct {
	ID                   string    `json:"id"`
	Name                 string    `json:"name"`
	PricingType          string    `json:"pricingType"`
	Price                float64   `json:"price"`
	Alias                string    `json:"alias"`
	Color                string    `json:"color"`
	MeasurementPresetsMm []float64 `json:"measurementPresetsMm"`
	Angle                int       `json:"angle"`
	Finish               string    `json:"finish"`
	// IsSystem marks the four types every tenant starts with.
	IsSystem bool `json:"isSystem"`
}

type attachedProfileTypeInput struct {
	Name                 string    `json:"name"`
	PricingType          string    `json:"pricingType"`
	Price                float64   `json:"price"`
	Alias                string    `json:"alias"`
	Color                string    `json:"color"`
	MeasurementPresetsMm []float64 `json:"measurementPresetsMm"`
	Angle                int       `json:"angle"`
	Finish               string    `json:"finish"`
}

// clean trims the text fields and returns the first thing wrong with the
// input, or "" when it can be saved.
func (in *attachedProfileTypeInput) clean() string {
	in.Name = strings.TrimSpace(in.Name)
	in.Alias = strings.TrimSpace(in.Alias)
	in.Color = strings.ToLower(strings.TrimSpace(in.Color))
	if in.PricingType == "" {
		in.PricingType = profileTypePricing
	}
	if in.Finish == "" {
		in.Finish = "none"
	}
	switch {
	case in.Name == "":
		return "Enter a name for the profile."
	case utf8.RuneCountInString(in.Name) > maxProfileTypeNameRunes:
		return "The name is too long."
	case utf8.RuneCountInString(in.Alias) > maxProfileTypeAliasRunes:
		return "The alias is too long."
	case in.PricingType != profileTypePricing:
		return "That pricing type is not available."
	case math.IsNaN(in.Price) || math.IsInf(in.Price, 0) || in.Price < 0 || in.Price > maxProfileTypePrice:
		return "Enter a price of zero or more."
	case !profileTypeColorPattern.MatchString(in.Color):
		return "Pick a colour."
	case !allowedProfileAngles[in.Angle]:
		return "Pick an angle."
	case !allowedProfileFinishes[in.Finish]:
		return "That finish is not available."
	case len(in.MeasurementPresetsMm) == 0:
		return "Add at least one measurement."
	case len(in.MeasurementPresetsMm) > maxProfileMeasurementPresets:
		return "Too many measurements."
	}
	for _, mm := range in.MeasurementPresetsMm {
		if math.IsNaN(mm) || math.IsInf(mm, 0) || mm <= 0 || mm > maxProfileMeasurementMm {
			return "Enter a measurement greater than zero."
		}
	}
	return ""
}

func scanAttachedProfileType(row pgx.Row) (attachedProfileType, error) {
	var p attachedProfileType
	err := row.Scan(&p.ID, &p.Name, &p.PricingType, &p.Price, &p.Alias, &p.Color,
		&p.MeasurementPresetsMm, &p.Angle, &p.Finish, &p.IsSystem)
	return p, err
}

// authorizeAttachedProfileTypeWrite answers 401/403/500 itself and returns the
// tenant pool only for a caller allowed to change drawings
// (sales_order:update) — adding or repricing a profile type is drawing
// tooling, not a separate admin area.
func authorizeAttachedProfileTypeWrite(w http.ResponseWriter, r *http.Request) (*pgxpool.Pool, bool) {
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
		fail(w, http.StatusForbidden, "You do not have permission to change profile types.")
		return nil, false
	}
	return pool, true
}

// GetAttachedProfileTypes GET /api/tenant/settings/attached-profile-types —
// the profile types the Profiles tool offers, in the order the shop reads
// them. Read-only for any authenticated tenant user, like the other drawing
// lookups.
func GetAttachedProfileTypes(w http.ResponseWriter, r *http.Request) {
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
		`SELECT `+profileTypeColumns+` FROM drawing_attached_profile_type ORDER BY sort_order, LOWER(name)`)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load profile types.")
		return
	}
	defer rows.Close()

	types := []attachedProfileType{}
	for rows.Next() {
		p, err := scanAttachedProfileType(rows)
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to load profile types.")
			return
		}
		types = append(types, p)
	}
	if err := rows.Err(); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load profile types.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "profileTypes": types})
}

// CreateAttachedProfileType POST /api/tenant/settings/attached-profile-types
func CreateAttachedProfileType(w http.ResponseWriter, r *http.Request) {
	pool, ok := authorizeAttachedProfileTypeWrite(w, r)
	if !ok {
		return
	}
	var in attachedProfileTypeInput
	if _, ok := readJSONBody(w, r, &in); !ok {
		return
	}
	if msg := in.clean(); msg != "" {
		fail(w, http.StatusBadRequest, msg)
		return
	}
	created, err := scanAttachedProfileType(pool.QueryRow(r.Context(), `
		INSERT INTO drawing_attached_profile_type
			(name, pricing_type, price, alias, color, measurement_presets_mm, angle_deg, finish, sort_order)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8,
			COALESCE((SELECT MAX(sort_order) FROM drawing_attached_profile_type), 0) + 1)
		RETURNING `+profileTypeColumns,
		in.Name, in.PricingType, in.Price, in.Alias, in.Color, in.MeasurementPresetsMm, in.Angle, in.Finish))
	if isUniqueViolation(err) {
		fail(w, http.StatusConflict, "A profile with that name already exists.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to save the profile type.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "profileType": created})
}

// UpdateAttachedProfileType PUT /api/tenant/settings/attached-profile-types/{id}
func UpdateAttachedProfileType(w http.ResponseWriter, r *http.Request) {
	pool, ok := authorizeAttachedProfileTypeWrite(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !profileTypeIDPattern.MatchString(id) {
		fail(w, http.StatusNotFound, "Profile type not found.")
		return
	}
	var in attachedProfileTypeInput
	if _, ok := readJSONBody(w, r, &in); !ok {
		return
	}
	if msg := in.clean(); msg != "" {
		fail(w, http.StatusBadRequest, msg)
		return
	}
	updated, err := scanAttachedProfileType(pool.QueryRow(r.Context(), `
		UPDATE drawing_attached_profile_type
		SET name = $2, pricing_type = $3, price = $4, alias = $5, color = $6,
			measurement_presets_mm = $7, angle_deg = $8, finish = $9, updated_at = NOW()
		WHERE id = $1::uuid
		RETURNING `+profileTypeColumns,
		id, in.Name, in.PricingType, in.Price, in.Alias, in.Color, in.MeasurementPresetsMm, in.Angle, in.Finish))
	if errors.Is(err, pgx.ErrNoRows) {
		fail(w, http.StatusNotFound, "Profile type not found.")
		return
	}
	if isUniqueViolation(err) {
		fail(w, http.StatusConflict, "A profile with that name already exists.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to save the profile type.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "profileType": updated})
}

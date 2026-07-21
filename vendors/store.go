// Package vendors: relational store. A vendor row (header only, no line
// items) with a status trail (vendor_history) — a sibling of the CRM
// `customer` table and `salesorder` package, not the generic v1 JSONB
// workflow engine (see the package doc comment in types.go).
package vendors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/query"
	"stonesuite-backend/workflow"
)

// ErrNotFound is returned when a vendor uuid matches nothing live.
var ErrNotFound = errors.New("vendor not found")

// ClientError signals a client-caused failure (validation, bad input) that a
// controller maps to HTTP 400, mirroring salesorder.ClientError.
type ClientError struct{ Msg string }

func (e ClientError) Error() string { return e.Msg }

// IsClientError reports whether err is a ClientError.
func IsClientError(err error) bool {
	var ce ClientError
	return errors.As(err, &ce)
}

// isForeignKeyViolation reports whether err is a PostgreSQL FK-constraint
// violation (code 23503) — an invalid caller-supplied reference id
// (nationality/country).
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// vendorRecordTypeCode is the lkp_record_type code for Vendor.
const vendorRecordTypeCode = "VNDR"

// defaultStatusCode is the status every new vendor starts at — a vendor
// directory entry has no draft state, unlike Sales Order.
const defaultStatusCode = "ACT_"

// ----- header select + scan -------------------------------------------------

const vendorSelect = `
	SELECT v.vendor_uuid, COALESCE(v.vendor_number,''),
	       rs.record_status_name, rs.record_status_code,
	       v.vendor_type,
	       COALESCE(ou.id::text,''), v.vendor_owner_id,
	       v.vendor_email, v.vendor_physical_address, v.vendor_fax_number,
	       v.vendor_global_location_number, v.vendor_isic_v4_code,
	       v.vendor_associated_brands, v.vendor_awards_won, v.vendor_contact_point,
	       v.vendor_funder, v.vendor_offer_catalog_url, v.vendor_point_of_sale_locations,
	       v.vendor_honorific_prefix, v.vendor_given_name, v.vendor_additional_name,
	       v.vendor_family_name, v.vendor_honorific_suffix, v.vendor_job_title,
	       v.vendor_gender, v.vendor_nationality_country_id, v.vendor_height, v.vendor_net_worth,
	       v.vendor_legal_name, v.vendor_registration_info, v.vendor_duns_number,
	       COALESCE(to_char(v.vendor_founding_date,'YYYY-MM-DD'),''), v.vendor_founding_location,
	       COALESCE(to_char(v.vendor_dissolution_date,'YYYY-MM-DD'),''), v.vendor_department,
	       v.vendor_accepted_payment_methods, v.vendor_compliance_policies,
	       v.vendor_created_at, v.vendor_updated_at
	FROM vendor v
	JOIN lkp_record_status rs ON rs.record_status_id = v.vendor_status
	LEFT JOIN employee oe ON oe.employee_id = v.vendor_owner_id
	LEFT JOIN users ou ON ou.id = oe.employee_user_id`

func scanVendor(row pgx.Row) (*Vendor, error) {
	var v Vendor
	var brandsRaw, contactRaw, methodsRaw, policiesRaw []byte
	if err := row.Scan(
		&v.ID, &v.Number,
		&v.Status, &v.StatusCode,
		&v.VendorType,
		&v.OwnerUserID, &v.OwnerEmployeeID,
		&v.Email, &v.PhysicalAddress, &v.FaxNumber,
		&v.GlobalLocationNumber, &v.ISICV4Code,
		&brandsRaw, &v.AwardsWon, &contactRaw,
		&v.Funder, &v.HasOfferCatalogURL, &v.PointOfSaleLocations,
		&v.HonorificPrefix, &v.GivenName, &v.AdditionalName,
		&v.FamilyName, &v.HonorificSuffix, &v.JobTitle,
		&v.Gender, &v.NationalityCountryID, &v.Height, &v.NetWorth,
		&v.LegalName, &v.RegistrationInfo, &v.DUNSNumber,
		&v.FoundingDate, &v.FoundingLocation,
		&v.DissolutionDate, &v.Department,
		&methodsRaw, &policiesRaw,
		&v.CreatedAt, &v.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if len(brandsRaw) > 0 {
		_ = json.Unmarshal(brandsRaw, &v.AssociatedBrands)
	}
	if len(contactRaw) > 0 {
		var cp ContactPoint
		if err := json.Unmarshal(contactRaw, &cp); err == nil && cp != (ContactPoint{}) {
			v.ContactPoint = &cp
		}
	}
	if len(methodsRaw) > 0 {
		_ = json.Unmarshal(methodsRaw, &v.AcceptedPaymentMethods)
	}
	if len(policiesRaw) > 0 {
		var cpz CompliancePolicies
		if err := json.Unmarshal(policiesRaw, &cpz); err == nil && cpz != (CompliancePolicies{}) {
			v.CompliancePolicies = &cpz
		}
	}
	v.DisplayName = displayName(v.VendorType, v.HonorificPrefix, v.GivenName, v.FamilyName, v.LegalName)
	return &v, nil
}

// ----- reads ------------------------------------------------------------

// Get loads a single live vendor by its external uuid.
func Get(ctx context.Context, pool *pgxpool.Pool, uuid string) (*Vendor, error) {
	v, err := scanVendor(pool.QueryRow(ctx, vendorSelect+`
		WHERE v.vendor_uuid = $1 AND v.vendor_deleted_at IS NULL`, uuid))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get vendor: %w", err)
	}
	return v, nil
}

// ----- lookups used by Create/Update/Transition ------------------------------

// recordTypeIDByCode resolves a lkp_record_type code to its internal id.
func recordTypeIDByCode(ctx context.Context, q workflow.Querier, code string) (int, error) {
	var id int
	err := q.QueryRow(ctx,
		`SELECT record_type_id FROM lkp_record_type WHERE record_type_code = $1`, code).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("record type %q: %w", code, err)
	}
	return id, nil
}

// statusIDByCode resolves a lkp_record_status code (scoped to a record type)
// to its internal id.
func statusIDByCode(ctx context.Context, q workflow.Querier, recordTypeID int, code string) (int, error) {
	var id int
	err := q.QueryRow(ctx, `
		SELECT record_status_id FROM lkp_record_status
		WHERE record_status_record_type = $1 AND record_status_code = $2`, recordTypeID, code).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("status %q: %w", code, err)
	}
	return id, nil
}

// nullableInt converts a non-positive id to SQL NULL.
func nullableInt(v int) any {
	if v <= 0 {
		return nil
	}
	return v
}

// nullableDate returns the given "yyyy-mm-dd" date string, or SQL NULL when blank.
func nullableDate(d string) any {
	if d == "" {
		return nil
	}
	return d
}

// colVal pairs a column name with its bind value (and an optional type cast
// suffix) so an INSERT/UPDATE's column list and argument list are always
// built from the same slice (mirrors salesorder.colVal).
type colVal struct {
	col  string
	val  any
	cast string
}

// buildInsert renders an INSERT ... VALUES (...) RETURNING statement from
// column/value pairs, numbering placeholders by position so cols and args
// can never drift apart.
func buildInsert(table string, cv []colVal, returning string) (string, []any) {
	cols := make([]string, len(cv))
	phs := make([]string, len(cv))
	args := make([]any, len(cv))
	for i, c := range cv {
		cols[i] = c.col
		args[i] = c.val
		phs[i] = fmt.Sprintf("$%d%s", i+1, c.cast)
	}
	sql := "INSERT INTO " + table + " (" + strings.Join(cols, ", ") + ") VALUES (" + strings.Join(phs, ", ") + ")"
	if returning != "" {
		sql += " RETURNING " + returning
	}
	return sql, args
}

// buildUpdateSet renders an "UPDATE ... SET col=$n, ... WHERE <where>"
// statement. leadingArgs are bound first (the WHERE clause's own
// placeholders); cv's placeholders continue after them.
func buildUpdateSet(table string, leadingArgs []any, cv []colVal, extraSets []string, where string) (string, []any) {
	sets := make([]string, 0, len(cv)+len(extraSets))
	args := make([]any, 0, len(leadingArgs)+len(cv))
	args = append(args, leadingArgs...)
	for _, c := range cv {
		args = append(args, c.val)
		sets = append(sets, fmt.Sprintf("%s = $%d%s", c.col, len(args), c.cast))
	}
	sets = append(sets, extraSets...)
	sql := "UPDATE " + table + " SET " + strings.Join(sets, ", ") + " WHERE " + where
	return sql, args
}

// vendorColVals returns the shared (column, value) pairs written by both
// Create and Update, in schema column order.
func vendorColVals(in vendorFields) []colVal {
	brands := in.AssociatedBrands
	if brands == nil {
		brands = []string{}
	}
	methods := in.AcceptedPaymentMethods
	if methods == nil {
		methods = []string{}
	}
	contactPoint := in.ContactPoint
	if contactPoint == nil {
		contactPoint = &ContactPoint{}
	}
	compliance := in.CompliancePolicies
	if compliance == nil {
		compliance = &CompliancePolicies{}
	}
	return []colVal{
		{"vendor_email", in.Email, ""},
		{"vendor_physical_address", in.PhysicalAddress, ""},
		{"vendor_fax_number", in.FaxNumber, ""},
		{"vendor_global_location_number", in.GlobalLocationNumber, ""},
		{"vendor_isic_v4_code", in.ISICV4Code, ""},
		{"vendor_associated_brands", brands, ""},
		{"vendor_awards_won", in.AwardsWon, ""},
		{"vendor_contact_point", contactPoint, ""},
		{"vendor_funder", in.Funder, ""},
		{"vendor_offer_catalog_url", in.HasOfferCatalogURL, ""},
		{"vendor_point_of_sale_locations", in.PointOfSaleLocations, ""},
		{"vendor_honorific_prefix", in.HonorificPrefix, ""},
		{"vendor_given_name", in.GivenName, ""},
		{"vendor_additional_name", in.AdditionalName, ""},
		{"vendor_family_name", in.FamilyName, ""},
		{"vendor_honorific_suffix", in.HonorificSuffix, ""},
		{"vendor_job_title", in.JobTitle, ""},
		{"vendor_gender", in.Gender, ""},
		{"vendor_nationality_country_id", in.NationalityCountryID, ""},
		{"vendor_height", in.Height, ""},
		{"vendor_net_worth", in.NetWorth, ""},
		{"vendor_legal_name", in.LegalName, ""},
		{"vendor_registration_info", in.RegistrationInfo, ""},
		{"vendor_duns_number", in.DUNSNumber, ""},
		{"vendor_founding_date", nullableDate(in.FoundingDate), "::date"},
		{"vendor_founding_location", in.FoundingLocation, ""},
		{"vendor_dissolution_date", nullableDate(in.DissolutionDate), "::date"},
		{"vendor_department", in.Department, ""},
		{"vendor_accepted_payment_methods", methods, ""},
		{"vendor_compliance_policies", compliance, ""},
	}
}

// validateVendorType checks the type discriminant and its required field
// (mirrors the frontend's validateVendorForm — enforced again server-side
// since the client cannot be trusted). Also backed by the vendor table's
// chk_vendor_person_names / chk_vendor_org_legal_name CHECK constraints.
func validateVendorType(vendorType string, in vendorFields) error {
	switch vendorType {
	case "Person":
		if strings.TrimSpace(in.GivenName) == "" || strings.TrimSpace(in.FamilyName) == "" {
			return ClientError{Msg: "First name and last name are required for a Person vendor."}
		}
	case "Organization":
		if strings.TrimSpace(in.LegalName) == "" {
			return ClientError{Msg: "Legal business name is required for an Organization vendor."}
		}
	default:
		return ClientError{Msg: `vendorType must be "Person" or "Organization".`}
	}
	return nil
}

// writeHistory appends a vendor_history row (best-effort within the caller's
// still-open transaction, mirrors salesorder.writeHistory).
func writeHistory(ctx context.Context, tx pgx.Tx, vendorInternalID int, action string, fromStatusID, toStatusID *int, actorEmployeeID int) {
	_, _ = tx.Exec(ctx, `
		INSERT INTO vendor_history (vendor_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1,$2,$3,$4,$5)`,
		vendorInternalID, fromStatusID, toStatusID, action, nullableInt(actorEmployeeID))
}

// ----- Create -----------------------------------------------------------

// Create inserts a new vendor inside one transaction: validates the
// type-specific required field, assigns the vendor number post-insert, and
// starts the vendor at ACT_ (Active). The creating employee becomes the
// owner (no owner-override field on the create contract).
func Create(ctx context.Context, pool *pgxpool.Pool, in CreateVendorInput, actorEmployeeID int) (*Vendor, error) {
	if err := validateVendorType(in.VendorType, in.vendorFields); err != nil {
		return nil, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create vendor: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	recordTypeID, err := recordTypeIDByCode(ctx, tx, vendorRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve VNDR record type: %w", err)
	}
	statusID, err := statusIDByCode(ctx, tx, recordTypeID, defaultStatusCode)
	if err != nil {
		return nil, fmt.Errorf("resolve ACT_ status: %w", err)
	}

	cv := append([]colVal{
		{"record_type", recordTypeID, ""},
		{"vendor_status", statusID, ""},
		{"vendor_type", in.VendorType, ""},
		{"vendor_owner_id", nullableInt(actorEmployeeID), ""},
	}, vendorColVals(in.vendorFields)...)
	cv = append(cv, colVal{"vendor_created_by", nullableInt(actorEmployeeID), ""})

	insertSQL, insertArgs := buildInsert("vendor", cv, "vendor_id, vendor_uuid")
	var internalID int
	var newUUID string
	if err := tx.QueryRow(ctx, insertSQL, insertArgs...).Scan(&internalID, &newUUID); err != nil {
		if isForeignKeyViolation(err) {
			return nil, ClientError{Msg: "The referenced nationality/country does not exist."}
		}
		return nil, fmt.Errorf("insert vendor: %w", err)
	}

	number := FormatNumber(int64(internalID))
	if _, err := tx.Exec(ctx,
		`UPDATE vendor SET vendor_number = $1 WHERE vendor_id = $2`, number, internalID); err != nil {
		return nil, fmt.Errorf("set vendor number: %w", err)
	}

	writeHistory(ctx, tx, internalID, "create", nil, &statusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create vendor: %w", err)
	}
	return Get(ctx, pool, newUUID)
}

// ----- Update -----------------------------------------------------------

// Update replaces a live vendor's editable fields inside one transaction.
// VendorType and status are not editable here (type is fixed at creation;
// status changes go through Transition).
func Update(ctx context.Context, pool *pgxpool.Pool, uuid string, in UpdateVendorInput, actorEmployeeID int) (*Vendor, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin update vendor: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID int
	var vendorType string
	err = tx.QueryRow(ctx, `
		SELECT vendor_id, vendor_type FROM vendor
		WHERE vendor_uuid = $1 AND vendor_deleted_at IS NULL`, uuid,
	).Scan(&internalID, &vendorType)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load vendor for update: %w", err)
	}

	if err := validateVendorType(vendorType, in.vendorFields); err != nil {
		return nil, err
	}

	cv := vendorColVals(in.vendorFields)
	cv = append(cv, colVal{"vendor_updated_by", nullableInt(actorEmployeeID), ""})

	updateSQL, updateArgs := buildUpdateSet("vendor", []any{uuid}, cv,
		[]string{"vendor_updated_at = NOW()", "vendor_record_version = vendor_record_version + 1"},
		"vendor_uuid = $1 AND vendor_deleted_at IS NULL")
	if _, err := tx.Exec(ctx, updateSQL, updateArgs...); err != nil {
		if isForeignKeyViolation(err) {
			return nil, ClientError{Msg: "The referenced nationality/country does not exist."}
		}
		return nil, fmt.Errorf("update vendor: %w", err)
	}

	writeHistory(ctx, tx, internalID, "update", nil, nil, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update vendor: %w", err)
	}
	return Get(ctx, pool, uuid)
}

// ----- SoftDelete ---------------------------------------------------------

// SoftDelete marks a live vendor deleted.
func SoftDelete(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) error {
	tag, err := pool.Exec(ctx, `
		UPDATE vendor
		SET vendor_deleted_at = NOW(), vendor_deleted_by = $2
		WHERE vendor_uuid = $1 AND vendor_deleted_at IS NULL`,
		uuid, nullableInt(actorEmployeeID))
	if err != nil {
		return fmt.Errorf("delete vendor: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ----- Transition ---------------------------------------------------------

// Transition moves a live vendor to toStatusCode, validating the move
// against the static transition map, row-locking the vendor to serialize
// concurrent transitions, and writing a history row.
func Transition(ctx context.Context, pool *pgxpool.Pool, uuid, toStatusCode string, actorEmployeeID int) (*Vendor, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID int
	var curStatusCode string
	err = tx.QueryRow(ctx, `
		SELECT v.vendor_id, v.vendor_status, rs.record_status_code
		FROM vendor v JOIN lkp_record_status rs ON rs.record_status_id = v.vendor_status
		WHERE v.vendor_uuid = $1 AND v.vendor_deleted_at IS NULL
		FOR UPDATE OF v`, uuid,
	).Scan(&internalID, &curStatusID, &curStatusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load vendor for transition: %w", err)
	}
	if err := ValidateTransition(curStatusCode, toStatusCode); err != nil {
		return nil, err
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, vendorRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve VNDR record type: %w", err)
	}
	toStatusID, err := statusIDByCode(ctx, tx, recordTypeID, toStatusCode)
	if err != nil {
		return nil, ClientError{Msg: "Unknown target status."}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE vendor SET
			vendor_status = $2, vendor_updated_at = NOW(),
			vendor_updated_by = $3, vendor_record_version = vendor_record_version + 1
		WHERE vendor_id = $1`, internalID, toStatusID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("transition vendor: %w", err)
	}

	writeHistory(ctx, tx, internalID, "transition", &curStatusID, &toStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transition: %w", err)
	}
	return Get(ctx, pool, uuid)
}

// ----- Search --------------------------------------------------------------

// Search lists vendors under the caller's RBAC scope with filter/sort/global
// search + keyset pagination, mirroring salesorder.Search.
func Search(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID string, req query.Request) (Page, error) {
	where := []string{"v.vendor_deleted_at IS NULL"}
	var args []any
	nextIdx := 1
	if scope != string(authz.ScopeAll) {
		empID, found := employeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return Page{}, nil
		}
		where = append(where, fmt.Sprintf("v.vendor_owner_id = $%d", nextIdx))
		args = append(args, empID)
		nextIdx++
	}

	built, err := query.Build(req, resolver{}, nextIdx)
	if err != nil {
		return Page{}, err
	}
	if built.Where != "" {
		where = append(where, built.Where)
	}
	if built.Keyset != "" {
		where = append(where, built.Keyset)
	}
	args = append(args, built.Args...)

	q := vendorSelect + " WHERE " + strings.Join(where, " AND ") +
		" ORDER BY " + built.OrderBy + " LIMIT " + strconv.Itoa(built.EffLimit+1)

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return Page{}, fmt.Errorf("search vendors: %w", err)
	}
	defer rows.Close()
	out := []Vendor{}
	for rows.Next() {
		v, err := scanVendor(rows)
		if err != nil {
			return Page{}, err
		}
		out = append(out, *v)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("search vendors: %w", err)
	}

	page := Page{Records: out}
	if len(out) > built.EffLimit {
		page.HasMore = true
		last := out[built.EffLimit-1]
		page.Records = out[:built.EffLimit]
		page.NextCursor = query.NextCursor(last.ID, built.Sort, sortValue(last, built.Sort.Field))
	}
	return page, nil
}

// sortValue reads the effective sort field's value from a vendor to mint the
// next cursor.
func sortValue(v Vendor, field string) any {
	switch field {
	case "updated_at":
		return v.UpdatedAt
	case "legal_name":
		return v.LegalName
	case "family_name":
		return v.FamilyName
	case "document_number", "record_number":
		return v.Number
	default: // created_at (default)
		return v.CreatedAt
	}
}

// employeeIDByIdentity resolves a control-plane identity to a tenant
// employee_id, mirroring salesorder.employeeIDByIdentity.
func employeeIDByIdentity(ctx context.Context, pool *pgxpool.Pool, identityID string) (int, bool) {
	if identityID == "" {
		return 0, false
	}
	var id int
	err := pool.QueryRow(ctx, `
		SELECT e.employee_id FROM employee e
		JOIN users u ON u.id = e.employee_user_id
		WHERE u.identity_id = $1 AND e.employee_deleted_at IS NULL`, identityID).Scan(&id)
	if err != nil {
		return 0, false
	}
	return id, true
}

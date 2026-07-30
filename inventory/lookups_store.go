package inventory

// lookups_store.go — writes for the user-extensible vocabularies.
//
// lkp_color ships deliberately empty (colour names are vendor catalogue names,
// and a guessed seed would collide with the tenant's real import), so without
// these endpoints a tenant could never record the colour of anything.

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// colorHex mirrors chk_color_hex, so a bad swatch is a 400 rather than a 500
// from the constraint.
var colorHex = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// sortedKeys gives the optional columns a stable order.
//
// Iterating the map directly would emit a different SQL string on each call for
// the same logical write, so pgx's prepared-statement cache would miss every
// time and the server would re-plan the statement on every request.
func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// LookupInput is the write shape for a vocabulary row.
type LookupInput struct {
	Name     string `json:"name"`
	Code     string `json:"code"`
	IsActive bool   `json:"isActive"`
	// Hex and MaterialID apply to colours only; AppliesTo and Direction to
	// reasons; IsPorous to materials. Ignored for vocabularies that lack them.
	Hex        string `json:"hex"`
	MaterialID *int   `json:"materialId,omitempty"`
	AppliesTo  string `json:"appliesTo"`
	Direction  string `json:"direction"`
	IsPorous   *bool  `json:"isPorous,omitempty"`
}

func (t lookupTable) requireWritable(kind string) error {
	if !t.writable {
		return ClientError{Msg: fmt.Sprintf("The %s vocabulary is read-only.", kind)}
	}
	return nil
}

func validateLookupInput(in *LookupInput) error {
	if strings.TrimSpace(in.Name) == "" {
		return ClientError{Msg: "Name is required."}
	}
	if strings.TrimSpace(in.Code) == "" {
		return ClientError{Msg: "Code is required."}
	}
	if in.Hex != "" && !colorHex.MatchString(in.Hex) {
		return ClientError{Msg: "Colour must be a hex value like #A1B2C3."}
	}
	if in.AppliesTo != "" && !validReasonApplies[in.AppliesTo] {
		return ClientError{Msg: "Reason must apply to one of: adjustment, transfer, count, scrap, any."}
	}
	if in.Direction != "" && !validReasonDirection[in.Direction] {
		return ClientError{Msg: "Reason direction must be one of: increase, decrease, both."}
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Code = strings.TrimSpace(in.Code)
	return nil
}

var (
	validReasonApplies   = map[string]bool{"adjustment": true, "transfer": true, "count": true, "scrap": true, "any": true}
	validReasonDirection = map[string]bool{"increase": true, "decrease": true, "both": true}
)

// CreateLookup inserts a row into a writable vocabulary.
func CreateLookup(ctx context.Context, pool *pgxpool.Pool, kind string, in LookupInput, actorEmployeeID int) (*LookupItem, error) {
	t, err := LookupKind(kind)
	if err != nil {
		return nil, err
	}
	if err := t.requireWritable(kind); err != nil {
		return nil, err
	}
	if err := validateLookupInput(&in); err != nil {
		return nil, err
	}

	cols := []string{t.nameCol, t.codeCol, t.activeCol}
	vals := []any{in.Name, in.Code, in.IsActive}
	optional := t.optionalWriteValues(in)
	for _, col := range sortedKeys(optional) {
		cols = append(cols, col)
		vals = append(vals, optional[col])
	}
	cols = append(cols, t.createdByCol())
	vals = append(vals, nullableInt(actorEmployeeID))

	ph := make([]string, len(vals))
	for i := range vals {
		ph[i] = fmt.Sprintf("$%d", i+1)
	}
	q := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) RETURNING %s",
		t.table, strings.Join(cols, ","), strings.Join(ph, ","), t.idCol)

	var id int
	if err := pool.QueryRow(ctx, q, vals...).Scan(&id); err != nil {
		if isUniqueViolation(err) {
			return nil, ClientError{Msg: "An entry with this code already exists."}
		}
		if isFKViolation(err) {
			return nil, ClientError{Msg: "Unknown related record."}
		}
		if isCheckViolation(err) {
			return nil, ClientError{Msg: "One or more values failed validation."}
		}
		return nil, fmt.Errorf("insert %s: %w", kind, err)
	}
	return getLookupByID(ctx, pool, t, kind, id)
}

// UpdateLookup edits a row in a writable vocabulary. System rows are protected:
// they are referenced by seeded logic and renaming a code would break it.
func UpdateLookup(ctx context.Context, pool *pgxpool.Pool, kind string, id int, in LookupInput, actorEmployeeID int) error {
	t, err := LookupKind(kind)
	if err != nil {
		return err
	}
	if err := t.requireWritable(kind); err != nil {
		return err
	}
	if err := validateLookupInput(&in); err != nil {
		return err
	}

	sets := []string{
		fmt.Sprintf("%s = $2", t.nameCol),
		fmt.Sprintf("%s = $3", t.codeCol),
		fmt.Sprintf("%s = $4", t.activeCol),
	}
	vals := []any{id, in.Name, in.Code, in.IsActive}
	optional := t.optionalWriteValues(in)
	for _, col := range sortedKeys(optional) {
		vals = append(vals, optional[col])
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(vals)))
	}

	q := fmt.Sprintf(`UPDATE %s SET %s WHERE %s = $1 AND %s IS NULL AND %s = FALSE`,
		t.table, strings.Join(sets, ", "), t.idCol, t.deletedAt, t.systemCol)
	tag, err := pool.Exec(ctx, q, vals...)
	if err != nil {
		if isUniqueViolation(err) {
			return ClientError{Msg: "An entry with this code already exists."}
		}
		return fmt.Errorf("update %s: %w", kind, err)
	}
	if tag.RowsAffected() == 0 {
		// Either it does not exist or it is a system row. Reporting "not found"
		// for both keeps the two indistinguishable, which is the same reasoning
		// as returning 404 rather than 403 on a scope denial.
		return ErrNotFound
	}
	return nil
}

// DeleteLookup soft-deletes a vocabulary row. System rows cannot be deleted.
func DeleteLookup(ctx context.Context, pool *pgxpool.Pool, kind string, id int, actorEmployeeID int) error {
	t, err := LookupKind(kind)
	if err != nil {
		return err
	}
	if err := t.requireWritable(kind); err != nil {
		return err
	}
	q := fmt.Sprintf(`UPDATE %s SET %s = NOW(), %s = $2 WHERE %s = $1 AND %s IS NULL AND %s = FALSE`,
		t.table, t.deletedAt, t.deletedByCol(), t.idCol, t.deletedAt, t.systemCol)
	tag, err := pool.Exec(ctx, q, id, nullableInt(actorEmployeeID))
	if err != nil {
		if isFKViolation(err) {
			return ClientError{Msg: "This entry is still referenced and cannot be deleted."}
		}
		return fmt.Errorf("delete %s: %w", kind, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func getLookupByID(ctx context.Context, pool *pgxpool.Pool, t lookupTable, kind string, id int) (*LookupItem, error) {
	var (
		it    LookupItem
		extra map[string]any
	)
	q := t.selectSQL() + " WHERE " + t.idCol + " = $1"
	if err := pool.QueryRow(ctx, q, id).Scan(
		&it.ID, &it.Name, &it.Code, &it.IsActive, &it.IsSystem, &extra); err != nil {
		return nil, fmt.Errorf("get %s: %w", kind, err)
	}
	it.Extra = extra
	return &it, nil
}

// optionalWriteValues returns the vocabulary-specific columns to write, keyed
// by column name. Only columns declared in the registry are ever produced, so
// a caller cannot reach a column the vocabulary does not have.
func (t lookupTable) optionalWriteValues(in LookupInput) map[string]any {
	out := map[string]any{}
	for key, col := range t.extraCols {
		switch key {
		case "hex":
			out[col] = in.Hex
		case "materialId":
			out[col] = nullableIntPtr(in.MaterialID)
		case "appliesTo":
			if in.AppliesTo != "" {
				out[col] = in.AppliesTo
			}
		case "direction":
			if in.Direction != "" {
				out[col] = in.Direction
			}
		case "isPorous":
			if in.IsPorous != nil {
				out[col] = *in.IsPorous
			}
		}
	}
	return out
}

// createdByCol / deletedByCol derive the audit column names from the table's
// own prefix, which every lkp_* table in this schema follows.
func (t lookupTable) createdByCol() string {
	return strings.TrimSuffix(t.deletedAt, "deleted_at") + "created_by"
}
func (t lookupTable) deletedByCol() string {
	return strings.TrimSuffix(t.deletedAt, "deleted_at") + "deleted_by"
}

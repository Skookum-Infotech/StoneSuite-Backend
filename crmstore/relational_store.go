package crmstore

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// relationalStore is the DesignV2 implementation: a single customer table
// keyed by record_type (LEAD/PROS/CUST), with statuses and transitions driven
// by lkp_crm_status. Lead/Prospect/Customer are views of one table; advancing a
// record to a status of a later type relocates it to that listing (forward-only).
// See ADR-002 — this supersedes the earlier crm_record master table.
type relationalStore struct{}

var _ Store = (*relationalStore)(nil)

// CRM stage ranks: a record may only advance to an equal-or-higher rank.
var crmKeyToCode = map[string]string{"lead": "LEAD", "prospect": "PROS", "customer": "CUST"}
var crmCodeToKey = map[string]string{"LEAD": "lead", "PROS": "prospect", "CUST": "customer"}
var crmCodeRank = map[string]int{"LEAD": 1, "PROS": 2, "CUST": 3}

// CRMWorkflowKeys returns the CRM workflow keys ("lead", "prospect",
// "customer") in stage order, for callers that need to enumerate every CRM
// record type regardless of design version (e.g. the RAG index reconciliation
// sweep and reindex trigger).
func CRMWorkflowKeys() []string { return []string{"lead", "prospect", "customer"} }

const closedWonCode = "CCLW" // Customer Closed Won — triggers approval

// fkind is the storage kind of a customer column, controlling how it is scanned
// from and written to the database and represented in the CoreFields map.
type fkind int

const (
	kStr  fkind = iota // VARCHAR/TEXT — round-tripped as a string
	kFK                // nullable INTEGER FK — CoreFields holds the id as a string
	kInt               // nullable plain INTEGER — CoreFields holds it as a string
	kBool              // NOT NULL BOOLEAN
	kDec               // nullable DECIMAL — CoreFields holds it as a string
	kDate              // nullable DATE — CoreFields holds 'YYYY-MM-DD'
)

// cfield maps a CoreFields key to a customer column. The registry is the single
// source of truth for the SELECT/INSERT/UPDATE column lists, so adding a field
// is a one-line change. always=true keeps a string field present in CoreFields
// even when empty (the six built-in text fields the frontend always expects).
type cfield struct {
	core   string
	col    string
	kind   fkind
	always bool
}

// customerFields is the full customer column registry (ADR-002). The first six
// (always) are the built-in contact fields; the rest map the workbook's rich
// fields. record_type, statuses, owner, approval, lineage and audit columns are
// handled explicitly (not in this registry).
var customerFields = []cfield{
	// core key == col: frontend always sends the DB column name as the field key.
	{"customer_name", "customer_name", kStr, true},
	{"customer_authorized_person_fname", "customer_authorized_person_fname", kStr, true},
	{"customer_authorized_person_lname", "customer_authorized_person_lname", kStr, true},
	{"customer_contact_email", "customer_contact_email", kStr, true},
	{"customer_primary_phonenum", "customer_primary_phonenum", kStr, true},
	{"customer_addr_line1", "customer_addr_line1", kStr, true},

	{"customer_type", "customer_type", kFK, false},
	{"customer_ar_status", "customer_ar_status", kFK, false},
	{"customer_payment_terms", "customer_payment_terms", kFK, false},
	{"customer_currency", "customer_currency", kFK, false},
	{"customer_addr_country", "customer_addr_country", kFK, false},
	{"customer_addr_state", "customer_addr_state", kFK, false},
	{"customer_lead_source", "customer_lead_source", kFK, false},
	{"customer_preferred_contact_method", "customer_preferred_contact_method", kFK, false},

	{"customer_dba_name", "customer_dba_name", kStr, false},
	{"customer_tax_id", "customer_tax_id", kStr, false},
	{"customer_is_child", "customer_is_child", kBool, false},
	{"customer_parent_company", "customer_parent_company", kFK, false},
	{"customer_accounts_email", "customer_accounts_email", kStr, false},
	{"customer_addl_email", "customer_addl_email", kStr, false},
	{"customer_alt_phonenum", "customer_alt_phonenum", kStr, false},
	{"customer_faxnum", "customer_faxnum", kStr, false},
	{"customer_cmpny_website", "customer_cmpny_website", kStr, false},
	{"customer_addr_line2", "customer_addr_line2", kStr, false},
	{"customer_addr_suitenum", "customer_addr_suitenum", kStr, false},
	{"customer_addr_city", "customer_addr_city", kStr, false},
	{"customer_addr_zip", "customer_addr_zip", kStr, false},

	{"customer_is_bill_as_primary", "customer_is_bill_as_primary", kBool, false},
	{"customer_bill_addr_line1", "customer_bill_addr_line1", kStr, false},
	{"customer_bill_addr_line2", "customer_bill_addr_line2", kStr, false},
	{"customer_bill_addr_suitenum", "customer_bill_addr_suitenum", kStr, false},
	{"customer_bill_addr_city", "customer_bill_addr_city", kStr, false},
	{"customer_bill_addr_state", "customer_bill_addr_state", kFK, false},
	{"customer_bill_addr_zip", "customer_bill_addr_zip", kStr, false},
	{"customer_bill_addr_country", "customer_bill_addr_country", kFK, false},

	{"customer_is_ship_as_primary", "customer_is_ship_as_primary", kBool, false},
	{"customer_ship_addr_line1", "customer_ship_addr_line1", kStr, false},
	{"customer_ship_addr_line2", "customer_ship_addr_line2", kStr, false},
	{"customer_ship_addr_suitenum", "customer_ship_addr_suitenum", kStr, false},
	{"customer_ship_addr_city", "customer_ship_addr_city", kStr, false},
	{"customer_ship_addr_state", "customer_ship_addr_state", kFK, false},
	{"customer_ship_addr_zip", "customer_ship_addr_zip", kStr, false},
	{"customer_ship_addr_country", "customer_ship_addr_country", kFK, false},

	{"customer_lead_score", "customer_lead_score", kInt, false},
	{"customer_expected_close_date", "customer_expected_close_date", kDate, false},
	{"customer_expected_deal_value", "customer_expected_deal_value", kDec, false},
	{"customer_last_contacted_date", "customer_last_contacted_date", kDate, false},
	{"customer_do_not_contact", "customer_do_not_contact", kBool, false},
	{"customer_internal_notes", "customer_internal_notes", kStr, false},

	{"customer_sales_rep", "customer_sales_rep_user_id", kFK, false},
	{"customer_price_level", "customer_price_level", kFK, false},
	{"customer_is_tax_exempt", "customer_is_tax_exempt", kBool, false},
	{"customer_tax_exempt_reason", "customer_tax_exempt_reason", kStr, false},
	{"customer_tax_exempt_cert_num", "customer_tax_exempt_cert_num", kStr, false},
	{"customer_tax_exempt_cert_file_id", "customer_tax_exempt_cert_file_id", kStr, false},
	{"customer_tax_exempt_expiry_date", "customer_tax_exempt_expiry_date", kDate, false},
	{"customer_sales_tax_percent", "customer_sales_tax_percent", kDec, false},
	{"customer_credit_limit", "customer_credit_limit", kDec, false},
	{"customer_is_credit_lock", "customer_is_credit_lock", kBool, false},
	{"customer_credit_lock_reason", "customer_credit_lock_reason", kStr, false},

	{"customer_total_balance", "customer_total_balance", kDec, false},
	{"customer_deposit_balance", "customer_deposit_balance", kDec, false},
	{"customer_overdue_balance", "customer_overdue_balance", kDec, false},
	{"customer_days_overdue", "customer_days_overdue", kInt, false},
}

// leadProspectRequired lists the CoreFields keys the workbook marks mandatory
// for LEAD/PROS records. Enforcement is deferred until the frontend collects
// them (the forms are a separate follow-up); kept here as the single reference.
var leadProspectRequired = []string{
	"customer_lead_source", "customer_lead_score", "customer_expected_close_date",
	"customer_expected_deal_value", "customer_last_contacted_date", "customer_preferred_contact_method",
}

// selectExpr returns the SELECT expression for a registry column, normalising
// nullable text/decimal/date to non-null strings so scanning is uniform.
func selectExpr(f cfield) string {
	switch f.kind {
	case kStr:
		return "COALESCE(c." + f.col + ",'')"
	case kDec:
		return "COALESCE(c." + f.col + "::text,'')"
	case kDate:
		return "COALESCE(to_char(c." + f.col + ",'YYYY-MM-DD'),'')"
	default: // kFK, kInt, kBool
		return "c." + f.col
	}
}

// placeholder returns the bind placeholder for a registry column, adding a cast
// for date/decimal so a string/number argument lands in the right column type.
func placeholder(i int, kind fkind) string {
	switch kind {
	case kDate:
		return fmt.Sprintf("$%d::date", i)
	case kDec:
		return fmt.Sprintf("$%d::numeric", i)
	default:
		return fmt.Sprintf("$%d", i)
	}
}

// recordSelect is the column list + joins shared by all record reads. The fixed
// leading columns must stay in lock-step with the fixed targets in scanRecord;
// the registry columns follow in registry order.
var recordSelect = buildRecordSelect()

func buildRecordSelect() string {
	cols := []string{
		"c.customer_uuid", "rt.record_type_code",
		"c.customer_crm_status", "COALESCE(cs.crm_status_code,'')", "COALESCE(cs.crm_status_name,'')",
		"COALESCE(c.customer_doc_num,'')", "COALESCE(ou.id::text,'')", "COALESCE(p.customer_uuid::text,'')",
		"c.customer_custom_fields", "c.customer_is_approved", "c.customer_approval_status",
		"c.customer_created_at", "c.customer_updated_at",
	}
	for _, f := range customerFields {
		cols = append(cols, selectExpr(f))
	}
	return `
		SELECT ` + strings.Join(cols, ", ") + `
		FROM customer c
		JOIN lkp_record_type rt ON rt.record_type_id = c.record_type
		LEFT JOIN lkp_crm_status cs ON cs.crm_status_id = c.customer_crm_status
		LEFT JOIN customer p ON p.customer_id = c.customer_parent_id
		LEFT JOIN employee oe ON oe.employee_id = c.customer_crm_owner_user_id
		LEFT JOIN users ou ON ou.id = oe.employee_user_id`
}

// scanHolder retains a typed scan target so its value can be read back after Scan.
type scanHolder struct {
	kind fkind
	str  *string
	iptr **int
	b    *bool
}

func scanRecord(row pgx.Row) (*workflow.Record, error) {
	var (
		uuid, typeCode, statusCode, statusName        string
		docNum, ownerUserID, parentUUID, approvalStat string
		crmStatusID                                   *int
		isApproved                                    bool
		custom                                        map[string]any
		rec                                           workflow.Record
	)
	targets := []any{
		&uuid, &typeCode, &crmStatusID, &statusCode, &statusName,
		&docNum, &ownerUserID, &parentUUID,
		&custom, &isApproved, &approvalStat,
		&rec.CreatedAt, &rec.UpdatedAt,
	}
	holders := make([]scanHolder, len(customerFields))
	for i, f := range customerFields {
		switch f.kind {
		case kStr, kDec, kDate:
			p := new(string)
			holders[i] = scanHolder{kind: f.kind, str: p}
			targets = append(targets, p)
		case kFK, kInt:
			p := new(*int)
			holders[i] = scanHolder{kind: f.kind, iptr: p}
			targets = append(targets, p)
		case kBool:
			p := new(bool)
			holders[i] = scanHolder{kind: f.kind, b: p}
			targets = append(targets, p)
		}
	}
	if err := row.Scan(targets...); err != nil {
		return nil, err
	}

	rec.ID = uuid
	rec.WorkflowID = crmCodeToKey[typeCode]
	rec.RecordNumber = docNum
	rec.ParentRecordID = parentUUID
	rec.OwnerUserID = ownerUserID
	if crmStatusID != nil {
		rec.CurrentStateID = strconv.Itoa(*crmStatusID)
	}
	if custom == nil {
		custom = map[string]any{}
	}
	rec.CustomFields = custom

	core := map[string]any{
		"record_type":     crmCodeToKey[typeCode],
		"crm_status_code": statusCode,
		"crm_status_name": statusName,
		"is_approved":     isApproved,
		"approval_status": approvalStat,
	}
	for i, f := range customerFields {
		h := holders[i]
		switch f.kind {
		case kStr:
			if v := *h.str; v != "" || f.always {
				core[f.core] = v
			}
		case kDec, kDate:
			if v := *h.str; v != "" {
				core[f.core] = v
			}
		case kFK, kInt:
			if ip := *h.iptr; ip != nil {
				core[f.core] = strconv.Itoa(*ip)
			}
		case kBool:
			core[f.core] = *h.b
		}
	}
	rec.CoreFields = core
	return &rec, nil
}

// ----- status reads ----------------------------------------------------------

func (s *relationalStore) AllStatuses(ctx context.Context, pool *pgxpool.Pool) ([]workflow.StatusInfo, error) {
	return s.statusesForTypeCodes(ctx, pool, []string{"LEAD", "PROS", "CUST"})
}

func (s *relationalStore) Statuses(ctx context.Context, pool *pgxpool.Pool, key string) ([]workflow.StatusInfo, error) {
	code, ok := crmKeyToCode[key]
	if !ok {
		return nil, ClientError{Msg: "Unknown CRM workflow: " + key}
	}
	return s.statusesForTypeCodes(ctx, pool, []string{code})
}

func (s *relationalStore) AvailableTransitions(ctx context.Context, pool *pgxpool.Pool, id string) ([]workflow.StatusInfo, error) {
	_, typeCode, _, err := s.recordKeyInfo(ctx, pool, id)
	if err != nil {
		return nil, err
	}
	rank := crmCodeRank[typeCode]
	// Forward stages; customer (max rank) shows its own statuses.
	var codes []string
	for code, r := range crmCodeRank {
		if r > rank {
			codes = append(codes, code)
		}
	}
	if len(codes) == 0 {
		codes = []string{typeCode} // customer: own statuses
	}
	return s.statusesForTypeCodes(ctx, pool, codes)
}

// statusesForTypeCodes loads lkp_crm_status rows for the given record-type codes.
func (s *relationalStore) statusesForTypeCodes(ctx context.Context, pool *pgxpool.Pool, codes []string) ([]workflow.StatusInfo, error) {
	rows, err := pool.Query(ctx, `
		SELECT cs.crm_status_id, cs.crm_status_code, cs.crm_status_name,
		       rt.record_type_code, rt.record_type_name
		FROM lkp_crm_status cs
		JOIN lkp_record_type rt ON rt.record_type_id = cs.crm_status_record_type
		WHERE rt.record_type_code = ANY($1) AND cs.crm_status_deleted_at IS NULL AND cs.crm_status_is_active
		ORDER BY cs.crm_status_record_type, cs.crm_status_id`, codes)
	if err != nil {
		return nil, fmt.Errorf("list crm statuses: %w", err)
	}
	defer rows.Close()
	out := []workflow.StatusInfo{}
	for rows.Next() {
		var (
			id                       int
			code, name, tCode, tName string
		)
		if err := rows.Scan(&id, &code, &name, &tCode, &tName); err != nil {
			return nil, err
		}
		out = append(out, workflow.StatusInfo{
			StateID:      strconv.Itoa(id),
			StateKey:     code,
			StatusLabel:  name,
			WorkflowKey:  crmCodeToKey[tCode],
			WorkflowName: tName,
			IsInitial:    true,
			IsTerminal:   strings.Contains(strings.ToLower(name), "closed"),
			SortOrder:    id,
		})
	}
	return out, rows.Err()
}

// ----- record reads ----------------------------------------------------------

func (s *relationalStore) KeyForRecord(ctx context.Context, pool *pgxpool.Pool, id string) (string, error) {
	_, typeCode, _, err := s.recordKeyInfo(ctx, pool, id)
	if err != nil {
		return "", err
	}
	return crmCodeToKey[typeCode], nil
}

func (s *relationalStore) GetRecord(ctx context.Context, pool *pgxpool.Pool, id string) (*workflow.Record, error) {
	rec, err := scanRecord(pool.QueryRow(ctx, recordSelect+`
		WHERE c.customer_uuid = $1 AND c.customer_deleted_at IS NULL`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRecordNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get customer record: %w", err)
	}
	return rec, nil
}

func (s *relationalStore) ListRecords(ctx context.Context, pool *pgxpool.Pool, key, scope, actorIdentityID string) ([]workflow.Record, error) {
	code, ok := crmKeyToCode[key]
	if !ok {
		return nil, ClientError{Msg: "Unknown CRM workflow: " + key}
	}
	q := recordSelect + ` WHERE rt.record_type_code = $1 AND c.customer_deleted_at IS NULL`
	args := []any{code}
	if scope == "own" || scope == "team" {
		empID, found := s.employeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return []workflow.Record{}, nil
		}
		q += ` AND c.customer_crm_owner_user_id = $2`
		args = append(args, empID)
	}
	q += ` ORDER BY c.customer_created_at DESC`
	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list customer records: %w", err)
	}
	defer rows.Close()
	out := []workflow.Record{}
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *rec)
	}
	return out, rows.Err()
}

// CountRecords returns how many customer rows of key exist under scope,
// without fetching rows. Mirrors ListRecords' WHERE/scope clauses exactly
// (including v2's pre-existing "team" == "own" behavior) so counts stay
// consistent with what ListRecords would return for the same scope.
func (s *relationalStore) CountRecords(ctx context.Context, pool *pgxpool.Pool, key, scope, actorIdentityID string) (int, error) {
	code, ok := crmKeyToCode[key]
	if !ok {
		return 0, ClientError{Msg: "Unknown CRM workflow: " + key}
	}
	q := `SELECT COUNT(*)
		FROM customer c
		JOIN lkp_record_type rt ON rt.record_type_id = c.record_type
		WHERE rt.record_type_code = $1 AND c.customer_deleted_at IS NULL`
	args := []any{code}
	if scope == "own" || scope == "team" {
		empID, found := s.employeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return 0, nil
		}
		q += ` AND c.customer_crm_owner_user_id = $2`
		args = append(args, empID)
	}
	var n int
	if err := pool.QueryRow(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count customer records: %w", err)
	}
	return n, nil
}

// ----- record writes ---------------------------------------------------------

// insertCustomer inserts a customer row from the registry-mapped core map plus
// the explicit stage/status/owner/approval columns, generates its document
// number, and returns the new external uuid.
func (s *relationalStore) insertCustomer(ctx context.Context, pool *pgxpool.Pool,
	typeID, statusID, ownerEmp int, approvalStatus, typeCode string,
	parentInternalID *int, core, custom map[string]any) (string, error) {

	cols := []string{
		"record_type", "customer_crm_status", "customer_crm_owner_user_id",
		"customer_approval_status", "customer_custom_fields", "customer_created_by",
	}
	args := []any{typeID, statusID, nullableInt(ownerEmp), approvalStatus, custom, nullableInt(ownerEmp)}
	if parentInternalID != nil {
		cols = append(cols, "customer_parent_id")
		args = append(args, *parentInternalID)
	}
	phs := make([]string, len(args))
	for i := range args {
		phs[i] = fmt.Sprintf("$%d", i+1)
	}
	for _, f := range customerFields {
		cols = append(cols, f.col)
		args = append(args, writeArg(f, core))
		phs = append(phs, placeholder(len(args), f.kind))
	}

	var newUUID string
	var newID int
	q := `INSERT INTO customer (` + strings.Join(cols, ", ") + `)
		VALUES (` + strings.Join(phs, ", ") + `)
		RETURNING customer_uuid, customer_id`
	if err := pool.QueryRow(ctx, q, args...).Scan(&newUUID, &newID); err != nil {
		return "", fmt.Errorf("insert customer: %w", err)
	}
	// Generate the document number from the stage code + serial id (e.g. LEAD-000001).
	docNum := fmt.Sprintf("%s-%06d", typeCode, newID)
	if _, err := pool.Exec(ctx,
		`UPDATE customer SET customer_doc_num = $1 WHERE customer_id = $2`, docNum, newID); err != nil {
		return "", fmt.Errorf("set customer doc number: %w", err)
	}
	return newUUID, nil
}

func (s *relationalStore) CreateRecord(ctx context.Context, pool *pgxpool.Pool, key string, in CreateInput) (*workflow.Record, error) {
	code, ok := crmKeyToCode[key]
	if !ok {
		return nil, ClientError{Msg: "Unknown CRM workflow: " + key}
	}
	core := in.CoreFields
	if core == nil {
		core = map[string]any{}
	}
	if getStr(core, "customer_name") == "" {
		return nil, ClientError{Msg: "Company name is required."}
	}
	typeID, err := s.typeIDByCode(ctx, pool, code)
	if err != nil {
		return nil, err
	}
	statusID, err := s.resolveCreateStatus(ctx, pool, typeID, in.CrmStatusID)
	if err != nil {
		return nil, err
	}
	if err := s.validateCustom(ctx, pool, key, in.CustomFields); err != nil {
		return nil, err
	}
	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}
	ownerEmp := s.ownerEmployee(ctx, pool, in.OwnerUserID, in.ActorIdentityID)
	approvalStatus := "none"
	if statusCode, _ := s.statusCode(ctx, pool, statusID); statusCode == closedWonCode {
		approvalStatus = "pending"
	}
	newUUID, err := s.insertCustomer(ctx, pool, typeID, statusID, ownerEmp, approvalStatus, code, nil, core, custom)
	if err != nil {
		return nil, err
	}
	s.writeHistory(ctx, pool, newUUID, "create", ownerEmp)
	return s.GetRecord(ctx, pool, newUUID)
}

func (s *relationalStore) UpdateRecord(ctx context.Context, pool *pgxpool.Pool, id string, core, custom map[string]any) error {
	rec, err := s.GetRecord(ctx, pool, id)
	if err != nil {
		return err
	}
	key := rec.WorkflowID // record_type key (lead/prospect/customer)
	merged := rec.CustomFields
	if merged == nil {
		merged = map[string]any{}
	}
	for k, v := range custom {
		merged[k] = v
	}
	if err := s.validateCustom(ctx, pool, key, merged); err != nil {
		return err
	}
	c := rec.CoreFields
	for k, v := range core {
		c[k] = v
	}

	sets := make([]string, 0, len(customerFields)+3)
	args := []any{id}
	for _, f := range customerFields {
		sets = append(sets, fmt.Sprintf("%s = %s", f.col, placeholder(len(args)+1, f.kind)))
		args = append(args, writeArg(f, c))
	}
	sets = append(sets, fmt.Sprintf("customer_custom_fields = $%d", len(args)+1))
	args = append(args, merged)

	q := `UPDATE customer SET ` + strings.Join(sets, ", ") + `,
		customer_updated_at = NOW(),
		customer_record_version = customer_record_version + 1
		WHERE customer_uuid = $1 AND customer_deleted_at IS NULL`
	if _, err = pool.Exec(ctx, q, args...); err != nil {
		return fmt.Errorf("update customer record: %w", err)
	}
	return nil
}

func (s *relationalStore) DeleteRecord(ctx context.Context, pool *pgxpool.Pool, id string) error {
	tag, err := pool.Exec(ctx, `
		UPDATE customer
		SET customer_deleted_at = NOW(), customer_deleted_by = 1
		WHERE customer_uuid = $1 AND customer_deleted_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("delete customer record: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrRecordNotFound
	}
	return nil
}

func (s *relationalStore) TransitionRecord(ctx context.Context, pool *pgxpool.Pool, id, toStatusID, actorIdentityID string) (*workflow.Record, error) {
	internalID, curTypeCode, _, err := s.recordKeyInfo(ctx, pool, id)
	if err != nil {
		return nil, err
	}
	statusID, err := strconv.Atoi(toStatusID)
	if err != nil {
		return nil, ClientError{Msg: "Invalid status id."}
	}
	targetTypeCode, statusCode, err := s.statusTypeAndCode(ctx, pool, statusID)
	if err != nil {
		return nil, err
	}
	if crmCodeRank[targetTypeCode] < crmCodeRank[curTypeCode] {
		return nil, ClientError{Msg: "CRM records can only move forward (lead → prospect → customer), not backward."}
	}
	targetTypeID, err := s.typeIDByCode(ctx, pool, targetTypeCode)
	if err != nil {
		return nil, err
	}
	approvalClause := ""
	if statusCode == closedWonCode {
		approvalClause = ", customer_approval_status = 'pending', customer_is_approved = FALSE"
	}
	_, err = pool.Exec(ctx, `
		UPDATE customer SET
			record_type = $2, customer_crm_status = $3,
			customer_updated_at = NOW(),
			customer_record_version = customer_record_version + 1`+approvalClause+`
		WHERE customer_uuid = $1 AND customer_deleted_at IS NULL`,
		id, targetTypeID, statusID)
	if err != nil {
		return nil, fmt.Errorf("transition customer record: %w", err)
	}
	// When the record moves to a new stage, regenerate the document number so
	// its prefix matches the new stage (e.g. LEAD-000042 → PROS-000042).
	// This is best-effort: the record is already moved, so we do not surface
	// any error from this secondary update.
	if targetTypeCode != curTypeCode {
		newDocNum := fmt.Sprintf("%s-%06d", targetTypeCode, internalID)
		_, _ = pool.Exec(ctx,
			`UPDATE customer SET customer_doc_num = $1 WHERE customer_uuid = $2`,
			newDocNum, id)
	}
	action := "transition"
	if targetTypeCode != curTypeCode {
		action = "convert"
	}
	s.writeHistory(ctx, pool, id, action, s.employeeIDOrZero(ctx, pool, actorIdentityID))
	return s.GetRecord(ctx, pool, id)
}

func (s *relationalStore) ConvertRecord(ctx context.Context, pool *pgxpool.Pool, id, targetKey string, core, custom map[string]any, actorIdentityID string) (*workflow.Record, string, error) {
	source, err := s.GetRecord(ctx, pool, id)
	if err != nil {
		return nil, "", err
	}
	code, ok := crmKeyToCode[targetKey]
	if !ok {
		return nil, "", ClientError{Msg: "Unknown target workflow: " + targetKey}
	}
	if crmCodeRank[code] <= crmCodeRank[crmKeyToCode[source.WorkflowID]] {
		return nil, "", ClientError{Msg: "Conversion must move forward to a later stage."}
	}
	typeID, err := s.typeIDByCode(ctx, pool, code)
	if err != nil {
		return nil, "", err
	}
	statusID, err := s.resolveCreateStatus(ctx, pool, typeID, "")
	if err != nil {
		return nil, "", err
	}
	// Seed core/custom from source where the caller did not override.
	if core == nil {
		core = map[string]any{}
	}
	for k, v := range source.CoreFields {
		if _, exists := core[k]; !exists {
			core[k] = v
		}
	}
	if custom == nil {
		custom = map[string]any{}
	}
	for k, v := range source.CustomFields {
		if _, exists := custom[k]; !exists {
			custom[k] = v
		}
	}
	// Resolve the source's internal id for the lineage FK.
	parentInternalID, _, _, err := s.recordKeyInfo(ctx, pool, id)
	if err != nil {
		return nil, "", err
	}
	ownerEmp := s.employeeIDOrZero(ctx, pool, actorIdentityID)
	newUUID, err := s.insertCustomer(ctx, pool, typeID, statusID, ownerEmp, "none", code, &parentInternalID, core, custom)
	if err != nil {
		return nil, "", err
	}
	s.writeHistory(ctx, pool, newUUID, "convert", ownerEmp)
	newRec, err := s.GetRecord(ctx, pool, newUUID)
	if err != nil {
		return nil, "", err
	}
	return newRec, id, nil
}

func (s *relationalStore) Approve(ctx context.Context, pool *pgxpool.Pool, id, approverIdentityID string) (*workflow.Record, error) {
	rec, err := s.GetRecord(ctx, pool, id)
	if err != nil {
		return nil, err
	}
	if rec.WorkflowID != "customer" {
		return nil, ClientError{Msg: "Only customer records require approval."}
	}
	if rec.CoreFields["approval_status"] != "pending" {
		return nil, ClientError{Msg: "This record is not pending approval."}
	}
	empID, found := s.employeeIDByIdentity(ctx, pool, approverIdentityID)
	if !found {
		return nil, ErrNotApprover
	}
	// The approver must be configured for CUST (optionally for this exact status).
	var allowed bool
	err = pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM crm_workflow_approver a
			JOIN lkp_record_type rt ON rt.record_type_id = a.record_type_id
			JOIN customer r ON r.customer_uuid = $1
			WHERE rt.record_type_code = 'CUST'
			  AND a.approver_employee_id = $2 AND a.is_active
			  AND (a.crm_status_id IS NULL OR a.crm_status_id = r.customer_crm_status)
		)`, id, empID).Scan(&allowed)
	if err != nil {
		return nil, fmt.Errorf("check approver: %w", err)
	}
	if !allowed {
		return nil, ErrNotApprover
	}
	if _, err := pool.Exec(ctx, `
		UPDATE customer SET
			customer_is_approved = TRUE, customer_approval_status = 'approved',
			customer_approved_by = $2, customer_approved_at = NOW(),
			customer_updated_at = NOW(),
			customer_record_version = customer_record_version + 1
		WHERE customer_uuid = $1`, id, empID); err != nil {
		return nil, fmt.Errorf("approve customer record: %w", err)
	}
	s.writeHistory(ctx, pool, id, "approve", empID)
	return s.GetRecord(ctx, pool, id)
}

// ----- helpers ---------------------------------------------------------------

// recordKeyInfo returns (internalID, typeCode, crmStatusID) for a customer uuid.
func (s *relationalStore) recordKeyInfo(ctx context.Context, pool *pgxpool.Pool, uuid string) (int, string, int, error) {
	var (
		id       int
		typeCode string
		statusID *int
	)
	err := pool.QueryRow(ctx, `
		SELECT c.customer_id, rt.record_type_code, c.customer_crm_status
		FROM customer c JOIN lkp_record_type rt ON rt.record_type_id = c.record_type
		WHERE c.customer_uuid = $1 AND c.customer_deleted_at IS NULL`, uuid).Scan(&id, &typeCode, &statusID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", 0, ErrRecordNotFound
	}
	if err != nil {
		return 0, "", 0, fmt.Errorf("record key info: %w", err)
	}
	sid := 0
	if statusID != nil {
		sid = *statusID
	}
	return id, typeCode, sid, nil
}

func (s *relationalStore) typeIDByCode(ctx context.Context, pool *pgxpool.Pool, code string) (int, error) {
	var id int
	err := pool.QueryRow(ctx,
		`SELECT record_type_id FROM lkp_record_type WHERE record_type_code = $1`, code).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("record type %q: %w", code, err)
	}
	return id, nil
}

// resolveCreateStatus validates a chosen status belongs to typeID, or defaults
// to the first status of that stage.
func (s *relationalStore) resolveCreateStatus(ctx context.Context, pool *pgxpool.Pool, typeID int, chosen string) (int, error) {
	if chosen != "" {
		sid, err := strconv.Atoi(chosen)
		if err != nil {
			return 0, ClientError{Msg: "Invalid status id."}
		}
		var rt int
		if err := pool.QueryRow(ctx,
			`SELECT crm_status_record_type FROM lkp_crm_status WHERE crm_status_id = $1`, sid).Scan(&rt); err != nil {
			return 0, ClientError{Msg: "Unknown status."}
		}
		if rt != typeID {
			return 0, ClientError{Msg: "The selected status does not belong to this stage."}
		}
		return sid, nil
	}
	var sid int
	if err := pool.QueryRow(ctx, `
		SELECT crm_status_id FROM lkp_crm_status
		WHERE crm_status_record_type = $1 AND crm_status_is_active AND crm_status_deleted_at IS NULL
		ORDER BY crm_status_id LIMIT 1`, typeID).Scan(&sid); err != nil {
		return 0, fmt.Errorf("default status for type %d: %w", typeID, err)
	}
	return sid, nil
}

func (s *relationalStore) statusTypeAndCode(ctx context.Context, pool *pgxpool.Pool, statusID int) (string, string, error) {
	var typeCode, code string
	err := pool.QueryRow(ctx, `
		SELECT rt.record_type_code, cs.crm_status_code
		FROM lkp_crm_status cs JOIN lkp_record_type rt ON rt.record_type_id = cs.crm_status_record_type
		WHERE cs.crm_status_id = $1`, statusID).Scan(&typeCode, &code)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ClientError{Msg: "Unknown status."}
	}
	if err != nil {
		return "", "", fmt.Errorf("status lookup: %w", err)
	}
	return typeCode, code, nil
}

func (s *relationalStore) statusCode(ctx context.Context, pool *pgxpool.Pool, statusID int) (string, error) {
	var code string
	err := pool.QueryRow(ctx,
		`SELECT crm_status_code FROM lkp_crm_status WHERE crm_status_id = $1`, statusID).Scan(&code)
	return code, err
}

// validateCustom reuses the workflow field definitions for the matching CRM
// workflow to validate dynamic fields (<=15, typed) before save.
func (s *relationalStore) validateCustom(ctx context.Context, pool *pgxpool.Pool, key string, custom map[string]any) error {
	wf, err := workflow.GetWorkflowByKey(ctx, pool, key)
	if errors.Is(err, workflow.ErrWorkflowNotFound) {
		return nil // no definition: accept as-is
	}
	if err != nil {
		return err
	}
	def, err := workflow.LoadDefinition(ctx, pool, wf.ID)
	if err != nil {
		return err
	}
	if custom == nil {
		return nil
	}
	if err := workflow.ValidateCustomFieldsPartial(def.Fields, custom); err != nil {
		return ClientError{Msg: err.Error()}
	}
	return nil
}

// employeeIDByIdentity resolves a control-plane identity to a tenant employee_id.
func (s *relationalStore) employeeIDByIdentity(ctx context.Context, pool *pgxpool.Pool, identityID string) (int, bool) {
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

func (s *relationalStore) employeeIDOrZero(ctx context.Context, pool *pgxpool.Pool, identityID string) int {
	id, _ := s.employeeIDByIdentity(ctx, pool, identityID)
	return id
}

// ownerEmployee picks the owning employee: explicit user → employee, else caller.
func (s *relationalStore) ownerEmployee(ctx context.Context, pool *pgxpool.Pool, ownerUserID, actorIdentityID string) int {
	if ownerUserID != "" {
		var id int
		if err := pool.QueryRow(ctx,
			`SELECT employee_id FROM employee WHERE employee_user_id = $1 AND employee_deleted_at IS NULL`,
			ownerUserID).Scan(&id); err == nil {
			return id
		}
	}
	return s.employeeIDOrZero(ctx, pool, actorIdentityID)
}

func (s *relationalStore) writeHistory(ctx context.Context, pool *pgxpool.Pool, uuid, action string, actorEmp int) {
	_, _ = pool.Exec(ctx, `
		INSERT INTO customer_history (customer_id, to_type_id, to_crm_status_id, action, actor_employee_id)
		SELECT customer_id, record_type, customer_crm_status, $2, $3
		FROM customer WHERE customer_uuid = $1`, uuid, action, nullableInt(actorEmp))
}

func getStr(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
		if v != nil {
			return fmt.Sprintf("%v", v)
		}
	}
	return ""
}

// getBool reads a boolean CoreFields value, tolerating string forms ("true"/"1").
func getBool(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	switch t := m[key].(type) {
	case bool:
		return t
	case string:
		return t == "true" || t == "1" || t == "t" || t == "yes"
	default:
		return false
	}
}

func nullableInt(v int) any {
	if v <= 0 {
		return nil
	}
	return v
}

// writeArg converts a CoreFields value to the SQL argument for a registry column.
func writeArg(f cfield, m map[string]any) any {
	switch f.kind {
	case kStr:
		return getStr(m, f.core)
	case kBool:
		return getBool(m, f.core)
	case kDec:
		return nullableNumFromCore(m, f.core)
	case kDate:
		return nullableDateFromCore(m, f.core)
	default: // kFK, kInt
		return nullableIntFromCore(m, f.core)
	}
}

// nullableIntFromCore extracts an integer lookup id stored under key in a
// CoreFields map (as a numeric string or number) for writing to a FK column.
// Returns nil (SQL NULL) if the key is missing, empty, or unparsable.
func nullableIntFromCore(core map[string]any, key string) any {
	if core == nil {
		return nil
	}
	v, ok := core[key]
	if !ok || v == nil {
		return nil
	}
	switch t := v.(type) {
	case string:
		if t == "" {
			return nil
		}
		n, err := strconv.Atoi(t)
		if err != nil {
			return nil
		}
		return n
	case float64:
		return int(t)
	case int:
		return t
	default:
		return nil
	}
}

// nullableNumFromCore extracts a decimal value (number or numeric string) for a
// DECIMAL column. Returns nil if missing, empty, or unparsable.
func nullableNumFromCore(core map[string]any, key string) any {
	if core == nil {
		return nil
	}
	v, ok := core[key]
	if !ok || v == nil {
		return nil
	}
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return t
	case string:
		if t == "" {
			return nil
		}
		n, err := strconv.ParseFloat(t, 64)
		if err != nil {
			return nil
		}
		return n
	default:
		return nil
	}
}

// nullableDateFromCore returns a 'YYYY-MM-DD' string (cast to date in SQL) or nil
// when the value is missing or empty.
func nullableDateFromCore(core map[string]any, key string) any {
	if core == nil {
		return nil
	}
	v, ok := core[key]
	if !ok || v == nil {
		return nil
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return nil
	}
	return s
}

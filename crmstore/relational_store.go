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

// relationalStore is the DesignV2 implementation: a single crm_record table
// keyed by record_type (LEAD/PROS/CUST), with statuses and transitions driven
// by lkp_crm_status. Lead/Prospect/Customer are views of one table; advancing a
// record to a status of a later type relocates it to that listing (forward-only).
type relationalStore struct{}

var _ Store = (*relationalStore)(nil)

// CRM stage ranks: a record may only advance to an equal-or-higher rank.
var crmKeyToCode = map[string]string{"lead": "LEAD", "prospect": "PROS", "customer": "CUST"}
var crmCodeToKey = map[string]string{"LEAD": "lead", "PROS": "prospect", "CUST": "customer"}
var crmCodeRank = map[string]int{"LEAD": 1, "PROS": 2, "CUST": 3}

const closedWonCode = "CCLW" // Customer Closed Won — triggers approval

// recordSelect is the column list + joins shared by record reads.
const recordSelect = `
	SELECT r.crm_record_uuid, rt.record_type_code,
	       r.crm_record_crm_status_id, COALESCE(cs.crm_status_code,''), COALESCE(cs.crm_status_name,''),
	       r.crm_record_company_name, r.crm_record_first_name, r.crm_record_last_name,
	       r.crm_record_email, r.crm_record_phone, r.crm_record_address,
	       r.crm_record_customer_type_id, r.crm_record_ar_status_id, r.crm_record_payment_terms_id,
	       r.crm_record_currency_id, r.crm_record_country_id, r.crm_record_state_id,
	       r.crm_record_lead_source_id, r.crm_record_contact_method_id,
	       r.crm_record_custom_fields, r.crm_record_is_approved, r.crm_record_approval_status,
	       COALESCE(p.crm_record_uuid::text, ''), COALESCE(ou.id::text, ''),
	       r.crm_record_created_at, r.crm_record_updated_at
	FROM crm_record r
	JOIN lkp_record_type rt ON rt.record_type_id = r.crm_record_type_id
	LEFT JOIN lkp_crm_status cs ON cs.crm_status_id = r.crm_record_crm_status_id
	LEFT JOIN crm_record p ON p.crm_record_id = r.crm_record_parent_id
	LEFT JOIN employee oe ON oe.employee_id = r.crm_record_owner_employee_id
	LEFT JOIN users ou ON ou.id = oe.employee_user_id`

func scanRecord(row pgx.Row) (*workflow.Record, error) {
	var (
		uuid, typeCode, statusCode, statusName      string
		company, first, last, email, phone, address string
		approvalStatus, parentUUID, ownerUserID     string
		crmStatusID                                 *int
		customerTypeID, arStatusID, paymentTermsID  *int
		currencyID, countryID, stateID              *int
		leadSourceID, contactMethodID               *int
		isApproved                                  bool
		custom                                      map[string]any
		rec                                         workflow.Record
	)
	if err := row.Scan(&uuid, &typeCode, &crmStatusID, &statusCode, &statusName,
		&company, &first, &last, &email, &phone, &address,
		&customerTypeID, &arStatusID, &paymentTermsID,
		&currencyID, &countryID, &stateID,
		&leadSourceID, &contactMethodID,
		&custom, &isApproved, &approvalStatus,
		&parentUUID, &ownerUserID, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
		return nil, err
	}
	rec.ID = uuid
	rec.WorkflowID = crmCodeToKey[typeCode]
	rec.ParentRecordID = parentUUID
	rec.OwnerUserID = ownerUserID
	if crmStatusID != nil {
		rec.CurrentStateID = strconv.Itoa(*crmStatusID)
	}
	if custom == nil {
		custom = map[string]any{}
	}
	rec.CustomFields = custom
	rec.CoreFields = map[string]any{
		"company_name":    company,
		"first_name":      first,
		"last_name":       last,
		"email":           email,
		"phone":           phone,
		"address":         address,
		"record_type":     crmCodeToKey[typeCode],
		"crm_status_code": statusCode,
		"crm_status_name": statusName,
		"is_approved":     isApproved,
		"approval_status": approvalStatus,
	}
	setLookupID(rec.CoreFields, "customer_type_id", customerTypeID)
	setLookupID(rec.CoreFields, "ar_status_id", arStatusID)
	setLookupID(rec.CoreFields, "payment_terms_id", paymentTermsID)
	setLookupID(rec.CoreFields, "currency_id", currencyID)
	setLookupID(rec.CoreFields, "country_id", countryID)
	setLookupID(rec.CoreFields, "state_id", stateID)
	setLookupID(rec.CoreFields, "lead_source_id", leadSourceID)
	setLookupID(rec.CoreFields, "contact_method_id", contactMethodID)
	return &rec, nil
}

// setLookupID sets core[key] to the string form of v if non-nil, leaving the
// key absent when the FK column is NULL.
func setLookupID(core map[string]any, key string, v *int) {
	if v != nil {
		core[key] = strconv.Itoa(*v)
	}
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
		WHERE r.crm_record_uuid = $1 AND r.crm_record_deleted_at IS NULL`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRecordNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get crm record: %w", err)
	}
	return rec, nil
}

func (s *relationalStore) ListRecords(ctx context.Context, pool *pgxpool.Pool, key, scope, actorIdentityID string) ([]workflow.Record, error) {
	code, ok := crmKeyToCode[key]
	if !ok {
		return nil, ClientError{Msg: "Unknown CRM workflow: " + key}
	}
	q := recordSelect + ` WHERE rt.record_type_code = $1 AND r.crm_record_deleted_at IS NULL`
	args := []any{code}
	if scope == "own" || scope == "team" {
		empID, found := s.employeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return []workflow.Record{}, nil
		}
		q += ` AND r.crm_record_owner_employee_id = $2`
		args = append(args, empID)
	}
	q += ` ORDER BY r.crm_record_created_at DESC`
	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list crm records: %w", err)
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

// ----- record writes ---------------------------------------------------------

func (s *relationalStore) CreateRecord(ctx context.Context, pool *pgxpool.Pool, key string, in CreateInput) (*workflow.Record, error) {
	code, ok := crmKeyToCode[key]
	if !ok {
		return nil, ClientError{Msg: "Unknown CRM workflow: " + key}
	}
	typeID, err := s.typeIDByCode(ctx, pool, code)
	if err != nil {
		return nil, err
	}
	// Resolve & validate the chosen status (must belong to this stage).
	statusID, err := s.resolveCreateStatus(ctx, pool, typeID, in.CrmStatusID)
	if err != nil {
		return nil, err
	}
	// Validate dynamic fields against the workflow definition for this stage.
	if err := s.validateCustom(ctx, pool, key, in.CustomFields); err != nil {
		return nil, err
	}
	core := in.CoreFields
	if core == nil {
		core = map[string]any{}
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

	var newUUID string
	err = pool.QueryRow(ctx, `
		INSERT INTO crm_record (
			crm_record_type_id, crm_record_crm_status_id,
			crm_record_company_name, crm_record_first_name, crm_record_last_name,
			crm_record_email, crm_record_phone, crm_record_address,
			crm_record_customer_type_id, crm_record_ar_status_id, crm_record_payment_terms_id,
			crm_record_currency_id, crm_record_country_id, crm_record_state_id,
			crm_record_lead_source_id, crm_record_contact_method_id,
			crm_record_owner_employee_id, crm_record_approval_status,
			crm_record_custom_fields, crm_record_created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
		RETURNING crm_record_uuid`,
		typeID, statusID,
		getStr(core, "company_name"), getStr(core, "first_name"), getStr(core, "last_name"),
		getStr(core, "email"), getStr(core, "phone"), getStr(core, "address"),
		nullableIntFromCore(core, "customer_type_id"), nullableIntFromCore(core, "ar_status_id"),
		nullableIntFromCore(core, "payment_terms_id"), nullableIntFromCore(core, "currency_id"),
		nullableIntFromCore(core, "country_id"), nullableIntFromCore(core, "state_id"),
		nullableIntFromCore(core, "lead_source_id"), nullableIntFromCore(core, "contact_method_id"),
		nullableInt(ownerEmp), approvalStatus, custom, nullableInt(ownerEmp),
	).Scan(&newUUID)
	if err != nil {
		return nil, fmt.Errorf("insert crm record: %w", err)
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
	_, err = pool.Exec(ctx, `
		UPDATE crm_record SET
			crm_record_company_name = $2, crm_record_first_name = $3, crm_record_last_name = $4,
			crm_record_email = $5, crm_record_phone = $6, crm_record_address = $7,
			crm_record_customer_type_id = $8, crm_record_ar_status_id = $9, crm_record_payment_terms_id = $10,
			crm_record_currency_id = $11, crm_record_country_id = $12, crm_record_state_id = $13,
			crm_record_lead_source_id = $14, crm_record_contact_method_id = $15,
			crm_record_custom_fields = $16, crm_record_updated_at = NOW(),
			crm_record_record_version = crm_record_record_version + 1
		WHERE crm_record_uuid = $1 AND crm_record_deleted_at IS NULL`,
		id, getStr(c, "company_name"), getStr(c, "first_name"), getStr(c, "last_name"),
		getStr(c, "email"), getStr(c, "phone"), getStr(c, "address"),
		nullableIntFromCore(c, "customer_type_id"), nullableIntFromCore(c, "ar_status_id"),
		nullableIntFromCore(c, "payment_terms_id"), nullableIntFromCore(c, "currency_id"),
		nullableIntFromCore(c, "country_id"), nullableIntFromCore(c, "state_id"),
		nullableIntFromCore(c, "lead_source_id"), nullableIntFromCore(c, "contact_method_id"),
		merged)
	if err != nil {
		return fmt.Errorf("update crm record: %w", err)
	}
	return nil
}

func (s *relationalStore) DeleteRecord(ctx context.Context, pool *pgxpool.Pool, id string) error {
	tag, err := pool.Exec(ctx, `
		UPDATE crm_record
		SET crm_record_deleted_at = NOW(), crm_record_deleted_by = 1
		WHERE crm_record_uuid = $1 AND crm_record_deleted_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("delete crm record: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrRecordNotFound
	}
	return nil
}

func (s *relationalStore) TransitionRecord(ctx context.Context, pool *pgxpool.Pool, id, toStatusID, actorIdentityID string) (*workflow.Record, error) {
	recID, curTypeCode, _, err := s.recordKeyInfo(ctx, pool, id)
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
		approvalClause = ", crm_record_approval_status = 'pending', crm_record_is_approved = FALSE"
	}
	_, err = pool.Exec(ctx, `
		UPDATE crm_record SET
			crm_record_type_id = $2, crm_record_crm_status_id = $3,
			crm_record_updated_at = NOW(),
			crm_record_record_version = crm_record_record_version + 1`+approvalClause+`
		WHERE crm_record_uuid = $1 AND crm_record_deleted_at IS NULL`,
		id, targetTypeID, statusID)
	if err != nil {
		return nil, fmt.Errorf("transition crm record: %w", err)
	}
	action := "transition"
	if targetTypeCode != curTypeCode {
		action = "convert"
	}
	s.writeHistory(ctx, pool, id, action, s.employeeIDOrZero(ctx, pool, actorIdentityID))
	_ = recID
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
	// Seed core fields from source.
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
	ownerEmp := s.employeeIDOrZero(ctx, pool, actorIdentityID)
	var newUUID string
	err = pool.QueryRow(ctx, `
		INSERT INTO crm_record (
			crm_record_type_id, crm_record_crm_status_id,
			crm_record_company_name, crm_record_first_name, crm_record_last_name,
			crm_record_email, crm_record_phone, crm_record_address,
			crm_record_customer_type_id, crm_record_ar_status_id, crm_record_payment_terms_id,
			crm_record_currency_id, crm_record_country_id, crm_record_state_id,
			crm_record_lead_source_id, crm_record_contact_method_id,
			crm_record_owner_employee_id, crm_record_custom_fields,
			crm_record_parent_id, crm_record_created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,
			(SELECT crm_record_id FROM crm_record WHERE crm_record_uuid = $19), $20)
		RETURNING crm_record_uuid`,
		typeID, statusID,
		getStr(core, "company_name"), getStr(core, "first_name"), getStr(core, "last_name"),
		getStr(core, "email"), getStr(core, "phone"), getStr(core, "address"),
		nullableIntFromCore(core, "customer_type_id"), nullableIntFromCore(core, "ar_status_id"),
		nullableIntFromCore(core, "payment_terms_id"), nullableIntFromCore(core, "currency_id"),
		nullableIntFromCore(core, "country_id"), nullableIntFromCore(core, "state_id"),
		nullableIntFromCore(core, "lead_source_id"), nullableIntFromCore(core, "contact_method_id"),
		nullableInt(ownerEmp), custom, id, nullableInt(ownerEmp)).Scan(&newUUID)
	if err != nil {
		return nil, "", fmt.Errorf("convert crm record: %w", err)
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
			JOIN crm_record r ON r.crm_record_uuid = $1
			WHERE rt.record_type_code = 'CUST'
			  AND a.approver_employee_id = $2 AND a.is_active
			  AND (a.crm_status_id IS NULL OR a.crm_status_id = r.crm_record_crm_status_id)
		)`, id, empID).Scan(&allowed)
	if err != nil {
		return nil, fmt.Errorf("check approver: %w", err)
	}
	if !allowed {
		return nil, ErrNotApprover
	}
	if _, err := pool.Exec(ctx, `
		UPDATE crm_record SET
			crm_record_is_approved = TRUE, crm_record_approval_status = 'approved',
			crm_record_approved_by = $2, crm_record_approved_at = NOW(),
			crm_record_updated_at = NOW(),
			crm_record_record_version = crm_record_record_version + 1
		WHERE crm_record_uuid = $1`, id, empID); err != nil {
		return nil, fmt.Errorf("approve crm record: %w", err)
	}
	s.writeHistory(ctx, pool, id, "approve", empID)
	return s.GetRecord(ctx, pool, id)
}

// ----- helpers ---------------------------------------------------------------

// recordKeyInfo returns (internalID, typeCode, crmStatusID) for a record uuid.
func (s *relationalStore) recordKeyInfo(ctx context.Context, pool *pgxpool.Pool, uuid string) (int, string, int, error) {
	var (
		id       int
		typeCode string
		statusID *int
	)
	err := pool.QueryRow(ctx, `
		SELECT r.crm_record_id, rt.record_type_code, r.crm_record_crm_status_id
		FROM crm_record r JOIN lkp_record_type rt ON rt.record_type_id = r.crm_record_type_id
		WHERE r.crm_record_uuid = $1 AND r.crm_record_deleted_at IS NULL`, uuid).Scan(&id, &typeCode, &statusID)
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
		INSERT INTO crm_record_history (crm_record_id, to_type_id, to_crm_status_id, action, actor_employee_id)
		SELECT crm_record_id, crm_record_type_id, crm_record_crm_status_id, $2, $3
		FROM crm_record WHERE crm_record_uuid = $1`, uuid, action, nullableInt(actorEmp))
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

func nullableInt(v int) any {
	if v <= 0 {
		return nil
	}
	return v
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

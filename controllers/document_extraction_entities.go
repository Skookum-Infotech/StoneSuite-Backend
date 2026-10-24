package controllers

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"stonesuite-backend/authz"
	"stonesuite-backend/docextract"
	"stonesuite-backend/docextractjob"
	"stonesuite-backend/proposedaction"
	"stonesuite-backend/proposedentity"
	"stonesuite-backend/workflow"
)

// Messages and labels of the propose endpoint.
const (
	msgProposedNotFound   = "Proposed record not found."
	msgProposeUnavailable = "Proposing records is not available."
	msgProposeDenied      = "You do not have permission to create this kind of record."
	msgProposeInternal    = "Failed to propose the record."
	msgProposeUsed        = "This document was already used to create a record."
	msgProposeNotReady    = "This document is not ready."
	// viaDocExtractPropose labels permission_denied events from this endpoint.
	viaDocExtractPropose = "docextract_propose"
)

// proposeOverrides are the only caller-supplied values a proposal accepts.
type proposeOverrides struct {
	ContactEmail string `json:"contactEmail"`
	SKU          string `json:"sku"`
}

// proposeRequest is the optional body of the propose endpoint.
type proposeRequest struct {
	Overrides proposeOverrides `json:"overrides"`
}

// decodeProposeBody strictly decodes the optional body: an empty body is fine,
// unknown fields and trailing data are 400.
func decodeProposeBody(w http.ResponseWriter, r *http.Request) (proposeRequest, bool) {
	var req proposeRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxDocExtractBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(&req)
	if errors.Is(err, io.EOF) {
		return req, true
	}
	if err == nil {
		if _, terr := dec.Token(); !errors.Is(terr, io.EOF) {
			err = errors.New("trailing data")
		}
	}
	if err != nil {
		fail(w, http.StatusBadRequest, msgInvalidBody)
		return req, false
	}
	return req, true
}

// findProposedEntity returns the stored entity with key.
func findProposedEntity(entities []docextractjob.ProposedEntity, key string) (docextractjob.ProposedEntity, bool) {
	for _, e := range entities {
		if e.Key == key {
			return e, true
		}
	}
	return docextractjob.ProposedEntity{}, false
}

// proposeInputFor builds the ProposeInput from the stored entity; only the
// whitelisted overrides come from the request. A missing required input is
// returned as the 422 message.
func proposeInputFor(e docextractjob.ProposedEntity, ov proposeOverrides, ref, docType string) (proposedentity.ProposeInput, string) {
	in := proposedentity.ProposeInput{
		Kind: e.Kind, SourceRef: ref, Name: e.Name, Address: e.Fields.Address, TaxID: e.Fields.TaxID,
		SKU: e.Fields.SKU, Description: e.Fields.Description, DocUoM: e.Fields.UoM,
		UnitPriceCents: e.Fields.UnitPriceCents, Side: proposedentity.SidePurchase,
	}
	if docextract.DocType(docType) == docextract.DocTypeSalesOrder {
		in.Side = proposedentity.SideSales
	}
	email, sku := strings.TrimSpace(ov.ContactEmail), strings.TrimSpace(ov.SKU)
	if e.Kind == docextractjob.ProposedKindCustomer || e.Kind == docextractjob.ProposedKindVendor {
		in.ContactEmail = email
	}
	for _, need := range e.Needs {
		switch need {
		case docextractjob.NeedContactEmail:
			if email == "" {
				return in, "contactEmail is required to create this record."
			}
		case docextractjob.NeedSKU:
			if sku == "" {
				return in, "sku is required to create this record."
			}
			in.SKU = sku
		default:
			return in, "A required input is missing."
		}
	}
	return in, ""
}

// ProposeEntity handles POST /api/tenant/document-extractions/{id}/proposed-entities/{key}/propose:
// turns one proposed record of a ready extraction into a pending ProposedAction
// the caller confirms via /api/tenant/actions/{id}/confirm. RBAC:
// Require(<doc type resource>, create) and Require(<target resource>, create);
// owner-only. Idempotent: a pending or confirmed proposal for the same record
// is returned (200) instead of creating another (201).
func (h *DocExtractOps) ProposeEntity(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.ProposeEntities {
		fail(w, http.StatusNotFound, "Not found.")
		return
	}
	c, ex, ok := h.authOwned(w, r, false)
	if !ok {
		return
	}
	if h.newActionStore == nil {
		fail(w, http.StatusServiceUnavailable, msgProposeUnavailable)
		return
	}
	switch ex.Status {
	case docextractjob.StatusReady:
	case docextractjob.StatusUsed, docextractjob.StatusAttached:
		failWithCode(w, http.StatusConflict, msgProposeUsed, codeAlreadyUsed)
		return
	default:
		failWithCode(w, http.StatusConflict, msgProposeNotReady, codeInvalidState)
		return
	}
	req, ok := decodeProposeBody(w, r)
	if !ok {
		return
	}
	entity, ok := h.loadProposedEntity(w, r, c, ex, r.PathValue("key"))
	if !ok {
		return
	}
	res, ok := h.requireTargetCreate(w, r, c, entity.Kind)
	if !ok {
		return
	}
	ref := ex.ID + sourceRefSep + entity.Key
	in, missing := proposeInputFor(entity, req.Overrides, ref, ex.DocType)
	if missing != "" {
		fail(w, http.StatusUnprocessableEntity, missing)
		return
	}
	userID, err := h.callerUserID(r.Context(), c)
	if errors.Is(err, workflow.ErrNoTenantUser) || (err == nil && userID == "") {
		fail(w, http.StatusForbidden, msgActionNoProfile)
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "doc extract: resolve caller user", "extractionId", ex.ID, "error", err)
		fail(w, http.StatusInternalServerError, msgProposeInternal)
		return
	}
	h.proposeOrReturn(w, r, c, ex.ID, res, in, proposedaction.Caller{IdentityID: c.identityID, UserID: userID})
}

// loadProposedEntity reads the stored result, refreshes it best-effort and
// finds the entity; anything absent is 404.
func (h *DocExtractOps) loadProposedEntity(w http.ResponseWriter, r *http.Request, c *docExtractCaller, ex *docextractjob.Extraction, key string) (docextractjob.ProposedEntity, bool) {
	var doc docextractjob.ResultDoc
	if len(ex.Result) > 0 {
		if err := json.Unmarshal(ex.Result, &doc); err != nil {
			slog.ErrorContext(r.Context(), "doc extract: decode stored result", "extractionId", ex.ID, "error", err)
			fail(w, http.StatusInternalServerError, msgProposeInternal)
			return docextractjob.ProposedEntity{}, false
		}
		if fresh, err := h.refresh(r.Context(), c, ex, doc); err != nil {
			slog.WarnContext(r.Context(), "doc extract: refresh resolution", "extractionId", ex.ID, "error", err)
		} else {
			doc = fresh
		}
	}
	entity, found := findProposedEntity(doc.ProposedEntities, key)
	if !found {
		fail(w, http.StatusNotFound, msgProposedNotFound)
		return entity, false
	}
	return entity, true
}

// requireTargetCreate enforces Require(<entity resource>, create): 403 after a
// permission_denied security log on denial.
func (h *DocExtractOps) requireTargetCreate(w http.ResponseWriter, r *http.Request, c *docExtractCaller, kind string) (authz.Resource, bool) {
	res, known := proposedEntityResources[kind]
	if !known {
		fail(w, http.StatusNotFound, msgProposedNotFound)
		return "", false
	}
	d, err := h.check(r.Context(), c, res, authz.ActionCreate)
	if err != nil {
		slog.ErrorContext(r.Context(), "doc extract: permission check", "error", err)
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return "", false
	}
	if !d.Allowed {
		logSecurityEvent(r, "permission_denied", "identity", c.identityID,
			"resource", string(res), "action", string(authz.ActionCreate), "via", viaDocExtractPropose)
		fail(w, http.StatusForbidden, msgProposeDenied)
		return "", false
	}
	return res, true
}

// proposeOrReturn returns the active proposal for in.SourceRef, or builds,
// stores and returns a new one.
func (h *DocExtractOps) proposeOrReturn(w http.ResponseWriter, r *http.Request, c *docExtractCaller, extractionID string, want authz.Resource, in proposedentity.ProposeInput, caller proposedaction.Caller) {
	ctx := r.Context()
	store := h.newActionStore(c.pool)
	existing, found, err := store.FindActiveBySourceRef(ctx, c.identityID, proposedaction.SourceDocExtract, in.SourceRef)
	if err != nil {
		slog.ErrorContext(ctx, "doc extract: find active proposal", "extractionId", extractionID, "error", err)
		fail(w, http.StatusInternalServerError, msgProposeInternal)
		return
	}
	if found {
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "action": existing})
		return
	}
	res, payload, err := h.buildPayload(ctx, c, in)
	if err != nil || res != want {
		h.failPropose(w, r, extractionID, err)
		return
	}
	id, err := store.Propose(ctx, c.pool, proposedaction.Proposal{
		Caller: caller, Kind: proposedaction.KindCreate, Resource: string(res),
		Source: proposedaction.SourceDocExtract, Payload: payload,
	})
	if err != nil {
		h.failPropose(w, r, extractionID, err)
		return
	}
	action, err := store.Get(ctx, c.identityID, id)
	if err != nil {
		h.failPropose(w, r, extractionID, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "action": action})
}

// failPropose maps a build/propose error: a ValidationError is 422 with its
// message (never payload values), anything else is logged and 500.
func (h *DocExtractOps) failPropose(w http.ResponseWriter, r *http.Request, extractionID string, err error) {
	var ve *proposedaction.ValidationError
	if errors.As(err, &ve) {
		fail(w, http.StatusUnprocessableEntity, ve.Message)
		return
	}
	slog.ErrorContext(r.Context(), "doc extract: propose entity", "extractionId", extractionID, "error", err)
	fail(w, http.StatusInternalServerError, msgProposeInternal)
}

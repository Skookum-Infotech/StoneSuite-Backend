package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
	"stonesuite-backend/docextract"
	"stonesuite-backend/docextractjob"
)

const (
	testExtractionID = "not-a-uuid" // keeps RecordCompletion off the (nil) pool
	testRecordUUID   = "99999999-2222-4333-8444-555555555555"
	testCallerUser   = "user-me"
	testOtherUser    = "user-other"
)

var allDocTypes = []docextract.DocType{docextract.DocTypeSalesOrder, docextract.DocTypePurchaseOrder, docextract.DocTypeVendorBill}

// grantOnly makes the env's permission check allow every action on the given
// resources (all scope) and deny everything else.
func grantOnly(e *docExtractEnv, resources ...authz.Resource) {
	e.h.check = func(_ context.Context, _ *docExtractCaller, res authz.Resource, _ authz.Action) (authz.Decision, error) {
		for _, r := range resources {
			if r == res {
				return authz.Decision{Allowed: true, Scope: authz.ScopeAll}, nil
			}
		}
		return authz.Decision{}, nil
	}
}

func TestDocExtract_Create_PerTypePermission(t *testing.T) {
	for _, dt := range allDocTypes {
		own := docExtractResources[dt]
		for _, other := range allDocTypes {
			t.Run(string(dt)+" with "+string(docExtractResources[other]), func(t *testing.T) {
				e := newDocExtractEnv(t)
				grantOnly(e, docExtractResources[other])
				body := `{"docType":"` + string(dt) + `","fileName":"a.pdf","sizeBytes":5,"contentType":"application/pdf"}`
				rec := e.do(e.h.Create, http.MethodPost, "/x", body, "")
				if docExtractResources[other] == own {
					assert.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
					require.Len(t, e.store.created, 1)
					assert.Equal(t, string(dt), e.store.created[0].DocType)
					return
				}
				assert.Equal(t, http.StatusForbidden, rec.Code)
				assert.Empty(t, e.store.created)
			})
		}
	}
}

type completeCase struct {
	name      string
	docType   docextract.DocType
	ownerType docextract.DocType // type the record lookup is asked about
	owner     string
	creator   string
	missing   bool
	wantCode  int
}

func TestDocExtract_Complete_PerType(t *testing.T) {
	var tests []completeCase
	for _, dt := range allDocTypes {
		tests = append(tests, []completeCase{
			{string(dt) + " owner ok", dt, dt, testCallerUser, "", false, http.StatusOK},
			{string(dt) + " other owner", dt, dt, testOtherUser, "", false, http.StatusNotFound},
			{string(dt) + " other owner but caller created it", dt, dt, testOtherUser, testCallerUser, false, http.StatusOK},
			{string(dt) + " other owner and other creator", dt, dt, testOtherUser, testOtherUser, false, http.StatusNotFound},
			{string(dt) + " record missing", dt, dt, "", "", true, http.StatusNotFound},
		}...)
	}
	// A record id that is a live record of a different type is not found for
	// the extraction's type (the per-type loader finds nothing).
	tests = append(tests, completeCase{"wrong type", docextract.DocTypeVendorBill, docextract.DocTypeSalesOrder, testCallerUser, "", false, http.StatusNotFound})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newDocExtractEnv(t)
			grantOnly(e, docExtractResources[tt.docType])
			e.store.rows[testExtractionID] = &docextractjob.Extraction{ID: testExtractionID, DocType: string(tt.docType),
				OwnerIdentityID: testIdentity, Status: docextractjob.StatusReady}
			e.h.callerUserID = func(context.Context, *docExtractCaller) (string, error) { return testCallerUser, nil }
			e.h.audit = func(*http.Request, *docExtractCaller, *docextractjob.Extraction, string) {}
			e.h.recordOwner = func(_ context.Context, _ *docExtractCaller, dt docextract.DocType, _ string) (string, string, error) {
				if tt.missing || dt != tt.ownerType {
					return "", "", errDocRecordNotFound
				}
				return tt.owner, tt.creator, nil
			}
			body := `{"recordUuid":"` + testRecordUUID + `","saved":{}}`
			rec := e.do(e.h.Complete, http.MethodPost, "/x", body, testExtractionID)
			assert.Equal(t, tt.wantCode, rec.Code, rec.Body.String())
		})
	}
}

func TestDocExtract_Complete_Denied403(t *testing.T) {
	e := newDocExtractEnv(t)
	grantOnly(e, authz.ResourceSalesOrder) // no vendor_bill:create
	e.store.rows[testExtractionID] = &docextractjob.Extraction{ID: testExtractionID, DocType: string(docextract.DocTypeVendorBill),
		OwnerIdentityID: testIdentity, Status: docextractjob.StatusReady}
	rec := e.do(e.h.Complete, http.MethodPost, "/x", `{"recordUuid":"`+testRecordUUID+`"}`, testExtractionID)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestDocExtract_List_PerTypeCreatePermissionAndFilter(t *testing.T) {
	rows := []docextractjob.Extraction{
		{ID: "so", DocType: "sales_order"}, {ID: "po", DocType: "purchase_order"}, {ID: "vb", DocType: "vendor_bill"},
	}
	tests := []struct {
		name   string
		grants []authz.Resource
		query  string
		want   []string
		code   int
	}{
		{"bill-only caller sees only bills", []authz.Resource{authz.ResourceVendorBill}, "status=ready", []string{"vb"}, http.StatusOK},
		{"bill filter", []authz.Resource{authz.ResourceVendorBill, authz.ResourceSalesOrder}, "status=ready&docType=vendor_bill", []string{"vb"}, http.StatusOK},
		{"filter on a type without permission", []authz.Resource{authz.ResourceSalesOrder}, "status=ready&docType=purchase_order", []string{}, http.StatusOK},
		{"no filter lists every permitted type", []authz.Resource{authz.ResourcePurchaseOrder, authz.ResourceSalesOrder}, "status=ready", []string{"so", "po"}, http.StatusOK},
		{"unknown docType", []authz.Resource{authz.ResourceSalesOrder}, "status=ready&docType=nope", nil, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newDocExtractEnv(t)
			e.store.ready = rows
			grantOnly(e, tt.grants...)
			rec := e.do(e.h.List, http.MethodGet, "/x?"+tt.query, "", "")
			require.Equal(t, tt.code, rec.Code, rec.Body.String())
			if tt.code != http.StatusOK {
				return
			}
			var resp struct {
				Extractions []docExtractView `json:"extractions"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			got := []string{}
			for _, v := range resp.Extractions {
				got = append(got, v.ID)
			}
			assert.Equal(t, tt.want, got)
			assert.True(t, strings.Contains(rec.Body.String(), `"extractions":[`), "extractions is an array, never null")
		})
	}
}

func TestDocExtract_DuplicateKindsUseTheirOwnResource(t *testing.T) {
	assert.Equal(t, authz.ResourceVendorBill, duplicateResources[docextractjob.DupSameInvoice])
	assert.Equal(t, authz.ResourcePurchaseOrder, duplicateResources[docextractjob.DupSameReference])

	dups := []docextractjob.Duplicate{
		{Kind: docextractjob.DupSameInvoice, RecordUUID: "vb-1", Number: "VB-1", Reason: "dup", OwnerUserID: "mine"},
		{Kind: docextractjob.DupSameReference, RecordUUID: "po-1", Number: "PO-1", Status: "Open", Reason: "dup", OwnerUserID: "mine"},
	}
	onlyBills := func(res authz.Resource, owner string) bool { return res == authz.ResourceVendorBill && owner == "mine" }
	got := filterDuplicates(dups, onlyBills)
	assert.Equal(t, "vb-1", got[0].RecordUUID)
	assert.Empty(t, got[1].RecordUUID)
	assert.Empty(t, got[1].Status)
	assert.Equal(t, docextractjob.GenericReason(docextractjob.DupSameReference), got[1].Reason)
}

func linkedPOResult() docextractjob.ResultDoc {
	return docextractjob.ResultDoc{Resolution: docextractjob.Resolution{LinkedPO: &docextractjob.LinkedPO{
		UUID: testRecordUUID, Number: "PO-7", Status: "PART", Billable: true,
		Lines: []docextractjob.LinkedPOLine{{ID: "l1", LineNumber: 1, BillableQtyMilli: 1000}},
	}}}
}

func TestDocExtract_ScopeResultDoc_LinkedPurchaseOrder(t *testing.T) {
	tests := []struct {
		name      string
		grant     bool
		ownerErr  error
		wantShown bool
	}{
		{"readable", true, nil, true},
		{"no purchase_order:read", false, nil, false},
		{"owner lookup fails closed", true, errDocRecordNotFound, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newDocExtractEnv(t)
			if tt.grant {
				grantOnly(e, authz.ResourcePurchaseOrder)
			} else {
				grantOnly(e, authz.ResourceVendorBill)
			}
			e.h.recordOwner = func(context.Context, *docExtractCaller, docextract.DocType, string) (string, string, error) {
				return testOtherUser, "", tt.ownerErr
			}
			doc := linkedPOResult()
			c, _, _ := e.h.identify(nil)
			e.h.scopeResultDoc(httptest.NewRequest(http.MethodGet, "/x", nil), c, &doc)
			po := doc.Resolution.LinkedPO
			require.NotNil(t, po)
			if tt.wantShown {
				assert.Equal(t, "PO-7", po.Number)
				assert.True(t, po.Billable)
				assert.Len(t, po.Lines, 1)
				return
			}
			assert.Empty(t, po.UUID)
			assert.Empty(t, po.Number)
			assert.Empty(t, po.Status)
			assert.False(t, po.Billable)
			assert.Equal(t, linkedPOHiddenReason, po.Reason)
			assert.NotNil(t, po.Lines)
			assert.Empty(t, po.Lines)
		})
	}
}

func TestNormalizeResultDoc_NeverNullSlices(t *testing.T) {
	for _, dt := range allDocTypes {
		t.Run(string(dt), func(t *testing.T) {
			doc := docextractjob.ResultDoc{Extracted: docextract.Result{DocType: dt}}
			if dt == docextract.DocTypeVendorBill {
				doc.Resolution.LinkedPO = &docextractjob.LinkedPO{}
			}
			normalizeResultDoc(&doc)
			raw, err := json.Marshal(doc)
			require.NoError(t, err)
			var m map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(raw, &m))
			assert.JSONEq(t, `[]`, string(m["duplicates"]))
			var ex, res map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(m["extracted"], &ex))
			require.NoError(t, json.Unmarshal(m["resolution"], &res))
			assert.JSONEq(t, `[]`, string(ex["lines"]))
			assert.JSONEq(t, `[]`, string(res["lines"]))
			if dt == docextract.DocTypeVendorBill {
				var po map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(res["linkedPurchaseOrder"], &po))
				assert.JSONEq(t, `[]`, string(po["lines"]))
			}
		})
	}
}

func TestFilterLinkedPO_UUIDLessReasonNeedsAllScope(t *testing.T) {
	other := func() *docextractjob.LinkedPO {
		return &docextractjob.LinkedPO{Reason: "PO-9 is for another vendor"}
	}
	tests := []struct {
		name       string
		allScope   bool
		wantReason string
	}{
		{"all-scope reader sees the reason", true, "PO-9 is for another vendor"},
		{"own-scope reader gets the generic reason", false, linkedPOHiddenReason},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			canRead := func(_ authz.Resource, owner string) bool { return tt.allScope || owner != "" }
			got := filterLinkedPO(other(), canRead, "", false)
			require.NotNil(t, got)
			assert.Equal(t, tt.wantReason, got.Reason)
			assert.False(t, got.Billable)
			assert.NotNil(t, got.Lines)
		})
	}
	t.Run("not found stays nil", func(t *testing.T) {
		assert.Nil(t, filterLinkedPO(nil, func(authz.Resource, string) bool { return false }, "", false))
	})
}

package docextractjob

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/docextract"
	"stonesuite-backend/services"
)

func TestClassifyVendorDocs(t *testing.T) {
	bill := vendorDocCandidate{uuid: "b1", number: "VBIL-000001", docNumber: "INV-000123", statusName: "Draft", totalCents: 9900, ownerUserID: "7"}
	tests := []struct {
		name       string
		cands      []vendorDocCandidate
		key        string
		in         DuplicateInput
		wantReason string
	}{
		{"same normalized number", []vendorDocCandidate{bill}, "123", DuplicateInput{}, "Possible duplicate of VBIL-000001 (same vendor invoice number)"},
		{"amount as extra signal", []vendorDocCandidate{bill}, "123", DuplicateInput{TotalCents: 9900, HasTotal: true}, "Possible duplicate of VBIL-000001 (same vendor invoice number, same amount)"},
		{"different amount no signal", []vendorDocCandidate{bill}, "123", DuplicateInput{TotalCents: 1, HasTotal: true}, "Possible duplicate of VBIL-000001 (same vendor invoice number)"},
		{"different number", []vendorDocCandidate{bill}, "124", DuplicateInput{}, ""},
		{"no candidates", nil, "123", DuplicateInput{}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyVendorDocs(tc.cands, tc.key, tc.in, false, DupSameInvoice, reasonSameInvoice, reasonSameInvoiceAndAmount)
			if tc.wantReason == "" {
				assert.Empty(t, got)
				return
			}
			require.Len(t, got, 1)
			assert.Equal(t, DupSameInvoice, got[0].Kind)
			assert.Equal(t, "b1", got[0].RecordUUID)
			assert.Equal(t, "7", got[0].OwnerUserID, "owner is carried so the controller can scope the read")
			assert.Equal(t, tc.wantReason, got[0].Reason)
		})
	}
}

func TestClassifyVendorDocs_OwnPONumber(t *testing.T) {
	po := vendorDocCandidate{uuid: "p1", number: "PORD-000123", docNumber: "", statusName: "Draft", ownerUserID: "7"}
	in := DuplicateInput{}
	got := classifyVendorDocs([]vendorDocCandidate{po}, "123", in, true, DupSameReference, reasonSameReference, reasonSameReferenceAmount)
	require.Len(t, got, 1)
	assert.Equal(t, "Possible duplicate of PORD-000123 (same reference number)", got[0].Reason)
	assert.Empty(t, classifyVendorDocs([]vendorDocCandidate{po}, "123", in, false, DupSameInvoice, reasonSameInvoice, reasonSameInvoiceAndAmount),
		"a bill's own number never matches")
	assert.Empty(t, classifyVendorDocs([]vendorDocCandidate{po}, "124", in, true, DupSameReference, reasonSameReference, reasonSameReferenceAmount))
}

func TestGenericReason_VendorKinds(t *testing.T) {
	for _, kind := range []string{DupSameInvoice, DupSameReference} {
		r := GenericReason(kind)
		assert.NotEqual(t, GenericReason("nope"), r, kind)
		assert.NotContains(t, r, "VBIL")
	}
}

func TestPONumberKey(t *testing.T) {
	tests := []struct{ in, want string }{
		{"PORD-000123", "123"}, {"PO-123", "123"}, {"PO # 123", "123"}, {"#000123", "123"},
		{"pord 7", "7"}, {"QT-55", "qt55"}, {"", ""}, {"---", ""},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, poNumberKey(tc.in), tc.in)
	}
}

func TestAnyBillable(t *testing.T) {
	assert.False(t, anyBillable(nil))
	assert.False(t, anyBillable([]LinkedPOLine{{BillableQtyMilli: 0}}))
	assert.True(t, anyBillable([]LinkedPOLine{{}, {BillableQtyMilli: 1}}))
}

func TestResolution_NilListsMarshalAsArrays(t *testing.T) {
	b, err := json.Marshal(LinkedPO{Lines: []LinkedPOLine{}})
	require.NoError(t, err)
	assert.Contains(t, string(b), `"lines":[]`)
	b, err = json.Marshal(ResultDoc{Resolution: Resolution{Lines: []LineMatch{}}, Duplicates: []Duplicate{}})
	require.NoError(t, err)
	assert.Contains(t, string(b), `"duplicates":[]`)
}

func vendorResult(docType docextract.DocType, vendor string) ResultDoc {
	res := feedbackResult("", "", "")
	res.Extracted.DocType = docType
	res.Extracted.Header.VendorName = docextract.Field{Value: vendor}
	res.Extracted.Header.InvoiceNumber = docextract.Field{Value: "INV-1"}
	res.Extracted.Header.InvoiceDate = docextract.Field{Value: "2026-03-04"}
	return res
}

func TestVendorDocLearning(t *testing.T) {
	fuzzy := vendorResult(docextract.DocTypePurchaseOrder, "Acme Supply")
	fuzzy.Resolution.Vendor = PartyMatch{UUID: custA, Confidence: docextract.ConfCheck}
	exact := vendorResult(docextract.DocTypeVendorBill, "Acme Supply")
	exact.Resolution.Vendor = PartyMatch{UUID: custA, Confidence: docextract.ConfHigh}
	noName := vendorResult(docextract.DocTypeVendorBill, "")
	tests := []struct {
		name  string
		res   ResultDoc
		saved SavedValues
		want  bool
	}{
		{"exact vendor match teaches nothing", exact, SavedValues{VendorUUID: custA}, false},
		{"corrected vendor is learned", exact, SavedValues{VendorUUID: custB}, true},
		{"confirmed fuzzy pick is learned", fuzzy, SavedValues{VendorUUID: custA}, true},
		{"unresolved then picked", vendorResult(docextract.DocTypeVendorBill, "Acme"), SavedValues{VendorUUID: custB}, true},
		{"customer uuid is ignored on a vendor doc", exact, SavedValues{CustomerUUID: custB}, false},
		{"no document name", noName, SavedValues{VendorUUID: custB}, false},
		{"invalid uuid", exact, SavedValues{VendorUUID: "nope"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := partyOf(tc.res, tc.saved)
			assert.Equal(t, partyKindVendor, p.kind)
			assert.Equal(t, tc.want, learnParty(p))
		})
	}
	assert.Equal(t, partyKindCustomer, partyOf(feedbackResult(custA, "", ""), SavedValues{CustomerUUID: custA}).kind)
}

func TestDiffFeedback_VendorBill(t *testing.T) {
	res := vendorResult(docextract.DocTypeVendorBill, "Acme Supply")
	res.Resolution.Vendor = PartyMatch{UUID: custA}
	rows := DiffFeedback(res, SavedValues{VendorUUID: custB, InvoiceNumber: "INV-2", InvoiceDate: "2026-03-04", PONumber: "PO-1"})
	var fields []string
	for _, r := range rows {
		fields = append(fields, r.Field)
		assert.Equal(t, custB, r.PartyUUID)
	}
	assert.Equal(t, []string{FieldVendor, FieldInvoiceNumber}, fields)

	po := vendorResult(docextract.DocTypePurchaseOrder, "Acme Supply")
	po.Resolution.Vendor = PartyMatch{UUID: custA}
	rows = DiffFeedback(po, SavedValues{VendorUUID: custA, PONumber: "PO-1", OrderDate: "2026-02-02"})
	require.Len(t, rows, 1)
	assert.Equal(t, FieldOrderDate, rows[0].Field)
}

func TestNotifyReady_PerDocType(t *testing.T) {
	tests := []struct {
		docType  docextract.DocType
		wantLink string
		wantRes  string
		wantBody string
	}{
		{docextract.DocTypeSalesOrder, "/sales/sales_order/new?fromDocument=ex-1", "sales_order", "Review the extracted values and create your sales order."},
		{docextract.DocTypePurchaseOrder, "/purchases/purchase_order/new?fromDocument=ex-1", "purchase_order", "Review the extracted values and create your purchase order."},
		{docextract.DocTypeVendorBill, "/purchases/vendor_bill/new?fromDocument=ex-1", "vendor_bill", "Review the extracted values and create your vendor bill."},
	}
	for _, tc := range tests {
		t.Run(string(tc.docType), func(t *testing.T) {
			var got []services.NotificationRequest
			send := func(_ context.Context, req services.NotificationRequest) error {
				got = append(got, req)
				return nil
			}
			notifyReady(context.Background(), send, "t1", &Extraction{ID: "ex-1", DocType: string(tc.docType), OwnerIdentityID: "u1", FileName: "f.pdf"})
			require.Len(t, got, 1)
			assert.Equal(t, tc.wantLink, got[0].Link)
			assert.Equal(t, tc.wantRes, got[0].Resource)
			assert.Equal(t, tc.wantBody, got[0].Body)
			assert.Equal(t, "u1", got[0].ActorUserID)
		})
	}
}

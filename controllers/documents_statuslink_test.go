package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/docpdf"
)

func TestCustomerSendRequest_CarriesStaffStatusLink(t *testing.T) {
	req := customerSendRequest("t1", "identity-1", DocMeta{WorkflowKey: "invoice", Number: "INV-1"},
		"rec-1", "Subject", docpdf.PrintableDoc{}, "", []string{"a@b.com"}, nil, "INV-1.pdf", []byte("%PDF"))

	assert.Equal(t, recordLink("invoice", "rec-1"), req.StatusLink)
	assert.NotEmpty(t, req.StatusLink, "invoice is a registered global-search resource, so it has a staff route")
	assert.Equal(t, "identity-1", req.ActorUserID)
}

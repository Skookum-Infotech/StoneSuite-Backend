package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/authz"
	"stonesuite-backend/portal"
)

// Staff access to a customer thread follows the DOCUMENT's permission, so this
// mapping is the whole authorization surface of the staff message endpoint. A
// wrong entry here would let someone read an invoice thread with, say, only
// refund permissions.
func TestModuleResourceMapping(t *testing.T) {
	tests := []struct {
		module portal.Module
		want   authz.Resource
		wantOK bool
	}{
		{portal.ModuleSalesOrder, authz.ResourceSalesOrder, true},
		{portal.ModuleInvoice, authz.ResourceInvoice, true},
		{portal.ModulePayment, authz.ResourcePayment, true},
		{portal.ModuleRefund, authz.ResourceRefund, true},
	}
	for _, tt := range tests {
		t.Run(string(tt.module), func(t *testing.T) {
			got, ok := moduleResource(tt.module)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

// Anything outside the four portal modules must fail closed. Falling back to a
// permissive default would let the {module} path segment widen access.
func TestModuleResourceFailsClosed(t *testing.T) {
	for _, bad := range []portal.Module{"quote", "estimate", "credit_memo", "user", "", "*"} {
		got, ok := moduleResource(bad)
		assert.False(t, ok, "module %q must not map to a resource", bad)
		assert.Empty(t, got)
	}
}

// Every portal-visible module must have a staff-side resource, or a customer
// could raise a query on a document type no staff endpoint can read.
func TestEveryPortalModuleHasAStaffResource(t *testing.T) {
	for _, m := range portal.Modules() {
		_, ok := moduleResource(m)
		assert.True(t, ok, "portal module %q has no staff-side resource mapping", m)
	}
}

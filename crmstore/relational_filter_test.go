package crmstore

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/query"
)

func TestRelationalResolver(t *testing.T) {
	r := relationalResolver{}
	cases := []struct {
		key      string
		wantExpr string
		wantDT   query.DataType
		wantOK   bool
	}{
		{"created_at", "c.customer_created_at", query.TypeDate, true},
		{"status", "c.customer_crm_status::text", query.TypeString, true},
		{"id", "c.customer_uuid::text", query.TypeString, true},
		{"core:customer_name", "c.customer_name", query.TypeString, true},                             // kStr
		{"core:customer_is_child", "c.customer_is_child", query.TypeBool, true},                       // kBool
		{"core:customer_credit_limit", "c.customer_credit_limit", query.TypeNumber, true},             // kDec
		{"core:customer_expected_close_date", "c.customer_expected_close_date", query.TypeDate, true}, // kDate
		{"core:customer_type", "c.customer_type::text", query.TypeString, true},                       // kFK
		{"cf:budget", "c.customer_custom_fields->>'budget'", query.TypeString, true},
		{"core:not_a_column", "", "", false},             // not in registry
		{"cf:bad-key", "", "", false},                    // fails identifier regex
		{"cf:x'; DROP TABLE customer;--", "", "", false}, // injection rejected
		{"totally_unknown", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			expr, dt, ok := r.Resolve(c.key)
			assert.Equal(t, c.wantOK, ok)
			assert.Equal(t, c.wantExpr, expr)
			assert.Equal(t, c.wantDT, dt)
		})
	}
}

func TestRelationalResolver_SearchPredicate(t *testing.T) {
	r := relationalResolver{}
	pred := r.SearchPredicate("$1")
	for _, want := range []string{
		"c.customer_doc_num ILIKE",
		"c.customer_name ILIKE",
		"c.customer_dba_name ILIKE",
		"c.customer_contact_email ILIKE",
		"c.customer_primary_phonenum ILIKE",
	} {
		assert.True(t, strings.Contains(pred, want), "search predicate missing %q: %s", want, pred)
	}
}

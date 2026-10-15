package inventory

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlanCatalog_DeduplicatesByKeyKeepingTheFirstSpelling(t *testing.T) {
	plan, err := planCatalog(CatalogRequest{Kits: []CatalogKit{
		{Name: "Kitchen", Items: []string{"Serene", "  serene ", "Flat"}},
		{Name: " kitchen ", Items: []string{"Flat", "Ogee"}},
		{Name: "Island", Items: []string{"Serene"}},
	}})

	require.NoError(t, err)
	assert.Equal(t, []string{"kitchen", "island"}, plan.kitKeys)
	assert.Equal(t, "Kitchen", plan.kitNames["kitchen"])
	assert.Equal(t, []string{"serene", "flat", "ogee"}, plan.itemKeys)
	assert.Equal(t, "Serene", plan.itemNames["serene"])
	assert.Equal(t, []string{"serene", "flat", "ogee"}, plan.members["kitchen"])
	assert.Equal(t, []string{"serene"}, plan.members["island"])
}

func TestPlanCatalog_SkipsBlankItemNames(t *testing.T) {
	plan, err := planCatalog(CatalogRequest{Kits: []CatalogKit{{Name: "Kitchen", Items: []string{"", "  ", "Serene"}}}})

	require.NoError(t, err)
	assert.Equal(t, []string{"serene"}, plan.itemKeys)
}

func TestPlanCatalog_Refusals(t *testing.T) {
	cases := []struct {
		name   string
		req    CatalogRequest
		want   string
		isClnt bool
	}{
		{"blank kit name", CatalogRequest{Kits: []CatalogKit{{Name: "  "}}}, "kit name is required", true},
		{"kit name too long", CatalogRequest{Kits: []CatalogKit{{Name: strings.Repeat("k", MaxKitNameLen+1)}}}, "at most 100", true},
		{"item name too long", CatalogRequest{Kits: []CatalogKit{
			{Name: "Kitchen", Items: []string{strings.Repeat("i", MaxItemNameLen+1)}},
		}}, "at most 150", true},
		{"SKU prefix too long", CatalogRequest{SKUPrefix: strings.Repeat("P", maxSKUPrefixLen+1)}, "SKU prefix", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := planCatalog(tc.req)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Equal(t, tc.isClnt, IsClientError(err))
		})
	}
}

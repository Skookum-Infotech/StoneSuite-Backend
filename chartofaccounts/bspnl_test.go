package chartofaccounts

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeriveBSPNL(t *testing.T) {
	tests := []struct {
		name     string
		subCode  int
		supplied string
		want     string
		wantErr  string
	}{
		// Derived, and anything supplied is ignored.
		{"current assets", 1100, "", "BS", ""},
		{"fixed assets", 1200, "", "BS", ""},
		{"intangible assets", 1300, "", "BS", ""},
		{"current liabilities", 2100, "", "BS", ""},
		{"long-term liabilities", 2200, "", "BS", ""},
		{"equity", 3100, "", "BS", ""},
		{"sales", 4100, "", "PNL", ""},
		{"returns and discounts", 4200, "", "PNL", ""},
		{"cogs", 5100, "", "PNL", ""},
		{"payroll", 6100, "", "PNL", ""},
		{"administrative", 6200, "", "PNL", ""},
		{"sales and marketing", 6300, "", "PNL", ""},
		{"logistics", 6400, "", "PNL", ""},
		{"depreciation", 6500, "", "PNL", ""},
		{"finance costs", 7100, "", "PNL", ""},
		{"other income", 8100, "", "PNL", ""},
		{"supplied value ignored outside 9100", 1100, "PNL", "BS", ""},

		// 9100 is the ONLY sub-category that mixes BS and PNL (AD-2).
		{"system requires an explicit value", 9100, "", "", "required"},
		{"system accepts BS", 9100, "BS", "BS", ""},
		{"system accepts PNL", 9100, "PNL", "PNL", ""},
		{"system rejects nonsense", 9100, "XX", "", "must be"},
		{"system rejects lowercase", 9100, "bs", "", "must be"},

		{"unknown sub-category", 9900, "", "", "9900"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DeriveBSPNL(tt.subCode, tt.supplied)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.True(t, IsClientError(err), "want ClientError, got %T", err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

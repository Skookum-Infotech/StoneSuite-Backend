package chartofaccounts

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeriveBSPNL(t *testing.T) {
	tests := []struct {
		name     string
		side     string
		supplied string
		want     string
		wantErr  string
	}{
		// Derived from the category, and anything supplied is ignored. The
		// cases name the seeded categories whose sides these are, so the table
		// still reads as the chart it encodes now that the codes are gone.
		{"assets", BalanceSheet, "", "BS", ""},
		{"liabilities", BalanceSheet, "", "BS", ""},
		{"equity", BalanceSheet, "", "BS", ""},
		{"revenue", ProfitAndLoss, "", "PNL", ""},
		{"cost of goods sold", ProfitAndLoss, "", "PNL", ""},
		{"operating expenses", ProfitAndLoss, "", "PNL", ""},
		{"finance costs", ProfitAndLoss, "", "PNL", ""},
		{"other income", ProfitAndLoss, "", "PNL", ""},
		{"supplied value ignored outside a MIXED category", BalanceSheet, "PNL", "BS", ""},

		// A MIXED category is the ONLY one whose accounts carry their own side
		// (AD-2); 9000 System & Control is the only seeded one.
		{"mixed requires an explicit value", MixedSide, "", "", "required"},
		{"mixed accepts BS", MixedSide, "BS", "BS", ""},
		{"mixed accepts PNL", MixedSide, "PNL", "PNL", ""},
		{"mixed rejects nonsense", MixedSide, "XX", "", "must be"},
		{"mixed rejects lowercase", MixedSide, "bs", "", "must be"},

		{"unknown side", "SIDE", "", "", "SIDE"},
		{"empty side", "", "", "", "Unknown category side"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DeriveBSPNL(tt.side, tt.supplied)
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

func TestValidCategorySide(t *testing.T) {
	tests := []struct {
		name string
		side string
		want bool
	}{
		{"balance sheet", BalanceSheet, true},
		{"profit and loss", ProfitAndLoss, true},
		{"mixed", MixedSide, true},
		{"empty", "", false},
		{"lowercase", "bs", false},
		{"nonsense", "BOTH", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ValidCategorySide(tt.side))
		})
	}
}

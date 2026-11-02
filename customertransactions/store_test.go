package customertransactions

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name       string
		in         Params
		wantLimit  int
		wantOffset int
		wantPage   int
		wantQuery  string
		wantErr    string // InvalidParamError.Param; "" for none
	}{
		{name: "defaults to the first page", in: Params{}, wantLimit: DefaultLimit, wantPage: 1},
		{name: "page 3 skips two pages", in: Params{Page: 3, Limit: 10}, wantLimit: 10, wantOffset: 20, wantPage: 3},
		{name: "offset uses the clamped size", in: Params{Page: 2, Limit: 5000}, wantLimit: MaxLimit, wantOffset: MaxLimit, wantPage: 2},
		{name: "last allowed page", in: Params{Page: MaxPage, Limit: 1}, wantLimit: 1, wantOffset: MaxPage - 1, wantPage: MaxPage},
		{name: "limit clamps high", in: Params{Limit: 5000}, wantLimit: MaxLimit, wantPage: 1},
		{name: "negative limit takes the default", in: Params{Limit: -3}, wantLimit: DefaultLimit, wantPage: 1},
		{name: "search is trimmed and LIKE metacharacters stripped", in: Params{Search: "  50%_off\\  "}, wantLimit: DefaultLimit, wantPage: 1, wantQuery: "50off"},
		{name: "known type and status", in: Params{Type: "sales_order", Status: "PART"}, wantLimit: DefaultLimit, wantPage: 1},
		{name: "status with underscore", in: Params{Status: "ACT_"}, wantLimit: DefaultLimit, wantPage: 1},
		{name: "equal from and to", in: Params{From: mustDate(t, "2026-05-01"), To: mustDate(t, "2026-05-01")}, wantLimit: DefaultLimit, wantPage: 1},
		{name: "unknown type", in: Params{Type: "lead"}, wantErr: "type"},
		{name: "type injection", in: Params{Type: "invoice; drop"}, wantErr: "type"},
		{name: "lowercase status", in: Params{Status: "sent"}, wantErr: "status"},
		{name: "status too short", in: Params{Status: "S"}, wantErr: "status"},
		{name: "status too long", in: Params{Status: "ABCDEFGHI"}, wantErr: "status"},
		{name: "status injection", in: Params{Status: "A' OR 1=1"}, wantErr: "status"},
		{name: "from after to", in: Params{From: mustDate(t, "2026-06-01"), To: mustDate(t, "2026-05-01")}, wantErr: "from"},
		{name: "negative page", in: Params{Page: -1}, wantErr: "page"},
		{name: "page past the cap", in: Params{Page: MaxPage + 1}, wantErr: "page"},
		{name: "overlong search", in: Params{Search: strings.Repeat("a", maxSearchLen+1)}, wantErr: "q"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, page, err := parse(tt.in)
			if tt.wantErr != "" {
				var invalid *InvalidParamError
				require.ErrorAs(t, err, &invalid)
				assert.Equal(t, tt.wantErr, invalid.Param)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantLimit, f.limit)
			assert.Equal(t, tt.wantOffset, f.offset)
			assert.Equal(t, tt.wantPage, page)
			assert.Equal(t, tt.wantQuery, f.search)
		})
	}
}

func TestNarrowTo(t *testing.T) {
	all := allGranted(t, false)
	assert.Len(t, narrowTo(all, ""), len(all))
	one := narrowTo(all, "invoice")
	require.Len(t, one, 1)
	assert.Equal(t, "invoice", one[0].Key)
	assert.Empty(t, narrowTo([]granted{{Source: mustSource(t, "quote")}}, "invoice"),
		"a type the caller cannot read narrows to nothing, not to an error")
}

func TestResolveCustomer_RejectsMalformedUUID(t *testing.T) {
	for _, in := range []string{"", "not-a-uuid", "1' OR '1'='1", "00000000-0000-0000-0000-00000000000g"} {
		_, err := resolveCustomer(nil, nil, in) //nolint:staticcheck // rejected before the pool is touched
		assert.ErrorIs(t, err, ErrCustomerNotFound, in)
	}
}

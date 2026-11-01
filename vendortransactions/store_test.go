package vendortransactions

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/dateonly"
)

func mustDate(t *testing.T, s string) *dateonly.Date {
	t.Helper()
	d, err := dateonly.Parse(s)
	require.NoError(t, err)
	return &d
}

func TestParse(t *testing.T) {
	cur := cursor{createdAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), id: testUUID}.encode()
	tests := []struct {
		name      string
		in        Params
		wantLimit int
		wantQuery string
		wantAfter bool
		wantErr   string // InvalidParamError.Param; "" for none
	}{
		{name: "defaults", in: Params{}, wantLimit: DefaultLimit},
		{name: "limit kept", in: Params{Limit: 10}, wantLimit: 10},
		{name: "limit clamps high", in: Params{Limit: 5000}, wantLimit: MaxLimit},
		{name: "limit at the cap", in: Params{Limit: MaxLimit}, wantLimit: MaxLimit},
		{name: "negative limit takes the default", in: Params{Limit: -3}, wantLimit: DefaultLimit},
		{name: "search is trimmed and LIKE metacharacters stripped", in: Params{Search: "  50%_off\\  "}, wantLimit: DefaultLimit, wantQuery: "50off"},
		{name: "known type and status", in: Params{Type: "vendor_bill", Status: "PART"}, wantLimit: DefaultLimit},
		{name: "valid cursor", in: Params{Cursor: cur}, wantLimit: DefaultLimit, wantAfter: true},
		{name: "equal from and to", in: Params{From: mustDate(t, "2026-05-01"), To: mustDate(t, "2026-05-01")}, wantLimit: DefaultLimit},
		{name: "unknown type", in: Params{Type: "expense"}, wantErr: "type"},
		{name: "type injection", in: Params{Type: "vendor_bill; drop"}, wantErr: "type"},
		{name: "lowercase status", in: Params{Status: "appv"}, wantErr: "status"},
		{name: "status too long", in: Params{Status: "ABCDEFGHI"}, wantErr: "status"},
		{name: "status injection", in: Params{Status: "A' OR 1=1"}, wantErr: "status"},
		{name: "from after to", in: Params{From: mustDate(t, "2026-06-01"), To: mustDate(t, "2026-05-01")}, wantErr: "from"},
		{name: "garbage cursor", in: Params{Cursor: "garbage"}, wantErr: "cursor"},
		{name: "overlong search", in: Params{Search: strings.Repeat("a", maxSearchLen+1)}, wantErr: "q"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := parse(tt.in)
			if tt.wantErr != "" {
				var invalid *InvalidParamError
				require.ErrorAs(t, err, &invalid)
				assert.Equal(t, tt.wantErr, invalid.Param)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantLimit, f.limit)
			assert.Equal(t, tt.wantQuery, f.search)
			assert.Equal(t, tt.wantAfter, f.after != nil)
		})
	}
}

func TestNarrowTo(t *testing.T) {
	all := allGranted(t, false)
	assert.Len(t, narrowTo(all, ""), len(all))
	one := narrowTo(all, "vendor_bill")
	require.Len(t, one, 1)
	assert.Equal(t, "vendor_bill", one[0].Key)
	assert.Empty(t, narrowTo([]granted{{Source: mustSource(t, "requisition")}}, "vendor_bill"),
		"a type the caller cannot read narrows to nothing, not to an error")
}

func TestResolveVendor_RejectsMalformedUUID(t *testing.T) {
	for _, in := range []string{"", "not-a-uuid", "1' OR '1'='1", "00000000-0000-0000-0000-00000000000g"} {
		_, err := resolveVendor(nil, nil, in) //nolint:staticcheck // rejected before the pool is touched
		assert.ErrorIs(t, err, ErrVendorNotFound, in)
	}
}

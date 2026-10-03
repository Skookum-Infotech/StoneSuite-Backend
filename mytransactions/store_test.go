package mytransactions

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
		wantRole   string
		wantLimit  int
		wantOffset int
		wantPage   int
		wantQuery  string
		wantErr    string // InvalidParamError.Param; "" for none
	}{
		{name: "defaults to the first page", in: Params{}, wantRole: RoleAll, wantLimit: DefaultLimit, wantPage: 1},
		{name: "created", in: Params{Role: "created", Limit: 10}, wantRole: RoleCreated, wantLimit: 10, wantPage: 1},
		{name: "page 1 explicit", in: Params{Page: 1, Limit: 10}, wantRole: RoleAll, wantLimit: 10, wantPage: 1},
		{name: "page 3 skips two pages", in: Params{Page: 3, Limit: 10}, wantRole: RoleAll, wantLimit: 10, wantOffset: 20, wantPage: 3},
		{name: "offset uses the clamped size", in: Params{Page: 2, Limit: 5000}, wantRole: RoleAll, wantLimit: MaxLimit, wantOffset: MaxLimit, wantPage: 2},
		{name: "last allowed page", in: Params{Page: MaxPage, Limit: 1}, wantRole: RoleAll, wantLimit: 1, wantOffset: MaxPage - 1, wantPage: MaxPage},
		{name: "limit clamps high", in: Params{Limit: 5000}, wantRole: RoleAll, wantLimit: MaxLimit, wantPage: 1},
		{name: "negative limit takes the default", in: Params{Limit: -3}, wantRole: RoleAll, wantLimit: DefaultLimit, wantPage: 1},
		{name: "search is trimmed and LIKE metacharacters stripped", in: Params{Search: "  50%_off\\  "}, wantRole: RoleAll, wantLimit: DefaultLimit, wantPage: 1, wantQuery: "50off"},
		{name: "known type", in: Params{Type: "sales_order"}, wantRole: RoleAll, wantLimit: DefaultLimit, wantPage: 1},
		{name: "bad role", in: Params{Role: "everyone"}, wantErr: "role"},
		{name: "unknown type", in: Params{Type: "users; drop"}, wantErr: "type"},
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
			assert.Equal(t, tt.wantRole, f.role)
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

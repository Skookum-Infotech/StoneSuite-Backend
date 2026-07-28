package chartofaccounts

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	uuidA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaa01"
	uuidB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbb02"
	uuidC = "cccccccc-cccc-4ccc-8ccc-cccccccccc03"
)

// TestLockOrderedUUIDs covers the bulk lock-ordering normalisation. The two
// mixed-case cases are the regression: Postgres compares uuid values
// case-insensitively, so before the lowercasing, two concurrent batches naming
// the same rows in different letter case sorted into different orders and
// deadlocked (SQLSTATE 40P01) despite the sort that was supposed to prevent it.
func TestLockOrderedUUIDs(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "empty input",
			in:   nil,
			want: []string{},
		},
		{
			name: "already ordered input is unchanged",
			in:   []string{uuidA, uuidB, uuidC},
			want: []string{uuidA, uuidB, uuidC},
		},
		{
			name: "reversed input sorts to the canonical order",
			in:   []string{uuidC, uuidB, uuidA},
			want: []string{uuidA, uuidB, uuidC},
		},
		{
			name: "exact duplicates collapse to one",
			in:   []string{uuidB, uuidA, uuidB},
			want: []string{uuidA, uuidB},
		},
		{
			name: "case-variant duplicates collapse to one lowercase entry",
			in:   []string{uuidB, upper(uuidB)},
			want: []string{uuidB},
		},
		{
			name: "mixed-case input sorts identically to its lowercase twin",
			in:   []string{upper(uuidB), uuidA},
			want: []string{uuidA, uuidB},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, lockOrderedUUIDs(tt.in))
		})
	}
}

// TestLockOrderedUUIDsIsOrderIndependent is the deadlock proof stated directly:
// two clients naming the same accounts in any order and any letter case must
// produce byte-identical lock orders, because that single global order is the
// only thing preventing a lock cycle between overlapping batches.
func TestLockOrderedUUIDsIsOrderIndependent(t *testing.T) {
	requests := [][]string{
		{uuidA, uuidB},
		{uuidB, uuidA},
		{upper(uuidB), uuidA},
		{uuidA, upper(uuidB)},
		{upper(uuidA), upper(uuidB)},
		{uuidB, uuidA, uuidB},
	}
	want := []string{uuidA, uuidB}
	for _, req := range requests {
		assert.Equal(t, want, lockOrderedUUIDs(req),
			"request %v must lock in the same order as every other request naming the same rows", req)
	}
}

// upper returns s with every ASCII letter uppercased, building the case
// variants Postgres treats as the same uuid.
func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - ('a' - 'A')
		}
	}
	return string(b)
}

package fabrication

import "testing"

func TestFormatNumber(t *testing.T) {
	cases := []struct {
		id   int64
		want string
	}{
		{1, "FJOB-000001"},
		{42, "FJOB-000042"},
		{999999, "FJOB-999999"},
		{1000000, "FJOB-1000000"},
	}
	for _, c := range cases {
		if got := FormatNumber(c.id); got != c.want {
			t.Errorf("FormatNumber(%d) = %q, want %q", c.id, got, c.want)
		}
	}
}

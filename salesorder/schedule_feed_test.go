package salesorder

import (
	"reflect"
	"strings"
	"testing"
)

func TestScheduleGroupCodes(t *testing.T) {
	cases := []struct {
		group string
		want  []string
	}{
		{"Template", []string{"template"}},
		{"WIP Bin Transfer", []string{"wip_bin_transfer"}},
		{"Installation", []string{"installation_ready", "installation_picked", "installation"}},
		{"Created", []string{"created"}},
		{"Invoicing", nil}, // invoices are not part of an order's schedule
		{"template", nil},  // group names match exactly
		{"", nil},
	}
	for _, tc := range cases {
		t.Run(tc.group, func(t *testing.T) {
			if got := scheduleGroupCodes(tc.group); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("scheduleGroupCodes(%q) = %v, want %v", tc.group, got, tc.want)
			}
		})
	}
}

// Every activity belongs to exactly one group, so the union of the groups the
// calendar asks for is the whole catalog with nothing listed twice.
func TestScheduleGroupCodes_PartitionTheCatalog(t *testing.T) {
	seenGroup := map[string]bool{}
	seenCode := map[string]bool{}
	for _, d := range scheduleCatalog {
		if seenGroup[d.Group] {
			continue
		}
		seenGroup[d.Group] = true
		for _, code := range scheduleGroupCodes(d.Group) {
			if seenCode[code] {
				t.Errorf("activity %q is listed under more than one group", code)
			}
			seenCode[code] = true
		}
	}
	if len(seenCode) != len(scheduleCatalog) {
		t.Errorf("groups cover %d activities, the catalog has %d", len(seenCode), len(scheduleCatalog))
	}
}

func TestFeedWindow(t *testing.T) {
	cases := []struct {
		name, from, to string
		wantErr        string // substring; empty = valid
	}{
		{"a single day", "2026-10-12", "2026-10-12", ""},
		{"a week", "2026-10-12", "2026-10-18", ""},
		{"surrounding spaces are trimmed", " 2026-10-12 ", " 2026-10-18 ", ""},
		{"exactly the longest window", "2026-10-01", "2027-01-01", ""}, // 93 days inclusive
		{"one day too long", "2026-10-01", "2027-01-02", "at most"},
		{"reversed", "2026-10-18", "2026-10-12", "before from"},
		{"blank from", "", "2026-10-18", "yyyy-mm-dd"},
		{"blank to", "2026-10-12", "", "yyyy-mm-dd"},
		{"wrong format", "10/12/2026", "10/18/2026", "yyyy-mm-dd"},
		{"not a real date", "2026-02-30", "2026-03-02", "yyyy-mm-dd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, end, err := feedWindow(tc.from, tc.to)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("feedWindow(%q, %q) error: %v", tc.from, tc.to, err)
				}
				if end.Before(start) {
					t.Errorf("window ends %v before it starts %v", end, start)
				}
				return
			}
			if err == nil || !IsClientError(err) || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("feedWindow(%q, %q) error = %v, want a client error containing %q", tc.from, tc.to, err, tc.wantErr)
			}
		})
	}
}

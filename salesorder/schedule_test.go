package salesorder

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

// planCodes returns every catalog activity except Created, in plan order — what
// a first-time plan hands to planSchedule.
func planCodes() []string {
	var codes []string
	for _, d := range scheduleCatalog {
		if d.Code != ActivityCreated {
			codes = append(codes, d.Code)
		}
	}
	return codes
}

func formatPlan(plan []plannedActivity) map[string]string {
	out := make(map[string]string, len(plan))
	for _, p := range plan {
		out[p.code] = p.at.Format("2006-01-02T15:04")
	}
	return out
}

func TestScheduleCatalog_Invariants(t *testing.T) {
	const want = 11
	if len(scheduleCatalog) != want {
		t.Fatalf("catalog has %d activities, want %d", len(scheduleCatalog), want)
	}
	if scheduleCatalog[0].Code != ActivityCreated {
		t.Errorf("first activity = %q, want %q", scheduleCatalog[0].Code, ActivityCreated)
	}
	if got := scheduleCatalog[want-1].Code; got != ActivityInstallation {
		t.Errorf("last activity = %q, want %q", got, ActivityInstallation)
	}
	codeShape := regexp.MustCompile(`^[a-z][a-z_]*$`)
	// Pending / in progress / completed are a row's status, so none of them may
	// appear in the name of an activity.
	statusWord := regexp.MustCompile(`(?i)\b(pending|in progress|completed|scheduled|allocated)\b`)
	seen := map[string]bool{}
	for _, d := range scheduleCatalog {
		if seen[d.Code] {
			t.Errorf("duplicate activity code %q", d.Code)
		}
		seen[d.Code] = true
		if !codeShape.MatchString(d.Code) || len(d.Code) > 40 {
			t.Errorf("activity code %q must be lower_snake and fit VARCHAR(40)", d.Code)
		}
		if d.Group == "" || d.Name == "" {
			t.Errorf("activity %q needs a group and a name", d.Code)
		}
		if statusWord.MatchString(d.Name) || strings.HasSuffix(d.Code, "_pending") ||
			strings.HasSuffix(d.Code, "_in_progress") || strings.HasSuffix(d.Code, "_completed") {
			t.Errorf("activity %q (%q) carries a status word; that belongs in the row's status", d.Code, d.Name)
		}
	}
}

// An order on Oct 12 installing Nov 10 spreads the 10 planned activities evenly
// over the 29 days, with Installation pinned to the installation date.
func TestPlanSchedule_EvenSpreadAcrossTheWindow(t *testing.T) {
	got := formatPlan(planSchedule(planCodes(), day("2026-10-12"), day("2026-11-10")))
	want := map[string]string{
		"template":            "2026-10-15T09:00",
		"material_allocation": "2026-10-18T09:00",
		"engineering":         "2026-10-21T09:00",
		"wip_bin_transfer":    "2026-10-24T09:00",
		"saw":                 "2026-10-27T09:00",
		"edges":               "2026-10-29T09:00",
		"quality_control":     "2026-11-01T09:00",
		"installation_ready":  "2026-11-04T09:00",
		"installation_picked": "2026-11-07T09:00",
		"installation":        "2026-11-10T09:00",
	}
	if len(got) != len(want) {
		t.Fatalf("planned %d activities, want %d", len(got), len(want))
	}
	for code, w := range want {
		if got[code] != w {
			t.Errorf("%s = %s, want %s", code, got[code], w)
		}
	}
}

func TestPlanSchedule_NeverGoesBackwardsAndEndsOnInstallDay(t *testing.T) {
	for _, tc := range []struct {
		name           string
		start, install string
	}{
		{"same day", "2026-10-12", "2026-10-12"},
		{"next day", "2026-10-12", "2026-10-13"},
		{"under a week", "2026-10-12", "2026-10-17"},
		{"exactly one per day", "2026-10-12", "2026-11-03"},
		{"months out", "2026-10-12", "2027-03-30"},
		{"start after install is capped", "2026-10-20", "2026-10-12"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start, install := day(tc.start), day(tc.install)
			if start.After(install) {
				start = install
			}
			plan := planSchedule(planCodes(), day(tc.start), day(tc.install))
			if len(plan) != len(planCodes()) {
				t.Fatalf("planned %d, want %d", len(plan), len(planCodes()))
			}
			prev := plan[0].at
			for _, p := range plan {
				if p.at.Before(prev) {
					t.Errorf("%s at %s is before the previous activity (%s)", p.code, p.at, prev)
				}
				if p.at.Before(start) || p.at.After(install.Add(24*time.Hour)) {
					t.Errorf("%s at %s falls outside %s..%s", p.code, p.at, start.Format("2006-01-02"), install.Format("2006-01-02"))
				}
				prev = p.at
			}
			got := formatPlan(plan)
			inst := install.Format("2006-01-02")
			if got[ActivityInstallation] != inst+"T09:00" {
				t.Errorf("installation = %s, want %sT09:00", got[ActivityInstallation], inst)
			}
		})
	}
}

// A re-plan spreads only what is left, so the first remaining activity is not
// stranded far from today because earlier ones were already done.
func TestPlanSchedule_ReplanSpreadsOnlyTheRemainingActivities(t *testing.T) {
	remaining := []string{"saw", "edges", "quality_control", "installation_picked", ActivityInstallation}
	got := formatPlan(planSchedule(remaining, day("2026-10-12"), day("2026-10-17")))
	// 5 days over 5 activities: one per day, Installation pinned to the last.
	want := map[string]string{
		"saw":                 "2026-10-13T09:00",
		"edges":               "2026-10-14T09:00",
		"quality_control":     "2026-10-15T09:00",
		"installation_picked": "2026-10-16T09:00",
		ActivityInstallation:  "2026-10-17T09:00",
	}
	for code, w := range want {
		if got[code] != w {
			t.Errorf("%s = %s, want %s", code, got[code], w)
		}
	}
}

func TestPlanSchedule_NoCodesPlansNothing(t *testing.T) {
	if got := planSchedule(nil, day("2026-10-12"), day("2026-11-10")); len(got) != 0 {
		t.Errorf("planned %d activities from no codes", len(got))
	}
}

func TestValidateInstallDate(t *testing.T) {
	today := time.Date(2026, 10, 12, 18, 30, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		raw     string
		wantErr string
	}{
		{"today", "2026-10-12", ""},
		{"tomorrow", "2026-10-13", ""},
		{"far future", "2027-01-01", ""},
		{"yesterday is within the timezone grace", "2026-10-11", ""},
		{"two days ago", "2026-10-10", "cannot be in the past"},
		{"last year", "2025-10-12", "cannot be in the past"},
		{"padded with spaces", " 2026-10-20 ", ""},
		{"wrong format", "12/10/2026", "yyyy-mm-dd"},
		{"empty", "", "yyyy-mm-dd"},
		{"impossible date", "2026-02-30", "yyyy-mm-dd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateInstallDate(tc.raw, today)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("want an error containing %q, got none", tc.wantErr)
			case tc.wantErr != "" && !IsClientError(err):
				t.Fatalf("error %T is not a ClientError", err)
			case tc.wantErr != "" && !regexp.MustCompile(tc.wantErr).MatchString(err.Error()):
				t.Fatalf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestResolveUpdateInstallDate(t *testing.T) {
	today := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name         string
		requested    string
		stored       string
		wantSet      bool
		wantChanged  bool
		wantErrMatch string
	}{
		{"blank leaves the stored date alone", "", "2026-11-10", false, false, ""},
		{"blank with nothing stored", "", "", false, false, ""},
		{"same as stored is accepted unchanged", "2026-11-10", "2026-11-10", true, false, ""},
		{"same as stored is accepted even once past", "2026-09-01", "2026-09-01", true, false, ""},
		{"a new future date re-plans", "2026-12-01", "2026-11-10", true, true, ""},
		{"first date on a legacy order", "2026-12-01", "", true, true, ""},
		{"a new past date is refused", "2026-09-01", "2026-11-10", false, false, "cannot be in the past"},
		{"a malformed date is refused", "soon", "2026-11-10", false, false, "yyyy-mm-dd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, set, changed, err := resolveUpdateInstallDate(tc.requested, tc.stored, today)
			if tc.wantErrMatch != "" {
				if err == nil || !regexp.MustCompile(tc.wantErrMatch).MatchString(err.Error()) {
					t.Fatalf("err = %v, want one matching %q", err, tc.wantErrMatch)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if set != tc.wantSet || changed != tc.wantChanged {
				t.Errorf("set=%v changed=%v, want set=%v changed=%v", set, changed, tc.wantSet, tc.wantChanged)
			}
		})
	}
}

func TestPlanOrderDate(t *testing.T) {
	today := time.Date(2026, 10, 12, 22, 15, 0, 0, time.UTC)
	for _, tc := range []struct{ raw, want string }{
		{"", "2026-10-12"},
		{"now", "2026-10-12"},
		{"2026-10-01", "2026-10-01"},
		{"garbage", "2026-10-12"},
	} {
		if got := planOrderDate(tc.raw, today).Format("2006-01-02"); got != tc.want {
			t.Errorf("planOrderDate(%q) = %s, want %s", tc.raw, got, tc.want)
		}
	}
}

func TestParseWallClock(t *testing.T) {
	for _, raw := range []string{"2026-10-13T09:00", "2026-10-13T09:00:00", "2026-10-13 09:00", "2026-10-13 09:00:00"} {
		got, err := parseWallClock(raw)
		if err != nil {
			t.Errorf("parseWallClock(%q): %v", raw, err)
			continue
		}
		if got.Format("2006-01-02T15:04") != "2026-10-13T09:00" {
			t.Errorf("parseWallClock(%q) = %s", raw, got)
		}
	}
	for _, raw := range []string{"", "2026-10-13", "tomorrow", "2026-10-13T25:00"} {
		if _, err := parseWallClock(raw); err == nil {
			t.Errorf("parseWallClock(%q) accepted", raw)
		}
	}
}

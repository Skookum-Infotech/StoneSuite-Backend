package feedback

import (
	"strings"
	"testing"
	"time"
)

func TestFormatTicketNumber(t *testing.T) {
	cases := []struct {
		name string
		seq  int64
		want string
	}{
		{"single digit", 1, "FB-000001"},
		{"zero", 0, "FB-000000"},
		{"six digits", 123456, "FB-123456"},
		{"overflows padding", 1234567, "FB-1234567"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatTicketNumber(tc.seq); got != tc.want {
				t.Errorf("FormatTicketNumber(%d) = %q, want %q", tc.seq, got, tc.want)
			}
		})
	}
}

func TestValidCategory(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"bug", CategoryBug, true},
		{"feature request", CategoryFeatureRequest, true},
		{"ux improvement", CategoryUXImprovement, true},
		{"performance", CategoryPerformance, true},
		{"general", CategoryGeneral, true},
		{"unknown", "student_feedback", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidCategory(tc.in); got != tc.want {
				t.Errorf("ValidCategory(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestValidArea(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"dashboard", AreaDashboard, true},
		{"crm", AreaCRM, true},
		{"sales", AreaSales, true},
		{"purchases", AreaPurchases, true},
		{"inventory", AreaInventory, true},
		{"finance", AreaFinance, true},
		{"configuration", AreaConfiguration, true},
		{"account", AreaAccount, true},
		{"other", AreaOther, true},
		{"empty is valid (unspecified)", "", true},
		{"unknown", "not_a_real_area", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidArea(tc.in); got != tc.want {
				t.Errorf("ValidArea(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestValidStatus(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"new", StatusNew, true},
		{"in progress", StatusInProgress, true},
		{"done", StatusDone, true},
		{"cancelled", StatusCancelled, true},
		{"unknown", "archived", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidStatus(tc.in); got != tc.want {
				t.Errorf("ValidStatus(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestValidPriority(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"low", PriorityLow, true},
		{"normal", PriorityNormal, true},
		{"high", PriorityHigh, true},
		{"urgent", PriorityUrgent, true},
		{"unknown", "critical", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidPriority(tc.in); got != tc.want {
				t.Errorf("ValidPriority(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestValidateCreate(t *testing.T) {
	validRating := 4
	tooLowRating := 0
	tooHighRating := 6

	cases := []struct {
		name    string
		in      CreateInput
		wantErr bool
	}{
		{
			name: "valid staff report",
			in: CreateInput{
				ReporterKind: KindStaff, Category: CategoryBug, Description: "Something broke",
			},
			wantErr: false,
		},
		{
			name: "valid portal report with rating",
			in: CreateInput{
				ReporterKind: KindPortal, Category: CategoryFeatureRequest, Description: "Please add X", Rating: &validRating,
			},
			wantErr: false,
		},
		{
			name: "valid report with area",
			in: CreateInput{
				ReporterKind: KindStaff, Category: CategoryBug, Area: AreaCRM, Description: "Lead form won't save",
			},
			wantErr: false,
		},
		{
			name:    "invalid reporter kind",
			in:      CreateInput{ReporterKind: "admin", Category: CategoryBug, Description: "x"},
			wantErr: true,
		},
		{
			name:    "invalid category",
			in:      CreateInput{ReporterKind: KindStaff, Category: "student_feedback", Description: "x"},
			wantErr: true,
		},
		{
			name:    "invalid area",
			in:      CreateInput{ReporterKind: KindStaff, Category: CategoryBug, Area: "not_a_real_area", Description: "x"},
			wantErr: true,
		},
		{
			name:    "empty description",
			in:      CreateInput{ReporterKind: KindStaff, Category: CategoryBug, Description: ""},
			wantErr: true,
		},
		{
			name:    "whitespace-only description",
			in:      CreateInput{ReporterKind: KindStaff, Category: CategoryBug, Description: "   "},
			wantErr: true,
		},
		{
			name:    "description too long",
			in:      CreateInput{ReporterKind: KindStaff, Category: CategoryBug, Description: strings.Repeat("a", MaxDescriptionLength+1)},
			wantErr: true,
		},
		{
			name:    "rating too low",
			in:      CreateInput{ReporterKind: KindStaff, Category: CategoryBug, Description: "x", Rating: &tooLowRating},
			wantErr: true,
		},
		{
			name:    "rating too high",
			in:      CreateInput{ReporterKind: KindStaff, Category: CategoryBug, Description: "x", Rating: &tooHighRating},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCreate(tc.in)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateCreate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateComment(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{"valid", "Thanks, looking into it.", false},
		{"empty", "", true},
		{"whitespace only", "   ", true},
		{"too long", strings.Repeat("a", MaxCommentLength+1), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateComment(tc.body)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateComment(%q) error = %v, wantErr %v", tc.body, err, tc.wantErr)
			}
		})
	}
}

func TestValidateInternalNotes(t *testing.T) {
	cases := []struct {
		name    string
		notes   string
		wantErr bool
	}{
		{"empty is allowed", "", false},
		{"normal", "Escalated to eng team", false},
		{"too long", strings.Repeat("a", MaxInternalNotesLength+1), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateInternalNotes(tc.notes)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateInternalNotes() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestCursorRoundTrip(t *testing.T) {
	ts := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	id := "11111111-1111-1111-1111-111111111111"

	cursor := encodeCursor(ts, id)
	if cursor == "" {
		t.Fatal("encodeCursor returned empty string")
	}

	gotTS, gotID, err := decodeCursor(cursor)
	if err != nil {
		t.Fatalf("decodeCursor() error = %v", err)
	}
	if !gotTS.Equal(ts) {
		t.Errorf("decodeCursor() ts = %v, want %v", gotTS, ts)
	}
	if gotID != id {
		t.Errorf("decodeCursor() id = %q, want %q", gotID, id)
	}
}

func TestDecodeCursorInvalid(t *testing.T) {
	cases := []struct {
		name   string
		cursor string
	}{
		{"not base64", "not-valid-base64!!!"},
		{"missing separator", "aGVsbG8"},              // base64("hello"), no "|"
		{"bad timestamp", "bm90LWEtdGltZXN0YW1wfGlk"}, // base64("not-a-timestamp|id")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := decodeCursor(tc.cursor); err == nil {
				t.Errorf("decodeCursor(%q) expected error, got nil", tc.cursor)
			}
		})
	}
}

func TestClampLimit(t *testing.T) {
	cases := []struct {
		name  string
		limit int
		want  int
	}{
		{"zero uses default", 0, DefaultLimit},
		{"negative uses default", -5, DefaultLimit},
		{"within range", 50, 50},
		{"over max clamps", MaxLimit + 50, MaxLimit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampLimit(tc.limit); got != tc.want {
				t.Errorf("clampLimit(%d) = %d, want %d", tc.limit, got, tc.want)
			}
		})
	}
}

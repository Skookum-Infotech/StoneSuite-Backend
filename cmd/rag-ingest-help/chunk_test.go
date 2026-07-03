package main

import "testing"

func TestChunkMarkdown(t *testing.T) {
	tests := []struct {
		name          string
		text          string
		fallbackTitle string
		want          []Section
	}{
		{
			name: "single heading",
			text: "# Getting Started\nCreate a lead from CRM > Leads > New.\n",
			want: []Section{
				{Title: "Getting Started", Content: "# Getting Started\nCreate a lead from CRM > Leads > New."},
			},
		},
		{
			name: "multiple headings, mixed levels",
			text: "# Onboarding\nWelcome.\n\n## Step 1\nCreate a tenant.\n\n## Step 2\nInvite users.\n",
			want: []Section{
				{Title: "Onboarding", Content: "# Onboarding\nWelcome."},
				{Title: "Step 1", Content: "## Step 1\nCreate a tenant."},
				{Title: "Step 2", Content: "## Step 2\nInvite users."},
			},
		},
		{
			name:          "no headings at all falls back to one section",
			text:          "Just a plain paragraph with no heading.",
			fallbackTitle: "readme",
			want: []Section{
				{Title: "readme", Content: "Just a plain paragraph with no heading."},
			},
		},
		{
			name:          "empty document produces no sections",
			text:          "",
			fallbackTitle: "empty",
			want:          nil,
		},
		{
			name: "content before first heading is dropped",
			text: "Intro paragraph nobody chunks.\n\n# Real Section\nContent.\n",
			want: []Section{
				{Title: "Real Section", Content: "# Real Section\nContent."},
			},
		},
		{
			name: "heading with no body still produces a section",
			text: "# Empty Section\n\n# Next Section\nHas content.\n",
			want: []Section{
				{Title: "Empty Section", Content: "# Empty Section"},
				{Title: "Next Section", Content: "# Next Section\nHas content."},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ChunkMarkdown(tt.text, tt.fallbackTitle)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d sections, want %d\ngot:  %+v\nwant: %+v", len(got), len(tt.want), got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("section %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

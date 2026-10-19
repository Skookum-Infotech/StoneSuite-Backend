package recordemail

import "testing"

func TestStripQuotedReply(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "gmail attribution on one line",
			in:   "Yes, that works.\n\nOn Tue, Oct 14, 2026 at 3:12 PM Acme Stone <orders@acme.com> wrote:\n> Please confirm\n> the slabs",
			want: "Yes, that works.",
		},
		{
			name: "gmail attribution wrapped across two lines",
			in:   "Confirmed.\n\nOn Tue, Oct 14, 2026 at 3:12 PM Acme Stone Orders Desk <orders@acme-stone-fabrication.com>\nwrote:\n> Please confirm",
			want: "Confirmed.",
		},
		{
			name: "outlook header block",
			in:   "Thanks, see below.\r\n\r\nFrom: Acme Stone <orders@acme.com>\r\nSent: Tuesday, October 14, 2026 3:12 PM\r\nTo: Priya\r\nSubject: SO-1\r\n\r\nPlease confirm",
			want: "Thanks, see below.",
		},
		{
			name: "outlook divider then header",
			in:   "OK\n________________________________\nFrom: Acme <o@acme.com>\nSent: Tuesday\nSubject: SO-1\nbody",
			want: "OK",
		},
		{
			name: "original message divider",
			in:   "Got it\n\n-----Original Message-----\nFrom: x@y.com\nold text",
			want: "Got it",
		},
		{
			name: "plain quoted lines only",
			in:   "Sounds good\n> earlier text\n> more",
			want: "Sounds good",
		},
		{
			name: "no quote at all is untouched",
			in:   "Hello,\n\nCan we move the install to Friday?\n\nThanks",
			want: "Hello,\n\nCan we move the install to Friday?\n\nThanks",
		},
		{
			name: "a sentence starting From: without a Sent line is not a header",
			in:   "From: the warehouse we need two slabs.\nThanks",
			want: "From: the warehouse we need two slabs.\nThanks",
		},
		{
			name: "a sentence starting On is not an attribution",
			in:   "On Friday we can install.\nThanks",
			want: "On Friday we can install.\nThanks",
		},
		{
			name: "reply written entirely inside the quote falls back to the original",
			in:   "> only quoted text",
			want: "> only quoted text",
		},
		{name: "empty", in: "  \n ", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := StripQuotedReply(tc.in); got != tc.want {
				t.Fatalf("StripQuotedReply\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

package services

import "testing"

func TestEmailPlainText(t *testing.T) {
	tests := []struct {
		name string
		in   Email
		want string
	}{
		{
			name: "document send",
			in: Email{
				Heading: "Sales Order SO-1", HeadingAccent: "is on its way.", Badge: "Document Sent",
				Greet: true, RecipientName: "Priya",
				Paragraphs: []Paragraph{
					{Bold("Acme Stone"), Text(" has sent you a sales order "), Link("SO-1", "https://x/y"), Text(". Use the button below.")},
					{Text("Please confirm the slab selection.")},
				},
				Attachment:  &AttachmentCard{FileName: "SO-1.pdf", Size: "245 KB"},
				ActionURL:   "https://x/dl",
				ActionLabel: "Download PDF",
			},
			want: "Hello Priya,\n\nAcme Stone has sent you a sales order SO-1. Use the button below.\n\nPlease confirm the slab selection.\n\nAttachment: SO-1.pdf (245 KB)\n\nDownload PDF: https://x/dl",
		},
		{
			name: "blank recipient greets plainly",
			in:   Email{Greet: true, Paragraphs: []Paragraph{{Text("Hi")}}},
			want: "Hello,\n\nHi",
		},
		{
			name: "details and fine print, no greeting",
			in: Email{
				Details:   []Detail{{Label: "Amount", Value: "$10.00"}, {Label: "Due", Value: "Jan 1"}},
				FinePrint: []string{"Link expires in 7 days."},
			},
			want: "Amount: $10.00\nDue: Jan 1\n\nLink expires in 7 days.",
		},
		{name: "empty", in: Email{}, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.PlainText(); got != tc.want {
				t.Fatalf("PlainText()\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

func TestPlainTextEmailHTML(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"paragraphs and line breaks", "Hi\nthere\n\nBye", "<div><p>Hi<br>there</p><p>Bye</p></div>"},
		{"escapes markup", "<script>x</script> & co", "<div><p>&lt;script&gt;x&lt;/script&gt; &amp; co</p></div>"},
		{"empty", "  ", "<div></div>"},
		{"crlf", "a\r\n\r\nb", "<div><p>a</p><p>b</p></div>"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := PlainTextEmailHTML(tc.in); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

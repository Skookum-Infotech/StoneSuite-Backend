package recordemail

import (
	"encoding/json"
	"strings"
	"testing"
)

// sampleForward is the JSON stonesuite-notify posts for a received reply: the
// field names are the contract between the two services.
const sampleForward = `{
  "emailId": "em_123",
  "from": "Priya Shah <priya@gmail.com>",
  "to": ["r-abcdefghijklmnopqrst@reply.example.com"],
  "cc": ["boss@example.org"],
  "bcc": ["audit@reply.example.com"],
  "subject": "Re: Your Sales Order SO-1",
  "messageId": "<CAF+abc@mail.gmail.com>",
  "inReplyTo": "<sent@resend>",
  "references": "<sent@resend>",
  "text": "Looks good.\n\nOn Thu, Oct 15, 2026 at 9:00 AM Acme <o@acme.com> wrote:\n> hi",
  "html": "<p>Looks good.</p>",
  "attachmentCount": 1
}`

func sampleMail(t *testing.T) InboundMail {
	t.Helper()
	var m InboundMail
	if err := json.Unmarshal([]byte(sampleForward), &m); err != nil {
		t.Fatalf("decode the forwarded mail: %v", err)
	}
	return m
}

func TestInboundMail_DecodesWhatNotifyForwards(t *testing.T) {
	m := sampleMail(t)
	if m.EmailID != "em_123" || m.From != "Priya Shah <priya@gmail.com>" || m.Subject != "Re: Your Sales Order SO-1" {
		t.Fatalf("identity fields wrong: %+v", m)
	}
	if m.MessageID != "<CAF+abc@mail.gmail.com>" || m.InReplyTo != "<sent@resend>" || m.References != "<sent@resend>" {
		t.Fatalf("threading fields wrong: %+v", m)
	}
	if m.HTML != "<p>Looks good.</p>" || m.AttachmentCount != 1 || !strings.HasPrefix(m.Text, "Looks good.") {
		t.Fatalf("content fields wrong: %+v", m)
	}
}

func TestInboundMail_Recipients(t *testing.T) {
	got := sampleMail(t).Recipients()
	want := []string{"r-abcdefghijklmnopqrst@reply.example.com", "boss@example.org", "audit@reply.example.com"}
	if len(got) != len(want) {
		t.Fatalf("Recipients() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Recipients() = %v, want to, cc, bcc in order %v", got, want)
		}
	}
	if len((InboundMail{}).Recipients()) != 0 {
		t.Fatal("a mail with no recipients must have none")
	}
}

func TestBuildMessage(t *testing.T) {
	target := Target{TenantID: "t1", RecordID: "r1", WorkflowKey: "sales_order"}

	t.Run("text body is stripped of quoted history", func(t *testing.T) {
		m := BuildMessage(target, sampleMail(t))
		if m.Direction != DirectionIn || m.Body != "Looks good." {
			t.Fatalf("direction/body = %s / %q", m.Direction, m.Body)
		}
		if m.FromName != "Priya Shah" || m.FromEmail != "priya@gmail.com" {
			t.Fatalf("sender = %q <%q>", m.FromName, m.FromEmail)
		}
		if m.RecordID != "r1" || m.WorkflowKey != "sales_order" || m.ProviderEmailID != "em_123" || m.AttachmentCount != 1 {
			t.Fatalf("bookkeeping wrong: %+v", m)
		}
		if m.InReplyTo != "<sent@resend>" || m.References != "<sent@resend>" || m.MessageID != "<CAF+abc@mail.gmail.com>" {
			t.Fatalf("threading wrong: %+v", m)
		}
		if m.To != "r-abcdefghijklmnopqrst@reply.example.com" || m.CC != "boss@example.org" {
			t.Fatalf("recipients wrong: to=%q cc=%q", m.To, m.CC)
		}
		if m.Subject != "Re: Your Sales Order SO-1" {
			t.Fatalf("subject = %q", m.Subject)
		}
	})

	t.Run("html only mail falls back to text derived from html", func(t *testing.T) {
		m := BuildMessage(target, InboundMail{EmailID: "em_1", HTML: "<div>Hi <b>there</b></div><div>Friday works</div>"})
		if m.Body != "Hi there\nFriday works" {
			t.Fatalf("body = %q", m.Body)
		}
	})

	t.Run("a sender that is not a parseable address is kept as given", func(t *testing.T) {
		m := BuildMessage(target, InboundMail{EmailID: "em_1", From: "  not-an-address  ", Text: "x"})
		if m.FromName != "" || m.FromEmail != "not-an-address" {
			t.Fatalf("sender = %q <%q>", m.FromName, m.FromEmail)
		}
	})

	t.Run("oversized body is truncated", func(t *testing.T) {
		m := BuildMessage(target, InboundMail{EmailID: "em_1", Text: strings.Repeat("a", maxBodyChars+500)})
		if got := len([]rune(m.Body)); got != maxBodyChars {
			t.Fatalf("body length = %d, want %d", got, maxBodyChars)
		}
	})
}

func TestBuildMessage_BoundsSenderAndRecipientFields(t *testing.T) {
	long := strings.Repeat("x", 5000)
	m := BuildMessage(Target{RecordID: "r1", WorkflowKey: "sales_order"}, InboundMail{
		EmailID: "em_1", From: long + " <" + long + "@example.com>", To: []string{long}, CC: []string{long},
		Subject: long, Text: "hi",
	})
	for name, got := range map[string]struct {
		value string
		max   int
	}{
		"fromName":  {m.FromName, maxFromNameChars},
		"fromEmail": {m.FromEmail, maxFromEmailChars},
		"to":        {m.To, maxRecipientsChars},
		"cc":        {m.CC, maxRecipientsChars},
		"subject":   {m.Subject, maxSubjectChars},
	} {
		if n := len([]rune(got.value)); n > got.max {
			t.Errorf("%s is %d chars, want at most %d", name, n, got.max)
		}
	}
}

func TestReplySubjectAndFirstAddress(t *testing.T) {
	for in, want := range map[string]string{
		"Your Sales Order SO-1": "Re: Your Sales Order SO-1",
		"Re: Your Sales Order":  "Re: Your Sales Order",
		"RE: shout":             "RE: shout",
		"":                      "Re:",
	} {
		if got := ReplySubject(in); got != want {
			t.Errorf("ReplySubject(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"a@x.com":                "a@x.com",
		"a@x.com, Bob <b@y.com>": "a@x.com",
		"Bob <b@y.com>, a@x.com": "b@y.com",
		"":                       "",
	} {
		if got := FirstAddress(in); got != want {
			t.Errorf("FirstAddress(%q) = %q, want %q", in, got, want)
		}
	}
}

package recordemail

import (
	"errors"
	"strings"
	"testing"
)

func TestIsBlockedFile(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"setup.exe", true},
		{"SETUP.EXE", true},
		{"invoice.pdf.exe", true},
		{"run.bat", true},
		{"macro.js", true},
		{"disk.iso", true},
		{"shortcut.lnk", true},
		{"invoice.pdf", false},
		{"photo.heic", false},
		{"drawing.dwg", false},
		{"archive.zip", false},
		{"exe", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := IsBlockedFile(tc.name); got != tc.want {
			t.Errorf("IsBlockedFile(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestReplyContentType(t *testing.T) {
	tests := []struct {
		name   string
		want   string
		wantOK bool
	}{
		{"quote.pdf", "application/pdf", true},
		{"Quote.PDF", "application/pdf", true},
		{"photo.jpg", "image/jpeg", true},
		{"photo.jpeg", "image/jpeg", true},
		{"photo.heic", "image/heic", true},
		{"template.dwg", "application/acad", true},
		{"sheet.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", true},
		{"notes.txt", "text/plain", true},
		{"setup.exe", "", false},
		{"page.html", "", false},
		{"image.svg", "", false},
		{"noextension", "", false},
		{"", "", false},
	}
	for _, tc := range tests {
		got, ok := ReplyContentType(tc.name)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("ReplyContentType(%q) = (%q, %v), want (%q, %v)", tc.name, got, ok, tc.want, tc.wantOK)
		}
	}
}

func replyFile(name string, size int) ReplyFile {
	return ReplyFile{Name: name, ContentType: "application/pdf", Data: make([]byte, size)}
}

func TestValidateReplyFiles(t *testing.T) {
	tests := []struct {
		name    string
		files   []ReplyFile
		wantMsg string // "" means accepted
	}{
		{"no files", nil, ""},
		{"one small file", []ReplyFile{replyFile("a.pdf", 10)}, ""},
		{"exactly the file count limit", []ReplyFile{
			replyFile("1.pdf", 1), replyFile("2.pdf", 1), replyFile("3.pdf", 1), replyFile("4.pdf", 1), replyFile("5.pdf", 1),
		}, ""},
		{"one over the file count limit", []ReplyFile{
			replyFile("1.pdf", 1), replyFile("2.pdf", 1), replyFile("3.pdf", 1), replyFile("4.pdf", 1), replyFile("5.pdf", 1), replyFile("6.pdf", 1),
		}, "Attach at most 5 files."},
		{"exactly the total size limit", []ReplyFile{replyFile("a.pdf", ReplyMaxTotalBytes)}, ""},
		{"one byte over the total size limit", []ReplyFile{replyFile("a.pdf", ReplyMaxTotalBytes+1)}, ReplyTooLargeMessage},
		{"total across files over the limit", []ReplyFile{
			replyFile("a.pdf", ReplyMaxTotalBytes/2), replyFile("b.pdf", ReplyMaxTotalBytes/2+1),
		}, ReplyTooLargeMessage},
		{"empty file", []ReplyFile{replyFile("a.pdf", 0)}, "a.pdf is empty."},
		{"nameless file", []ReplyFile{replyFile("  ", 5)}, "A file has no name."},
		{"type not allowed", []ReplyFile{replyFile("setup.exe", 5)}, "setup.exe is not a file type that can be attached."},
		{"no extension", []ReplyFile{replyFile("README", 5)}, "README is not a file type that can be attached."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateReplyFiles(tc.files)
			if tc.wantMsg == "" {
				if err != nil {
					t.Fatalf("ValidateReplyFiles = %v, want accepted", err)
				}
				return
			}
			var fe *ReplyFilesError
			if !errors.As(err, &fe) {
				t.Fatalf("ValidateReplyFiles = %v, want a *ReplyFilesError", err)
			}
			if fe.Msg != tc.wantMsg {
				t.Fatalf("message = %q, want %q", fe.Msg, tc.wantMsg)
			}
		})
	}
}

func TestReplyLimitsAreFiveMegabytesInFiveFiles(t *testing.T) {
	// The numbers the web app mirrors (src/lib/recordEmail.ts): a change here must
	// be made there too.
	if ReplyMaxFiles != 5 || ReplyMaxTotalBytes != 5*1024*1024 {
		t.Fatalf("reply limits = %d files, %d bytes; the web app expects 5 files, 5 MiB", ReplyMaxFiles, ReplyMaxTotalBytes)
	}
}

func TestClassifyReceived(t *testing.T) {
	tests := []struct {
		name     string
		file     string
		size     int64
		canStore bool
		want     string
	}{
		{"an ordinary file waits to be copied", "quote.pdf", 1024, true, AttachmentPending},
		{"exactly the size limit is kept", "big.pdf", InboundMaxFileBytes, true, AttachmentPending},
		{"one byte over the size limit", "big.pdf", InboundMaxFileBytes + 1, true, AttachmentTooLarge},
		{"a program is blocked", "setup.exe", 1024, true, AttachmentBlocked},
		{"blocked wins over too large", "setup.exe", InboundMaxFileBytes + 1, true, AttachmentBlocked},
		{"no storage means unavailable", "quote.pdf", 1024, false, AttachmentUnavailable},
		{"too large is reported even without storage", "big.pdf", InboundMaxFileBytes + 1, false, AttachmentTooLarge},
		{"an unknown size is still copied", "quote.pdf", 0, true, AttachmentPending},
	}
	for _, tc := range tests {
		if got := ClassifyReceived(tc.file, tc.size, tc.canStore); got != tc.want {
			t.Errorf("%s: ClassifyReceived(%q, %d, %v) = %q, want %q", tc.name, tc.file, tc.size, tc.canStore, got, tc.want)
		}
	}
}

func TestReceivedFileName(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"plain", "quote.pdf", "quote.pdf"},
		{"path stripped", "../../etc/passwd", "passwd"},
		{"control characters removed", "a\x00b\nc.pdf", "abc.pdf"},
		{"empty falls back", "", "file"},
	}
	for _, tc := range tests {
		if got := receivedFileName(tc.raw); got != tc.want {
			t.Errorf("%s: receivedFileName(%q) = %q, want %q", tc.name, tc.raw, got, tc.want)
		}
	}

	long := receivedFileName(strings.Repeat("é", 400) + ".pdf")
	if n := len([]rune(long)); n > maxAttachmentNameChars {
		t.Errorf("long name has %d characters, want at most %d", n, maxAttachmentNameChars)
	}
}

func TestInboundMail_ReceivedFilesAndCount(t *testing.T) {
	mail := InboundMail{
		EmailID: "em_1",
		Attachments: []InboundAttachment{
			{ID: "a1", Filename: "one.pdf", ContentType: "application/pdf", Size: 10},
			{ID: "", Filename: "nameless-id.pdf"},
			{ID: "a2", Filename: "logo.png", ContentType: "image/png", Size: 5, Inline: true},
		},
	}

	files := mail.ReceivedFiles()
	if len(files) != 2 || files[0].ProviderID != "a1" || files[1].ProviderID != "a2" || !files[1].Inline {
		t.Fatalf("ReceivedFiles = %+v, want a1 and the inline a2 (an entry with no id dropped)", files)
	}

	target := Target{RecordID: "r", WorkflowKey: "quote"}
	if got := BuildMessage(target, mail).AttachmentCount; got != 3 {
		t.Errorf("AttachmentCount = %d, want 3 (every listed file counts)", got)
	}
	// A count from an older notify, with no list, still counts.
	if got := BuildMessage(target, InboundMail{AttachmentCount: 2}).AttachmentCount; got != 2 {
		t.Errorf("AttachmentCount from a bare count = %d, want 2", got)
	}
}

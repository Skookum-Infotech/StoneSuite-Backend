package recordemail

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"stonesuite-backend/workflow"
)

// The limits of files on a record's Communication thread. They live in one place
// so raising one is a one-line change; the web app mirrors the reply limits in
// src/lib/recordEmail.ts.
const (
	// InboundMaxFileBytes is the largest received file that is kept. A bigger one
	// still shows in the thread, marked as too large.
	InboundMaxFileBytes = 20 << 20

	// MaxRecordAttachmentBytes caps everything kept for one record's conversation,
	// counted apart from the Files tab.
	MaxRecordAttachmentBytes = 200 << 20

	// ReplyMaxFiles and ReplyMaxTotalBytes bound what staff attach to one message.
	// 5 MB in total, for now.
	ReplyMaxFiles      = 5
	ReplyMaxTotalBytes = 5 << 20

	// PendingExpiry is how long a received file may stay "pending" before the
	// thread reports it as unavailable: a copy that never finished (the worker
	// gave up) must not read "Copying…" forever.
	PendingExpiry = 24 * time.Hour

	// maxAttachmentNameChars bounds the file name stored for a received file.
	maxAttachmentNameChars = 255
)

// ReplyTooLargeMessage is what staff are told when a message's files add up to
// more than ReplyMaxTotalBytes.
var ReplyTooLargeMessage = fmt.Sprintf("Attachments are limited to %d MB in total.", ReplyMaxTotalBytes>>20)

// ReplyFilesError is the reason files staff attached cannot be sent. Its message
// is a sentence safe to show.
type ReplyFilesError struct{ Msg string }

func (e *ReplyFilesError) Error() string { return e.Msg }

// blockedExtensions are received files that are never kept: programs and script
// or disk-image formats that exist to be run.
var blockedExtensions = map[string]bool{
	".exe": true, ".msi": true, ".bat": true, ".cmd": true, ".com": true, ".scr": true,
	".js": true, ".jse": true, ".vbs": true, ".vbe": true, ".wsf": true, ".ps1": true,
	".jar": true, ".dll": true, ".lnk": true, ".iso": true, ".hta": true, ".reg": true,
}

// replyContentTypes are the file types staff may attach, with the content type
// sent for each. The type comes from here, never from the browser.
var replyContentTypes = map[string]string{
	".pdf":  "application/pdf",
	".doc":  "application/msword",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xls":  "application/vnd.ms-excel",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".csv":  "text/csv",
	".txt":  "text/plain",
	".ppt":  "application/vnd.ms-powerpoint",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".heic": "image/heic",
	".heif": "image/heif",
	".dwg":  "application/acad",
	".dxf":  "image/vnd.dxf",
	".zip":  "application/zip",
}

func fileExt(name string) string { return strings.ToLower(filepath.Ext(name)) }

// IsBlockedFile reports whether a received file of this name is never kept.
func IsBlockedFile(name string) bool { return blockedExtensions[fileExt(name)] }

// ReplyContentType returns the content type to send a file of this name as, and
// false when staff may not attach that type.
func ReplyContentType(name string) (string, bool) {
	ct, ok := replyContentTypes[fileExt(name)]
	return ct, ok
}

// ReplyFile is one file staff attached to a message: the sanitized name, the
// content type derived from it, and the bytes.
type ReplyFile struct {
	Name        string
	ContentType string
	Data        []byte
}

// ValidateReplyFiles checks the files staff attached to one message against the
// reply limits and the allowed types. The error is a *ReplyFilesError.
func ValidateReplyFiles(files []ReplyFile) error {
	if len(files) > ReplyMaxFiles {
		return &ReplyFilesError{Msg: fmt.Sprintf("Attach at most %d files.", ReplyMaxFiles)}
	}
	var total int64
	for _, f := range files {
		if strings.TrimSpace(f.Name) == "" {
			return &ReplyFilesError{Msg: "A file has no name."}
		}
		if len(f.Data) == 0 {
			return &ReplyFilesError{Msg: fmt.Sprintf("%s is empty.", f.Name)}
		}
		if _, ok := ReplyContentType(f.Name); !ok {
			return &ReplyFilesError{Msg: fmt.Sprintf("%s is not a file type that can be attached.", f.Name)}
		}
		total += int64(len(f.Data))
	}
	if total > ReplyMaxTotalBytes {
		return &ReplyFilesError{Msg: ReplyTooLargeMessage}
	}
	return nil
}

// ClassifyReceived decides what happens to a received file before any byte is
// copied: blocked and oversized files are recorded but not kept, a tenant with no
// file storage cannot keep any, and the rest wait to be copied.
func ClassifyReceived(name string, size int64, canStore bool) string {
	switch {
	case IsBlockedFile(name):
		return AttachmentBlocked
	case size > InboundMaxFileBytes:
		return AttachmentTooLarge
	case !canStore:
		return AttachmentUnavailable
	default:
		return AttachmentPending
	}
}

// receivedFileName is the name shown for a received file: sanitized (no path, no
// control characters), valid UTF-8 (SanitizeFileName may cut a name inside a
// multi-byte character, which the database would refuse) and bounded.
func receivedFileName(raw string) string {
	return BoundFileName(workflow.SanitizeFileName(raw))
}

// BoundFileName makes an already sanitized file name safe to store: valid UTF-8
// and at most maxAttachmentNameChars characters.
func BoundFileName(name string) string {
	name = strings.ToValidUTF8(name, "")
	if r := []rune(name); len(r) > maxAttachmentNameChars {
		name = string(r[:maxAttachmentNameChars])
	}
	if name == "" {
		return "file"
	}
	return name
}

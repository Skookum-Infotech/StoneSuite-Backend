package recordemail

// Attachment states. The vocabulary lives here, not in a database constraint.
const (
	// AttachmentPending is a received file that has not been copied yet.
	AttachmentPending = "pending"
	// AttachmentStored is a file kept in the tenant's bucket and downloadable.
	AttachmentStored = "stored"
	// AttachmentTooLarge and AttachmentBlocked are received files deliberately not
	// kept (over the size limit, or a program-like type).
	AttachmentTooLarge = "too_large"
	AttachmentBlocked  = "blocked"
	// AttachmentUnavailable is a file that could not be kept: the tenant has no
	// file storage, the record is over its cap, or the copy never finished.
	AttachmentUnavailable = "unavailable"
)

// Attachment is one file on a message of a record's conversation. Only the first
// group of fields goes to the browser; where the bytes live never does.
type Attachment struct {
	ID          string `json:"id"`
	FileName    string `json:"fileName"`
	ContentType string `json:"contentType"`
	SizeBytes   int64  `json:"sizeBytes"`
	Inline      bool   `json:"inline"`
	Status      string `json:"status"`

	MessageID            string `json:"-"`
	RecordID             string `json:"-"`
	ProviderAttachmentID string `json:"-"`
	StorageKey           string `json:"-"`
}

// ReceivedFile is the metadata of a file that arrived with a mail, as the mail
// provider describes it (see InboundAttachment).
type ReceivedFile struct {
	ProviderID  string
	Name        string
	ContentType string
	Size        int64
	Inline      bool
}

// InboundAttachment is one file's metadata in the mail stonesuite-notify forwards.
// The bytes follow separately, one request per file, once the backend has said
// which ones it wants (see controllers' InboundAttachment).
type InboundAttachment struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
	Inline      bool   `json:"inline"`
}

// ReceivedFiles converts a forwarded mail's attachment list, dropping entries
// with no provider id (they could never be matched to an upload).
func (m InboundMail) ReceivedFiles() []ReceivedFile {
	out := make([]ReceivedFile, 0, len(m.Attachments))
	for _, a := range m.Attachments {
		if a.ID == "" {
			continue
		}
		out = append(out, ReceivedFile{
			ProviderID: a.ID, Name: a.Filename, ContentType: a.ContentType, Size: a.Size, Inline: a.Inline,
		})
	}
	return out
}

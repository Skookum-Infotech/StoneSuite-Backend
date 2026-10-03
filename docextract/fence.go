// Package docextract holds dependency-free helpers for extracting data from
// untrusted uploaded documents. It imports only the standard library.
package docextract

import "strings"

const (
	// FenceOpen opens the untrusted-document region of an LLM prompt.
	FenceOpen = "<<<DOCUMENT"
	// FenceClose closes the untrusted-document region of an LLM prompt.
	FenceClose = "DOCUMENT>>>"

	neutralOpen  = "<<DOCUMENT"
	neutralClose = "DOCUMENT>>"
)

// InjectionPhrases are lower-case phrases that suggest a document is trying
// to instruct the model rather than just carry data.
var InjectionPhrases = []string{
	"ignore previous",
	"ignore all previous",
	"disregard",
	"system prompt",
	"you are now",
	"set the customer",
	"set customer to",
	"assistant:",
	"### instruction",
}

// FenceDocument wraps untrusted text in fence markers, first neutralising any
// marker occurrences inside the text so the document cannot close the fence.
func FenceDocument(text string) string {
	text = strings.ReplaceAll(text, FenceOpen, neutralOpen)
	text = strings.ReplaceAll(text, FenceClose, neutralClose)
	return FenceOpen + "\n" + text + "\n" + FenceClose
}

// ScanInjection returns the suspicious phrases found in text (case-insensitive).
func ScanInjection(text string) []string {
	lower := strings.ToLower(text)
	var hits []string
	for _, p := range InjectionPhrases {
		if strings.Contains(lower, p) {
			hits = append(hits, p)
		}
	}
	return hits
}

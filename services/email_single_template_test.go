package services

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSingleEmailTemplate_OnlyOneTemplateIsStored guards the architecture: the
// embedded template directory holds exactly one email template.
func TestSingleEmailTemplate_OnlyOneTemplateIsStored(t *testing.T) {
	entries, err := fs.ReadDir(emailTemplateFS, "templates")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "email.html", entries[0].Name())
}

// TestSingleEmailTemplate_NoHTMLDocumentsBuiltInGo fails if any production Go
// file on the email path (one that builds a NotificationRequest) assembles its
// own HTML document instead of supplying Email content for the shared
// template. Non-email HTML pages (e.g. the SAML auto-POST form) are out of scope.
func TestSingleEmailTemplate_NoHTMLDocumentsBuiltInGo(t *testing.T) {
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	skipDirs := map[string]bool{".git": true, ".claude": true, ".superpowers": true, "node_modules": true, "docs": true}

	var offenders []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(src)
		if strings.Contains(text, "NotificationRequest") && strings.Contains(strings.ToLower(text), "<!doctype html") {
			offenders = append(offenders, path)
		}
		return nil
	})
	require.NoError(t, err)
	assert.Empty(t, offenders, "HTML email documents must come from services/templates/email.html only")
}

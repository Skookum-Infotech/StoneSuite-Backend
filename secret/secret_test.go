package secret

import (
	"crypto/rand"
	"encoding/base64"
	"testing"
)

func newTestKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	c, err := New(newTestKey(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cases := []string{
		"",
		"postgres://u:p@host:5432/tenant_acme?sslmode=require",
		"a longer secret with spaces and symbols !@#$%^&*()",
	}
	for _, pt := range cases {
		ct, err := c.Encrypt(pt)
		if err != nil {
			t.Fatalf("Encrypt(%q): %v", pt, err)
		}
		if ct == pt && pt != "" {
			t.Fatalf("ciphertext equals plaintext for %q", pt)
		}
		got, err := c.Decrypt(ct)
		if err != nil {
			t.Fatalf("Decrypt: %v", err)
		}
		if got != pt {
			t.Fatalf("round trip mismatch: got %q want %q", got, pt)
		}
	}
}

func TestNewRejectsBadKey(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("expected error for empty key")
	}
	if _, err := New("not-base64!!"); err == nil {
		t.Fatal("expected error for non-base64 key")
	}
	// 10 bytes is not a valid AES key length.
	short := base64.StdEncoding.EncodeToString(make([]byte, 10))
	if _, err := New(short); err == nil {
		t.Fatal("expected error for wrong key length")
	}
}

func TestDecryptRejectsTampered(t *testing.T) {
	c, err := New(newTestKey(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ct, err := c.Encrypt("secret")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	raw, err := base64.StdEncoding.DecodeString(ct)
	if err != nil {
		t.Fatalf("decode ciphertext: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("ciphertext unexpectedly empty")
	}

	// Deterministically corrupt one byte instead of relying on a
	// string-level flip, which can decode to a still-valid ciphertext.
	raw[len(raw)-1] ^= 0x01
	bad := base64.StdEncoding.EncodeToString(raw)

	if _, err := c.Decrypt(bad); err == nil {
		t.Fatal("expected decryption of tampered ciphertext to fail")
	}
}

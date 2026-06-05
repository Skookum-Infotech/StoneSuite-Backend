// Package secret provides authenticated encryption (AES-256-GCM) for data at
// rest that must not be stored in plaintext — tenant database DSNs and
// per-tenant SSO client secrets held in the control-plane database.
//
// The key comes from config.SecretEncryptionKey as a base64-encoded 32-byte
// value. When no key is configured (local dev), use the plaintext code paths
// instead of this package.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// Cipher encrypts and decrypts short secrets with AES-256-GCM.
type Cipher struct {
	aead cipher.AEAD
}

// New builds a Cipher from a base64-encoded 16/24/32-byte key.
func New(base64Key string) (*Cipher, error) {
	if base64Key == "" {
		return nil, errors.New("encryption key is empty")
	}
	key, err := base64.StdEncoding.DecodeString(base64Key)
	if err != nil {
		return nil, fmt.Errorf("decode key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new aes cipher (key must be 16/24/32 bytes): %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt returns a base64 ciphertext (nonce prepended).
func (c *Cipher) Encrypt(plaintext string) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("read nonce: %w", err)
	}
	ct := c.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ct), nil
}

// Decrypt reverses Encrypt.
func (c *Cipher) Decrypt(b64 string) (string, error) {
	// DEBUG: log ciphertext length
	fmt.Printf("[DECRYPT DEBUG] Input ciphertext length: %d chars\n", len(b64))

	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		fmt.Printf("[DECRYPT DEBUG] base64 decode failed: %v\n", err)
		return "", fmt.Errorf("decode ciphertext: %w", err)
	}
	fmt.Printf("[DECRYPT DEBUG] Decoded %d bytes\n", len(raw))

	ns := c.aead.NonceSize()
	fmt.Printf("[DECRYPT DEBUG] Nonce size: %d, raw size: %d\n", ns, len(raw))

	if len(raw) < ns {
		fmt.Printf("[DECRYPT DEBUG] ERROR: ciphertext too short\n")
		return "", errors.New("ciphertext too short")
	}
	nonce, ct := raw[:ns], raw[ns:]
	fmt.Printf("[DECRYPT DEBUG] Attempting GCM decryption with %d bytes ciphertext\n", len(ct))

	pt, err := c.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		fmt.Printf("[DECRYPT DEBUG] GCM Open failed: %v (type: %T)\n", err, err)
		return "", fmt.Errorf("decrypt: %w", err)
	}
	fmt.Printf("[DECRYPT DEBUG] Successfully decrypted to plaintext (length: %d)\n", len(pt))
	return string(pt), nil
}

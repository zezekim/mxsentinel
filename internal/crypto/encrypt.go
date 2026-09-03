package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Encryptor encrypts/decrypts short credential strings with AES-256-GCM.
// When the key is empty it operates in passthrough mode (stores plaintext).
type Encryptor struct {
	key []byte // 32 bytes; nil = passthrough
}

// NewEncryptor parses a 64-char hex key.
// Returns (enc, isEncrypted, error).
// isEncrypted=false when keyHex is empty (passthrough); error on malformed key.
func NewEncryptor(keyHex string) (*Encryptor, bool, error) {
	if keyHex == "" {
		return &Encryptor{}, false, nil
	}
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return nil, false, fmt.Errorf("crypto: invalid encryption key: %w", err)
	}
	if len(key) != 32 {
		return nil, false, errors.New("crypto: encryption key must be 32 bytes (64 hex chars)")
	}
	return &Encryptor{key: key}, true, nil
}

// Enabled reports whether a key is configured. False means passthrough mode, where Seal
// returns plaintext — callers storing a reversible secret (e.g. the webmail autologin copy
// of an SMTP password) must check this and decline rather than write plaintext to the DB.
// Nil-safe: a nil Encryptor (never wired) reports false.
func (e *Encryptor) Enabled() bool { return e != nil && len(e.key) > 0 }

// Seal encrypts plaintext to "v1:<hex(nonce+ciphertext)>".
// In passthrough mode returns plaintext unchanged.
func (e *Encryptor) Seal(plaintext string) (string, error) {
	if len(e.key) == 0 {
		return plaintext, nil
	}
	block, err := aes.NewCipher(e.key)
	if err != nil {
		return "", fmt.Errorf("crypto: aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("crypto: gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("crypto: nonce: %w", err)
	}
	ct := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return "v1:" + hex.EncodeToString(ct), nil
}

// Open decrypts a value produced by Seal.
// In passthrough mode returns the value unchanged.
func (e *Encryptor) Open(ciphertext string) (string, error) {
	if !strings.HasPrefix(ciphertext, "v1:") {
		if len(e.key) == 0 {
			return ciphertext, nil
		}
		return "", errors.New("crypto: encryption key required to decrypt stored credential")
	}
	if len(e.key) == 0 {
		return "", errors.New("crypto: encryption key required to decrypt stored credential")
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(ciphertext, "v1:"))
	if err != nil {
		return "", fmt.Errorf("crypto: hex decode: %w", err)
	}
	block, err := aes.NewCipher(e.key)
	if err != nil {
		return "", fmt.Errorf("crypto: aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("crypto: gcm: %w", err)
	}
	ns := gcm.NonceSize()
	if len(raw) < ns {
		return "", errors.New("crypto: ciphertext too short")
	}
	pt, err := gcm.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("crypto: decrypt: %w", err)
	}
	return string(pt), nil
}

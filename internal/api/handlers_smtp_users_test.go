package api

import (
	"testing"

	"github.com/zezekim/mxsentinel/internal/crypto"
)

// The sealed password copy is the one reversible secret in the system. It must never be
// written when no encryption key is configured — Encryptor.Seal is a passthrough there, so
// storing its output would put the SMTP password in Postgres in the clear.
func TestSealSMTPPasswordRefusesPassthrough(t *testing.T) {
	const password = "correct-horse-battery"

	t.Run("no key configured", func(t *testing.T) {
		enc, encrypted, err := crypto.NewEncryptor("")
		if err != nil {
			t.Fatalf("NewEncryptor: %v", err)
		}
		if encrypted {
			t.Fatal("an empty key must not report encrypted mode")
		}
		s := &Server{enc: enc}
		got, err := s.sealSMTPPassword(password)
		if err != nil {
			t.Fatalf("sealSMTPPassword: %v", err)
		}
		if got != "" {
			t.Errorf("sealSMTPPassword = %q, want \"\" (store NULL) in passthrough mode", got)
		}
	})

	t.Run("encryptor never wired", func(t *testing.T) {
		s := &Server{} // WithEncryptor was never called
		got, err := s.sealSMTPPassword(password)
		if err != nil {
			t.Fatalf("sealSMTPPassword: %v", err)
		}
		if got != "" {
			t.Errorf("sealSMTPPassword = %q, want \"\" when no encryptor is wired", got)
		}
	})

	t.Run("key configured", func(t *testing.T) {
		enc, encrypted, err := crypto.NewEncryptor("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
		if err != nil {
			t.Fatalf("NewEncryptor: %v", err)
		}
		if !encrypted {
			t.Fatal("a 32-byte key must report encrypted mode")
		}
		s := &Server{enc: enc}
		sealed, err := s.sealSMTPPassword(password)
		if err != nil {
			t.Fatalf("sealSMTPPassword: %v", err)
		}
		if sealed == password {
			t.Fatal("sealSMTPPassword returned the plaintext password")
		}
		opened, err := s.enc.Open(sealed)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if opened != password {
			t.Errorf("round trip = %q, want %q", opened, password)
		}
	})
}

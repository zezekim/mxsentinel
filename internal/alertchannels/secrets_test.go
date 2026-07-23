package alertchannels

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/zezekim/mxsentinel/internal/crypto"
)

// a valid 32-byte hex key for AES-256.
const testKeyHex = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

func TestSealOpenRoundTrip(t *testing.T) {
	enc, encrypted, err := crypto.NewEncryptor(testKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	if !encrypted {
		t.Fatal("expected encryptor to be active")
	}

	raw := []byte(`{"webhook_url":"https://hooks.slack.com/services/secret"}`)
	sealed, err := SealConfig(enc, TypeSlack, raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed), "hooks.slack.com") {
		t.Errorf("sealed config leaked plaintext secret: %s", sealed)
	}

	opened, err := OpenConfig(enc, TypeSlack, sealed)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(opened, &m); err != nil {
		t.Fatal(err)
	}
	if m["webhook_url"] != "https://hooks.slack.com/services/secret" {
		t.Errorf("round-trip mismatch: %v", m["webhook_url"])
	}
}

func TestSealPassthroughNilEncryptor(t *testing.T) {
	raw := []byte(`{"routing_key":"R1"}`)
	sealed, err := SealConfig(nil, TypePagerDuty, raw)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(sealed, &m)
	if m["routing_key"] != "R1" {
		t.Errorf("passthrough should leave value unchanged, got %v", m["routing_key"])
	}
}

func TestWebhookNonSecretFieldNotEncrypted(t *testing.T) {
	enc, _, _ := crypto.NewEncryptor(testKeyHex)
	raw := []byte(`{"url":"https://example.com/hook","signing_secret":"abc"}`)
	sealed, err := SealConfig(enc, TypeWebhook, raw)
	if err != nil {
		t.Fatal(err)
	}
	// url is not a secret -> stays in cleartext; signing_secret is encrypted.
	if !strings.Contains(string(sealed), "https://example.com/hook") {
		t.Errorf("non-secret url should remain in cleartext: %s", sealed)
	}
	if strings.Contains(string(sealed), "\"abc\"") {
		t.Errorf("signing_secret should be encrypted: %s", sealed)
	}
}

func TestRedactConfig(t *testing.T) {
	raw := []byte(`{"webhook_url":"https://hooks.slack.com/x"}`)
	red, err := RedactConfig(TypeSlack, raw)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(red, &m)
	if m["webhook_url"] != "***" {
		t.Errorf("expected redaction to ***, got %v", m["webhook_url"])
	}
}

func TestRedactEmailHasNoSecrets(t *testing.T) {
	raw := []byte(`{"to":["a@b.com"]}`)
	red, err := RedactConfig(TypeEmail, raw)
	if err != nil {
		t.Fatal(err)
	}
	// email carries no secret field, so recipients survive redaction.
	if !strings.Contains(string(red), "a@b.com") {
		t.Errorf("email recipients should not be redacted: %s", red)
	}
}

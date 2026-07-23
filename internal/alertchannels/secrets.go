package alertchannels

import (
	"encoding/json"
	"fmt"

	"github.com/zezekim/mxsentinel/internal/crypto"
)

// secretFields lists the config keys whose values are sensitive per channel type and must
// be encrypted at rest. Email carries no per-channel secret (SMTP creds live at the daemon
// level), so it has no entry.
var secretFields = map[string][]string{
	TypeSlack:     {"webhook_url"},
	TypeWebhook:   {"signing_secret"},
	TypePagerDuty: {"routing_key"},
}

// SecretFields returns the sensitive config keys for a channel type.
func SecretFields(chType string) []string { return secretFields[chType] }

// SealConfig returns a copy of raw config JSON with the type's secret fields encrypted via
// enc. A nil enc (passthrough) leaves values unchanged. Non-string or absent secret fields
// are left as-is. The returned bytes are safe to persist.
func SealConfig(enc *crypto.Encryptor, chType string, raw []byte) ([]byte, error) {
	return transformConfig(raw, secretFields[chType], func(v string) (string, error) {
		if enc == nil {
			return v, nil
		}
		return enc.Seal(v)
	})
}

// OpenConfig returns a copy of raw config JSON with the type's secret fields decrypted via
// enc. A nil enc (passthrough) leaves values unchanged.
func OpenConfig(enc *crypto.Encryptor, chType string, raw []byte) ([]byte, error) {
	return transformConfig(raw, secretFields[chType], func(v string) (string, error) {
		if enc == nil {
			return v, nil
		}
		return enc.Open(v)
	})
}

// RedactConfig returns a copy of raw config JSON with the type's secret fields replaced by
// "***" (or "" if absent). Used for API responses so secrets never leave the server.
func RedactConfig(chType string, raw []byte) ([]byte, error) {
	return transformConfig(raw, secretFields[chType], func(v string) (string, error) {
		if v == "" {
			return "", nil
		}
		return "***", nil
	})
}

// DecodeConfig unmarshals config JSON into a map for a driver.
func DecodeConfig(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

// transformConfig applies fn to each named string field of the JSON object in raw and
// returns the re-marshaled object. Fields that are absent or non-string are left untouched.
func transformConfig(raw []byte, fields []string, fn func(string) (string, error)) ([]byte, error) {
	m, err := DecodeConfig(raw)
	if err != nil {
		return nil, err
	}
	for _, f := range fields {
		s, ok := m[f].(string)
		if !ok || s == "" {
			continue
		}
		nv, err := fn(s)
		if err != nil {
			return nil, fmt.Errorf("transform field %q: %w", f, err)
		}
		m[f] = nv
	}
	out, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}
	return out, nil
}

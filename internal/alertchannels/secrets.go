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
	TypeTelegram:  {"bot_token"},
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
		return RedactedValue, nil
	})
}

// RedactedValue is what RedactConfig substitutes for a set secret. A config posted back by
// the dashboard carries it verbatim, which PreserveRedactedSecrets resolves.
const RedactedValue = "***"

// PreserveRedactedSecrets returns incoming config JSON with every secret field that still
// holds the redaction marker (or is absent) replaced by the value currently stored in
// existing (sealed) config, decrypted via enc. The result is plaintext, ready for
// SealConfig.
//
// Without this, a PATCH that echoes back a channel as the API returned it — which is
// exactly what a dashboard toggle does — would overwrite the Slack webhook URL or the
// Telegram bot token with "***".
func PreserveRedactedSecrets(enc *crypto.Encryptor, chType string, incoming, existing []byte) ([]byte, error) {
	fields := secretFields[chType]
	if len(fields) == 0 {
		return incoming, nil
	}
	prev, err := OpenConfig(enc, chType, existing)
	if err != nil {
		return nil, err
	}
	prevMap, err := DecodeConfig(prev)
	if err != nil {
		return nil, err
	}
	m, err := DecodeConfig(incoming)
	if err != nil {
		return nil, err
	}
	for _, f := range fields {
		v, ok := m[f].(string)
		if ok && v != "" && v != RedactedValue {
			continue // caller supplied a genuinely new secret
		}
		if old, ok := prevMap[f].(string); ok && old != "" {
			m[f] = old
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}
	return out, nil
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

// Package telemetry turns relay (Postfix) maillog lines into structured SMTP events.
//
// PRIVACY BOUNDARY (docs/data-model.md): this package extracts metadata only. It never
// reads or emits message bodies (maillogs don't contain them), stores recipient addresses
// only as a keyed hash plus their domain, and truncates remote-MTA response text.
package telemetry

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// maxResponseText bounds the remote-MTA response we retain (diagnostic, not archival).
const maxResponseText = 512

// hasher hashes recipient addresses so the full address never leaves the relay node.
type hasher struct {
	key []byte
}

// hash returns a stable keyed hash of an email address (lower-cased). Empty in → empty
// out. With no key it falls back to a plain SHA-256 (still non-reversible).
func (h hasher) hash(addr string) string {
	addr = strings.ToLower(strings.TrimSpace(addr))
	if addr == "" {
		return ""
	}
	if len(h.key) > 0 {
		m := hmac.New(sha256.New, h.key)
		_, _ = m.Write([]byte(addr))
		return hex.EncodeToString(m.Sum(nil))
	}
	sum := sha256.Sum256([]byte(addr))
	return hex.EncodeToString(sum[:])
}

// domainOf returns the lower-cased domain part of an email address, or "".
func domainOf(addr string) string {
	addr = strings.ToLower(strings.TrimSpace(addr))
	if i := strings.LastIndexByte(addr, '@'); i >= 0 && i < len(addr)-1 {
		return addr[i+1:]
	}
	return ""
}

// truncate caps s at n bytes.
func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

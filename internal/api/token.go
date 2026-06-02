package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"
)

// API tokens look like:  mxs_<8 hex>_<40 hex>
// The "mxs_<8 hex>" portion is the non-secret prefix (stored and used for lookup); the
// full token is hashed with SHA-256 for storage. Only the hash and prefix are persisted.
const tokenScheme = "mxs"

// GenerateToken returns a new token, its lookup prefix, and the SHA-256 hash to store.
func GenerateToken() (token, prefix, hash string, err error) {
	pb := make([]byte, 4)  // 8 hex chars of prefix
	sb := make([]byte, 20) // 40 hex chars of secret
	if _, err = rand.Read(pb); err != nil {
		return "", "", "", fmt.Errorf("read random: %w", err)
	}
	if _, err = rand.Read(sb); err != nil {
		return "", "", "", fmt.Errorf("read random: %w", err)
	}
	prefix = tokenScheme + "_" + hex.EncodeToString(pb)
	token = prefix + "_" + hex.EncodeToString(sb)
	return token, prefix, HashToken(token), nil
}

// HashToken returns the hex SHA-256 of a token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// PrefixOf extracts the lookup prefix ("mxs_<8 hex>") from a token, or "" if malformed.
func PrefixOf(token string) string {
	parts := strings.SplitN(token, "_", 3)
	if len(parts) != 3 || parts[0] != tokenScheme || parts[1] == "" {
		return ""
	}
	return parts[0] + "_" + parts[1]
}

// tokenMatches reports whether token hashes to the stored hash, in constant time.
func tokenMatches(token, storedHash string) bool {
	return subtle.ConstantTimeCompare([]byte(HashToken(token)), []byte(storedHash)) == 1
}

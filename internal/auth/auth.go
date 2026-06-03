// Package auth provides user authentication primitives for MX Sentinel (Phase 4):
// password hashing, role→scope mapping, and an opaque session-token store. Sessions are
// kept in Redis (revocable, TTL'd); session tokens are distinguishable from API tokens by
// the "mxs_sess_" prefix.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
)

// SessionPrefix marks a token as a user session (vs. an API credential).
const SessionPrefix = "mxs_sess_"

// HashPassword returns a bcrypt hash of pw.
func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(b), nil
}

// CheckPassword reports whether pw matches the bcrypt hash.
func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// ScopesForRole maps a user role to API scopes (see internal/api scope constants).
func ScopesForRole(role string) []string {
	switch role {
	case "owner", "admin":
		return []string{"read", "write", "admin"}
	case "operator":
		return []string{"read", "write"}
	default: // viewer and anything unknown -> read-only
		return []string{"read"}
	}
}

// Session is the authenticated context behind a session token.
type Session struct {
	UserID   string `json:"user_id"`
	TenantID string `json:"tenant_id"`
	Role     string `json:"role"`
}

// SessionStore creates, resolves, and revokes session tokens.
type SessionStore interface {
	Create(ctx context.Context, s Session, ttl time.Duration) (token string, err error)
	Resolve(ctx context.Context, token string) (Session, bool, error)
	Delete(ctx context.Context, token string) error
}

// RedisSessions is a Redis-backed SessionStore.
type RedisSessions struct {
	client *redis.Client
}

// NewRedisSessions builds a session store over the given client.
func NewRedisSessions(client *redis.Client) *RedisSessions {
	return &RedisSessions{client: client}
}

func (r *RedisSessions) Create(ctx context.Context, s Session, ttl time.Duration) (string, error) {
	token, err := newSessionToken()
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	if err := r.client.Set(ctx, sessionKey(token), b, ttl).Err(); err != nil {
		return "", fmt.Errorf("store session: %w", err)
	}
	return token, nil
}

func (r *RedisSessions) Resolve(ctx context.Context, token string) (Session, bool, error) {
	if !strings.HasPrefix(token, SessionPrefix) {
		return Session{}, false, nil
	}
	v, err := r.client.Get(ctx, sessionKey(token)).Result()
	if err == redis.Nil {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, fmt.Errorf("resolve session: %w", err)
	}
	var s Session
	if err := json.Unmarshal([]byte(v), &s); err != nil {
		return Session{}, false, fmt.Errorf("decode session: %w", err)
	}
	return s, true, nil
}

func (r *RedisSessions) Delete(ctx context.Context, token string) error {
	return r.client.Del(ctx, sessionKey(token)).Err()
}

func sessionKey(token string) string { return "sess:" + token }

func newSessionToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return SessionPrefix + hex.EncodeToString(b), nil
}

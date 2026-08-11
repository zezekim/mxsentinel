package api

import (
	"context"
	"time"
)

// AuthInfo is the authenticated caller, resolved from either an API token or a user
// session token. UserID/Role are set only for session (user-login) auth; CredName and
// CredExpiresAt only for API-credential auth.
type AuthInfo struct {
	TenantID string
	CredID   string
	// CredName is the credential's per-tenant name; "" for session auth. Renewal needs it
	// because a renewed credential keeps its name.
	CredName string
	// CredExpiresAt is when the credential stops authenticating; nil means never (or that
	// the caller is a session, which has its own lifecycle).
	CredExpiresAt *time.Time
	UserID        string
	Role          string
	Scopes        []string
}

type ctxKey int

const authKey ctxKey = iota

func withAuth(ctx context.Context, a AuthInfo) context.Context {
	return context.WithValue(ctx, authKey, a)
}

// authFromContext returns the authenticated caller, or ok=false if unauthenticated.
func authFromContext(ctx context.Context) (AuthInfo, bool) {
	a, ok := ctx.Value(authKey).(AuthInfo)
	return a, ok
}

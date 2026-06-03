package api

import "context"

// AuthInfo is the authenticated caller, resolved from either an API token or a user
// session token. UserID/Role are set only for session (user-login) auth.
type AuthInfo struct {
	TenantID string
	CredID   string
	UserID   string
	Role     string
	Scopes   []string
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

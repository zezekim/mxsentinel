package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHasScope(t *testing.T) {
	cases := []struct {
		granted []string
		want    string
		ok      bool
	}{
		{[]string{"read"}, "read", true},
		{[]string{"read"}, "write", false},
		{[]string{"read", "write"}, "write", true},
		{[]string{"admin"}, "write", true}, // admin is a superset
		{[]string{"admin"}, "read", true},
		{nil, "read", false},
	}
	for _, c := range cases {
		if got := hasScope(c.granted, c.want); got != c.ok {
			t.Errorf("hasScope(%v,%q) = %v, want %v", c.granted, c.want, got, c.ok)
		}
	}
}

func TestRequireScope(t *testing.T) {
	s := &Server{}
	hits := 0
	guarded := s.requireScope(ScopeWrite, func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	})

	// Holds write scope -> allowed.
	req := httptest.NewRequest(http.MethodPost, "/v1/x", nil)
	req = req.WithContext(withAuth(req.Context(), AuthInfo{TenantID: "t", Scopes: []string{"read", "write"}}))
	rec := httptest.NewRecorder()
	guarded(rec, req)
	if rec.Code != http.StatusOK || hits != 1 {
		t.Errorf("write-scoped call: code=%d hits=%d", rec.Code, hits)
	}

	// Read-only -> 403, handler not invoked.
	req = httptest.NewRequest(http.MethodPost, "/v1/x", nil)
	req = req.WithContext(withAuth(req.Context(), AuthInfo{TenantID: "t", Scopes: []string{"read"}}))
	rec = httptest.NewRecorder()
	guarded(rec, req)
	if rec.Code != http.StatusForbidden || hits != 1 {
		t.Errorf("read-only call should be 403: code=%d hits=%d", rec.Code, hits)
	}

	// No auth context -> 401.
	rec = httptest.NewRecorder()
	guarded(rec, httptest.NewRequest(http.MethodPost, "/v1/x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated call should be 401: code=%d", rec.Code)
	}
}

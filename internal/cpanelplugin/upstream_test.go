package cpanelplugin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestGetSettingsDecodesWrapper guards the {"settings": {...}} envelope apid returns —
// decoding the fields at the top level silently yields an empty relay_host (the bug that
// made the WHM tool always say "set a relay host").
func TestGetSettingsDecodesWrapper(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/settings" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"settings":{"spf_include":"spf.squidix.net","dkim_selector":"mxs","dmarc_policy":"quarantine","relay_host":"sentinel.squidix.net","relay_port":587}}`))
	}))
	defer srv.Close()

	u := newUpstream(Config{APIBase: srv.URL, Token: "t", VerifySSL: true})
	s, err := u.GetSettings(context.Background())
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if s.RelayHost != "sentinel.squidix.net" {
		t.Fatalf("relay_host not decoded from wrapper: got %q", s.RelayHost)
	}
	if s.RelayPort != 587 {
		t.Fatalf("relay_port: got %d", s.RelayPort)
	}
	if s.DKIMSelector != "mxs" {
		t.Fatalf("dkim_selector: got %q", s.DKIMSelector)
	}
}

// TestCreateSMTPUserDecodesTopLevel confirms the create response (not wrapped) decodes.
func TestCreateSMTPUserDecodesTopLevel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"abc","username":"u@h","domain":"h","enabled":true}`))
	}))
	defer srv.Close()

	u := newUpstream(Config{APIBase: srv.URL, Token: "t"})
	su, err := u.CreateSMTPUser(context.Background(), "u@h", "pw12345678", "h")
	if err != nil {
		t.Fatalf("CreateSMTPUser: %v", err)
	}
	if su.ID != "abc" || su.Username != "u@h" {
		t.Fatalf("unexpected smtp user: %+v", su)
	}
}

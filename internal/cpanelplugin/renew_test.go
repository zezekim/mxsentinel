package cpanelplugin

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	oldToken = "mxs_aaaaaaaa_1111111111111111111111111111111111111111"
	newToken = "mxs_bbbbbbbb_2222222222222222222222222222222222222222"
)

// fakeAPI stands in for apid's /v1/me and /v1/apikeys/renew.
type fakeAPI struct {
	mu          sync.Mutex
	meStatus    int
	meBody      string
	renewStatus int
	renewBody   string
	meCalls     int
	renewCalls  int
}

func (f *fakeAPI) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/me":
			f.meCalls++
			w.WriteHeader(f.meStatus)
			_, _ = io.WriteString(w, f.meBody)
		case "/v1/apikeys/renew":
			f.renewCalls++
			w.WriteHeader(f.renewStatus)
			_, _ = io.WriteString(w, f.renewBody)
		default:
			http.NotFound(w, r)
		}
	})
}

func (f *fakeAPI) counts() (me, renew int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.meCalls, f.renewCalls
}

// meBody renders a /v1/me payload expiring in d. A nil d omits expires_at entirely,
// which is how apid reports a credential that never expires.
func meBody(d *time.Duration) string {
	base := `"tenant_id":"t1","scopes":["read","relay"],"user_id":"","role":"","credential_name":"cpanel-host.example.com"`
	if d == nil {
		return "{" + base + "}"
	}
	return fmt.Sprintf(`{%s,"expires_at":%q}`, base, time.Now().Add(*d).UTC().Format(time.RFC3339))
}

func renewBody(token string) string {
	return fmt.Sprintf(`{"id":"k1","name":"cpanel-host.example.com","token":%q,"prefix":"mxs_bbbbbbbb","scopes":["read","relay"],"expires_at":%q}`,
		token, time.Now().Add(365*24*time.Hour).UTC().Format(time.RFC3339))
}

func days(n int) *time.Duration {
	d := time.Duration(n) * 24 * time.Hour
	return &d
}

// writeTestConfig lays down a realistic plugin.conf and returns its path.
func writeTestConfig(t *testing.T, apiBase, token string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plugin.conf")
	body := "# MX Sentinel plugin config\napi_base = " + apiBase + "\ntoken = " + token + "\nverify_ssl = true\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// testPolicy compresses every production interval so the loop tests finish in ms.
func testPolicy() renewPolicy {
	return renewPolicy{
		interval:    20 * time.Millisecond,
		threshold:   30 * 24 * time.Hour,
		firstJitter: time.Millisecond,
		retryBase:   10 * time.Millisecond,
		retryMax:    50 * time.Millisecond,
		writeRetry:  10 * time.Millisecond,
		deadBackoff: 30 * time.Second,
		timeout:     5 * time.Second,
	}
}

func newTestBroker(t *testing.T, apiBase, confPath string) *Broker {
	t.Helper()
	cfg := Config{APIBase: apiBase, Token: oldToken, VerifySSL: true, Path: confPath}
	return &Broker{
		cfg:   cfg,
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		up:    newUpstream(cfg),
		renew: testPolicy(),
	}
}

func TestRenewOnce(t *testing.T) {
	cases := []struct {
		name string
		api  *fakeAPI
		// badConfigPath points the broker at an unwritable config so the persist step fails.
		badConfigPath bool
		want          renewOutcome
		wantRenews    int
		wantSwapped   bool
	}{
		{
			name:        "renews when near expiry",
			api:         &fakeAPI{meStatus: 200, meBody: meBody(days(10)), renewStatus: 200, renewBody: renewBody(newToken)},
			want:        renewOK,
			wantRenews:  1,
			wantSwapped: true,
		},
		{
			name:       "does not renew when far from expiry",
			api:        &fakeAPI{meStatus: 200, meBody: meBody(days(300)), renewStatus: 200, renewBody: renewBody(newToken)},
			want:       renewOK,
			wantRenews: 0,
		},
		{
			name:       "stops permanently when the credential never expires",
			api:        &fakeAPI{meStatus: 200, meBody: meBody(nil), renewStatus: 200, renewBody: renewBody(newToken)},
			want:       renewStop,
			wantRenews: 0,
		},
		{
			name:          "does not swap the in-memory token when the config write fails",
			api:           &fakeAPI{meStatus: 200, meBody: meBody(days(3)), renewStatus: 200, renewBody: renewBody(newToken)},
			badConfigPath: true,
			want:          renewWriteFailed,
			wantRenews:    1,
			wantSwapped:   false,
		},
		{
			name:       "retries when /v1/me is down",
			api:        &fakeAPI{meStatus: 500, meBody: `{"error":"boom"}`},
			want:       renewRetry,
			wantRenews: 0,
		},
		{
			name:       "retries when the renew call 500s",
			api:        &fakeAPI{meStatus: 200, meBody: meBody(days(5)), renewStatus: 500, renewBody: `{"error":"boom"}`},
			want:       renewRetry,
			wantRenews: 1,
		},
		{
			name:       "treats a 401 on the expiry check as dead",
			api:        &fakeAPI{meStatus: 401, meBody: `{"error":"unauthorized"}`},
			want:       renewDead,
			wantRenews: 0,
		},
		{
			name:       "treats a 401 on renew as dead",
			api:        &fakeAPI{meStatus: 200, meBody: meBody(days(5)), renewStatus: 401, renewBody: `{"error":"unauthorized"}`},
			want:       renewDead,
			wantRenews: 1,
		},
		{
			name:       "treats a 400 on renew as dead",
			api:        &fakeAPI{meStatus: 200, meBody: meBody(days(5)), renewStatus: 400, renewBody: `{"error":"bad_request","message":"credential does not expire"}`},
			want:       renewDead,
			wantRenews: 1,
		},
		{
			name:       "retries when the server returns no token",
			api:        &fakeAPI{meStatus: 200, meBody: meBody(days(5)), renewStatus: 200, renewBody: `{"id":"k1","name":"n","prefix":"mxs_bbbbbbbb"}`},
			want:       renewRetry,
			wantRenews: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := tc.api
			srv := httptest.NewServer(api.handler())
			defer srv.Close()

			confPath := writeTestConfig(t, srv.URL, oldToken)
			b := newTestBroker(t, srv.URL, confPath)
			if tc.badConfigPath {
				b.cfg.Path = filepath.Join(filepath.Dir(confPath), "no-such-dir", "plugin.conf")
			}

			if got := b.renewOnce(context.Background()); got != tc.want {
				t.Fatalf("renewOnce = %d, want %d", got, tc.want)
			}
			if _, renews := api.counts(); renews != tc.wantRenews {
				t.Fatalf("renew calls = %d, want %d", renews, tc.wantRenews)
			}

			wantTok := oldToken
			if tc.wantSwapped {
				wantTok = newToken
			}
			if got := b.up.Token(); got != wantTok {
				t.Fatalf("in-memory token swapped incorrectly: got %q, want %q", tokenPrefix(got), tokenPrefix(wantTok))
			}

			// The persisted token must track the in-memory one, never lead or lag it.
			if !tc.badConfigPath {
				cfg, err := LoadConfig(confPath)
				if err != nil {
					t.Fatalf("reload config: %v", err)
				}
				if cfg.Token != wantTok {
					t.Fatalf("persisted token = %q, want %q", tokenPrefix(cfg.Token), tokenPrefix(wantTok))
				}
			}
		})
	}
}

// TestRenewOnceWriteFailureKeepsDiskAndMemoryInSync is the ordering guarantee: a failed
// persist must leave BOTH copies on the old token, so a restart mid-failure still comes
// up with a credential the server accepts for the rest of its grace window.
func TestRenewOnceWriteFailureKeepsDiskAndMemoryInSync(t *testing.T) {
	api := &fakeAPI{meStatus: 200, meBody: meBody(days(2)), renewStatus: 200, renewBody: renewBody(newToken)}
	srv := httptest.NewServer(api.handler())
	defer srv.Close()

	confPath := writeTestConfig(t, srv.URL, oldToken)
	before, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatal(err)
	}

	b := newTestBroker(t, srv.URL, confPath)
	b.cfg.Path = filepath.Join(t.TempDir(), "missing", "plugin.conf")

	if got := b.renewOnce(context.Background()); got != renewWriteFailed {
		t.Fatalf("renewOnce = %d, want renewWriteFailed", got)
	}
	after, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("config was modified despite a failed write")
	}
	if b.up.Token() != oldToken {
		t.Fatalf("token swapped in memory while the disk copy is stale")
	}
}

func TestRunTokenRenewalStopsWhenCredentialNeverExpires(t *testing.T) {
	api := &fakeAPI{meStatus: 200, meBody: meBody(nil)}
	srv := httptest.NewServer(api.handler())
	defer srv.Close()

	b := newTestBroker(t, srv.URL, writeTestConfig(t, srv.URL, oldToken))

	done := make(chan struct{})
	go func() { defer close(done); b.runTokenRenewal(context.Background()) }()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not stop for a credential with no expiry")
	}
	if me, _ := api.counts(); me != 1 {
		t.Fatalf("/v1/me calls = %d, want exactly 1 (the loop must not keep asking)", me)
	}
}

func TestRunTokenRenewalStopsOnContextCancel(t *testing.T) {
	api := &fakeAPI{meStatus: 200, meBody: meBody(days(300))}
	srv := httptest.NewServer(api.handler())
	defer srv.Close()

	b := newTestBroker(t, srv.URL, writeTestConfig(t, srv.URL, oldToken))
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() { defer close(done); b.runTokenRenewal(ctx) }()

	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not stop on context cancellation")
	}
}

// TestRunTokenRenewalBacksOffOnUnauthorized guards the "never hammer" rule: a 401 cannot
// be retried into success, so the loop must go quiet instead of spinning on apid.
func TestRunTokenRenewalBacksOffOnUnauthorized(t *testing.T) {
	api := &fakeAPI{meStatus: 401, meBody: `{"error":"unauthorized"}`}
	srv := httptest.NewServer(api.handler())
	defer srv.Close()

	b := newTestBroker(t, srv.URL, writeTestConfig(t, srv.URL, oldToken))
	// deadBackoff dominates the ~10ms retry ladder, so a correct loop calls once.
	p := testPolicy()
	p.deadBackoff = time.Minute
	b.renew = p

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); b.runTokenRenewal(ctx) }()

	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	if me, _ := api.counts(); me != 1 {
		t.Fatalf("/v1/me calls = %d, want 1 — a dead token must back off hard, not retry on the fast ladder", me)
	}
}

// TestRunTokenRenewalRetriesAfterTransientFailure proves a 500 is retried (and that the
// retry succeeds once the server recovers).
func TestRunTokenRenewalRetriesAfterTransientFailure(t *testing.T) {
	api := &fakeAPI{meStatus: 500, meBody: `{"error":"boom"}`, renewStatus: 200, renewBody: renewBody(newToken)}
	srv := httptest.NewServer(api.handler())
	defer srv.Close()

	confPath := writeTestConfig(t, srv.URL, oldToken)
	b := newTestBroker(t, srv.URL, confPath)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); b.runTokenRenewal(ctx) }()

	time.Sleep(40 * time.Millisecond)
	api.mu.Lock()
	api.meStatus, api.meBody = 200, meBody(days(3))
	api.mu.Unlock()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && b.up.Token() != newToken {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	if b.up.Token() != newToken {
		t.Fatalf("loop never recovered from the transient failure")
	}
	if me, _ := api.counts(); me < 2 {
		t.Fatalf("/v1/me calls = %d, want at least 2 (one failure + one retry)", me)
	}
}

func TestBackoffDelay(t *testing.T) {
	p := renewPolicy{retryBase: 5 * time.Minute, retryMax: 6 * time.Hour}
	for _, tc := range []struct {
		fails int
		want  time.Duration
	}{
		{1, 5 * time.Minute},
		{2, 10 * time.Minute},
		{3, 20 * time.Minute},
		{8, 6 * time.Hour},
		{100, 6 * time.Hour},
	} {
		if got := backoffDelay(p, tc.fails); got != tc.want {
			t.Fatalf("backoffDelay(%d) = %s, want %s", tc.fails, got, tc.want)
		}
	}
}

func TestTokenPrefixNeverLeaksTheSecret(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{oldToken, "mxs_aaaaaaaa"},
		{"garbage", "unknown"},
		{"", "unknown"},
	} {
		got := tokenPrefix(tc.in)
		if got != tc.want {
			t.Fatalf("tokenPrefix(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if tc.in != "" && strings.HasSuffix(tc.in, got) && len(tc.in) > len(got) && strings.Contains(got, "1111") {
			t.Fatalf("prefix leaked secret material: %q", got)
		}
	}
}

// --- config rewriting -------------------------------------------------------------
// These live here rather than in a config_test.go because WriteToken exists solely for
// the renewal path.

// odd on purpose: comments, blank lines, both separator forms, ragged spacing, a key that
// merely starts with "token", and no trailing newline.
const fixtureConf = `# MX Sentinel cPanel plugin configuration.

api_base = https://api.example.com
token   =   mxs_aaaaaaaa_1111111111111111111111111111111111111111

# verify the TLS cert
verify_ssl false
token_note = not the token
   socket_path = /run/mxsentinel-plugin/api.sock`

func TestWriteTokenRewritesOnlyTheTokenLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugin.conf")
	if err := os.WriteFile(path, []byte(fixtureConf), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := WriteToken(path, newToken); err != nil {
		t.Fatalf("WriteToken: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	gotLines := strings.Split(string(got), "\n")
	wantLines := strings.Split(fixtureConf, "\n")
	if len(gotLines) != len(wantLines) {
		t.Fatalf("line count changed: %d -> %d\n%s", len(wantLines), len(gotLines), got)
	}
	for i := range wantLines {
		if strings.HasPrefix(strings.TrimSpace(wantLines[i]), "token ") || strings.HasPrefix(strings.TrimSpace(wantLines[i]), "token=") {
			continue // the one line allowed to change
		}
		if gotLines[i] != wantLines[i] {
			t.Fatalf("line %d changed:\n old: %q\n new: %q", i+1, wantLines[i], gotLines[i])
		}
	}
	if strings.Contains(string(got), oldToken) {
		t.Fatal("old token still present in the config")
	}
	if strings.HasSuffix(string(got), "\n") {
		t.Fatal("a trailing newline was added to a file that had none")
	}

	// Everything LoadConfig cares about must survive, including the bare "key value" form.
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Token != newToken {
		t.Fatalf("token = %q, want the new one", tokenPrefix(cfg.Token))
	}
	if cfg.APIBase != "https://api.example.com" {
		t.Fatalf("api_base = %q", cfg.APIBase)
	}
	if cfg.VerifySSL {
		t.Fatal("verify_ssl (bare-space form) was lost")
	}
	if cfg.SocketPath != "/run/mxsentinel-plugin/api.sock" {
		t.Fatalf("socket_path = %q", cfg.SocketPath)
	}
}

func TestWriteTokenPreservesModeAndLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plugin.conf")
	if err := os.WriteFile(path, []byte("api_base = https://a\ntoken = "+oldToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := WriteToken(path, newToken); err != nil {
		t.Fatalf("WriteToken: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600 — the token must not become readable", fi.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "plugin.conf" {
		t.Fatalf("temp file left behind: %v", entries)
	}
}

func TestWriteTokenAppendsWhenNoTokenLineExists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugin.conf")
	if err := os.WriteFile(path, []byte("# no token here\napi_base = https://a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteToken(path, newToken); err != nil {
		t.Fatalf("WriteToken: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Token != newToken {
		t.Fatalf("token = %q, want the appended one", tokenPrefix(cfg.Token))
	}
}

func TestWriteTokenRewritesDuplicateTokenLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugin.conf")
	body := "api_base = https://a\ntoken = " + oldToken + "\nToken " + oldToken + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteToken(path, newToken); err != nil {
		t.Fatalf("WriteToken: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), oldToken) {
		t.Fatalf("a duplicate token line kept the old secret:\n%s", got)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Token != newToken {
		t.Fatalf("token = %q", tokenPrefix(cfg.Token))
	}
}

func TestWriteTokenRejectsEmptyToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugin.conf")
	if err := os.WriteFile(path, []byte("token = "+oldToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteToken(path, "  "); err == nil {
		t.Fatal("WriteToken accepted an empty token")
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), oldToken) {
		t.Fatal("config was clobbered by a rejected write")
	}
}

// TestUpstreamTokenIsRaceSafe fails under -race if the renewal goroutine's swap can
// interleave with a request reading the token. Run: go test -race ./internal/cpanelplugin/...
func TestUpstreamTokenIsRaceSafe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"settings":{"relay_host":"r"}}`)
	}))
	defer srv.Close()

	u := newUpstream(Config{APIBase: srv.URL, Token: oldToken})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				_, _ = u.GetSettings(context.Background())
				_, _ = u.CreateSMTPUser(context.Background(), "u", "p", "d")
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 200; j++ {
			u.SetToken(newToken)
			u.SetToken(oldToken)
		}
	}()
	wg.Wait()
}

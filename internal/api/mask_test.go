package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

type fakeAliasStore struct {
	mu   sync.Mutex
	seq  int
	seen map[string]int
	fail bool
}

func newFakeStore() *fakeAliasStore {
	return &fakeAliasStore{seen: make(map[string]int)}
}

func (f *fakeAliasStore) ListViewerAliases(context.Context) ([]AliasRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []AliasRow
	for h, s := range f.seen {
		out = append(out, AliasRow{RealHost: h, Seq: s})
	}
	return out, nil
}

func (f *fakeAliasStore) AssignViewerAlias(_ context.Context, host string) (int, error) {
	if f.fail {
		return 0, errors.New("database down")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.seen[host]; ok {
		return s, nil
	}
	f.seq++
	f.seen[host] = f.seq
	return f.seq, nil
}

func testMasker(t *testing.T, store aliasStore) *Masker {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m, err := NewMasker(store, log, "mxsentinel.app", []string{"squidix.net", "squidix.org", "squidix.com", "srvon.com"})
	if err != nil {
		t.Fatalf("NewMasker: %v", err)
	}
	if m == nil {
		t.Fatal("NewMasker returned nil for a non-empty suffix list")
	}
	return m
}

// The whole point of the feature: nothing recognisable survives.
func TestMaskLeavesNoProviderTrace(t *testing.T) {
	m := testMasker(t, newFakeStore())
	body := `{"from_domain":"ocean.squidix.net","sasl_username":"cpanel-relay@server4736.squidix.net",
	  "relay_host":"sentinel.squidix.net","spf_include":"spf.squidix.net","dmarc_rua":"dmarc@squidix.org",
	  "title":"DNS validation issues for sentinel.squidix.net (1 finding(s))",
	  "second":"cloud1.srvon.com","note":"escalate to Squidix support","apex":"squidix.com"}`

	got := string(m.Mask(context.Background(), []byte(body)))
	for _, bad := range []string{"squidix", "Squidix", "SQUIDIX", "srvon"} {
		if strings.Contains(strings.ToLower(got), strings.ToLower(bad)) {
			t.Errorf("masked body still contains %q:\n%s", bad, got)
		}
	}
}

// A viewer must see the same server under the same name everywhere, or the dashboard
// stops making sense — that is the reason aliases are persisted rather than randomised.
func TestMaskIsStableAndOneToOne(t *testing.T) {
	m := testMasker(t, newFakeStore())
	ctx := context.Background()

	first := string(m.Mask(ctx, []byte(`"ocean.squidix.net"`)))
	second := string(m.Mask(ctx, []byte(`{"a":"ocean.squidix.net"}`)))
	if !strings.Contains(second, strings.Trim(first, `"`)) {
		t.Errorf("same host aliased differently across responses: %q then %q", first, second)
	}

	a := string(m.Mask(ctx, []byte(`"ocean.squidix.net"`)))
	b := string(m.Mask(ctx, []byte(`"sentinel.squidix.net"`)))
	if a == b {
		t.Errorf("distinct hosts collapsed to the same alias: %q", a)
	}
}

// Message-IDs embed a per-message counter in front of the host. Aliasing the whole run
// would mint a row per message and grow the table without bound.
func TestMaskBoundsAliasKeyToTheHost(t *testing.T) {
	store := newFakeStore()
	m := testMasker(t, store)
	ctx := context.Background()

	for _, id := range []string{
		`"<248.sentinel.squidix.net>"`,
		`"<240.sentinel.squidix.net>"`,
		`"<246.sentinel.squidix.net>"`,
	} {
		if got := string(m.Mask(ctx, []byte(id))); strings.Contains(got, "squidix") {
			t.Fatalf("message-id not masked: %s", got)
		}
	}

	store.mu.Lock()
	n := len(store.seen)
	store.mu.Unlock()
	if n != 1 {
		t.Errorf("three message-ids for one host minted %d aliases, want 1: %v", n, store.seen)
	}
}

// The mailbox part is not provider-identifying and is what makes the row useful.
func TestMaskKeepsEmailLocalPart(t *testing.T) {
	m := testMasker(t, newFakeStore())
	got := string(m.Mask(context.Background(), []byte(`"cpanel-relay@ocean.squidix.net"`)))
	if !strings.Contains(got, "cpanel-relay@") {
		t.Errorf("local part lost, row is no longer useful: %s", got)
	}
}

// Unrelated hostnames must pass through untouched, or the viewer's own client data breaks.
func TestMaskLeavesOtherDomainsAlone(t *testing.T) {
	m := testMasker(t, newFakeStore())
	body := `{"a":"gmail.com","b":"bounce.nytimes.com","c":"fulllineexhaust.com","d":"notsquidix.example.com"}`
	got := string(m.Mask(context.Background(), []byte(body)))
	for _, keep := range []string{"gmail.com", "bounce.nytimes.com", "fulllineexhaust.com"} {
		if !strings.Contains(got, keep) {
			t.Errorf("rewrote an unrelated domain %q: %s", keep, got)
		}
	}
}

// A database outage must degrade to a different alias, never to the real hostname.
func TestMaskFailsClosedWhenTheStoreIsDown(t *testing.T) {
	store := newFakeStore()
	store.fail = true
	m := testMasker(t, store)

	got := string(m.Mask(context.Background(), []byte(`"ocean.squidix.net"`)))
	if strings.Contains(got, "squidix") {
		t.Errorf("store failure leaked the real hostname: %s", got)
	}
	if got != string(m.Mask(context.Background(), []byte(`"ocean.squidix.net"`))) {
		t.Errorf("fallback alias is not stable: %s", got)
	}
}

func TestMaskAppliesOnlyToViewers(t *testing.T) {
	m := testMasker(t, newFakeStore())
	for _, role := range []string{"owner", "admin", "operator", ""} {
		if m.AppliesTo(role) {
			t.Errorf("role %q should not be masked", role)
		}
	}
	if !m.AppliesTo("viewer") {
		t.Error("viewer should be masked")
	}
	var nilMasker *Masker
	if nilMasker.AppliesTo("viewer") {
		t.Error("a disabled masker must not claim to apply")
	}
}

func TestNewMaskerDisabledWithoutSuffixes(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m, err := NewMasker(newFakeStore(), log, "mxsentinel.app", []string{"", "  "})
	if err != nil {
		t.Fatalf("NewMasker: %v", err)
	}
	if m != nil {
		t.Error("no configured suffixes should disable masking, got a masker")
	}
	if out := m.Mask(context.Background(), []byte("ocean.squidix.net")); string(out) != "ocean.squidix.net" {
		t.Errorf("disabled masker rewrote the body: %s", out)
	}
}

func TestMaskableContentType(t *testing.T) {
	for _, ct := range []string{"application/json", "application/json; charset=utf-8", "text/csv", "", "application/problem+json"} {
		if !maskableContentType(ct) {
			t.Errorf("content type %q should be masked", ct)
		}
	}
	for _, ct := range []string{"image/png", "application/octet-stream", "font/woff2"} {
		if maskableContentType(ct) {
			t.Errorf("content type %q should be passed through", ct)
		}
	}
}

// The provider name also appears welded into identifiers that are not hostnames, where a
// word-bounded pattern never fires. Both cases below were live leaks found by scanning
// the real API after the first version of this masker shipped.
func TestMaskCatchesBrandInsideIdentifiers(t *testing.T) {
	m := testMasker(t, newFakeStore())
	cases := map[string]string{
		"seznam DMARC report id": `"szn_squidix.com-2026-07-23"`,
		"report recipient":       `"squidixtest@gmail.com"`,
		"underscore prefix":      `"relay_squidix.net_node"`,
		"no separator":           `"mailsquidixnet"`,
	}
	for name, body := range cases {
		got := strings.ToLower(string(m.Mask(context.Background(), []byte(body))))
		if strings.Contains(got, "squidix") || strings.Contains(got, "srvon") {
			t.Errorf("%s: leaked through: %s", name, got)
		}
	}
}

func TestSameHost(t *testing.T) {
	same := [][2]string{
		{"sentinel.example.net", "sentinel.example.net"},
		{"Sentinel.Example.Net", "sentinel.example.net"},
		{"sentinel.example.net:443", "sentinel.example.net"},
		{"sentinel.example.net.", "sentinel.example.net"},
	}
	for _, c := range same {
		if !sameHost(c[0], c[1]) {
			t.Errorf("sameHost(%q, %q) = false, want true", c[0], c[1])
		}
	}
	diff := [][2]string{
		{"control.other.app", "sentinel.example.net"},
		{"", "sentinel.example.net"},
		{"evil-sentinel.example.net", "sentinel.example.net"},
	}
	for _, c := range diff {
		if sameHost(c[0], c[1]) {
			t.Errorf("sameHost(%q, %q) = true, want false", c[0], c[1])
		}
	}
}

package cpanelplugin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureLocal is a trimmed but representative /etc/exim.conf.local with the section
// marker tokens cPanel ships.
const fixtureLocal = `####################################################################
# This file allows custom changes to exim.conf. Edit with care.
####################################################################

@CONFIG@

@LOCALOPTS@

@AUTH@

@ACL@

@ROUTERSGLOBAL@

@PREROUTERS@

@ROUTERSTART@

@POSTMAILCOUNT@

@TRANSPORTSTART@

@AUTHS_PLAINTEXT@
`

func TestInsertRemoveRoundTrip(t *testing.T) {
	blocks := smarthostBlocks("relay.example.com", 587, "mailer@send.example.com", "s3cret-pass")

	inserted, err := insertBlocks(fixtureLocal, blocks)
	if err != nil {
		t.Fatalf("insertBlocks: %v", err)
	}
	if !hasBlocks(inserted) {
		t.Fatal("expected blocks present after insert")
	}

	// Each block must land immediately after its marker line.
	assertBlockAfterMarker(t, inserted, markerRouter, "mxsentinel_smarthost:")
	assertBlockAfterMarker(t, inserted, markerTransport, "mxsentinel_smtp:")
	assertBlockAfterMarker(t, inserted, markerAuth, "mxsentinel_login:")

	// Credential must be present in the authenticator.
	if !strings.Contains(inserted, "client_send = : mailer@send.example.com : s3cret-pass") {
		t.Fatal("authenticator missing client_send credential")
	}

	// Removing must restore the original bytes exactly.
	if got := removeBlocks(inserted); got != fixtureLocal {
		t.Fatalf("round trip not byte-identical:\n--- want ---\n%q\n--- got ---\n%q", fixtureLocal, got)
	}
}

func TestInsertIdempotent(t *testing.T) {
	blocks := smarthostBlocks("relay.example.com", 587, "u", "p")
	once, err := insertBlocks(fixtureLocal, blocks)
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}
	twice, err := insertBlocks(once, blocks)
	if err != nil {
		t.Fatalf("second insert: %v", err)
	}
	if once != twice {
		t.Fatal("re-applying blocks was not idempotent")
	}
	// Exactly one managed region.
	if n := strings.Count(twice, sentinelBegin); n != 3 {
		t.Fatalf("expected 3 BEGIN sentinels (router/transport/auth), got %d", n)
	}
}

func TestInsertMissingMarkerFails(t *testing.T) {
	noMarkers := "@CONFIG@\n@ACL@\n"
	if _, err := insertBlocks(noMarkers, smarthostBlocks("h", 587, "u", "p")); err == nil {
		t.Fatal("expected error when a section marker is missing")
	}
}

func TestRemoveBlocksNoOpWhenAbsent(t *testing.T) {
	if got := removeBlocks(fixtureLocal); got != fixtureLocal {
		t.Fatal("removeBlocks altered content with no managed regions")
	}
}

// assertBlockAfterMarker checks that the first non-sentinel line after the marker is the
// expected stanza header.
func assertBlockAfterMarker(t *testing.T, content, marker, wantHeader string) {
	t.Helper()
	lines := strings.Split(content, "\n")
	idx := indexOfLine(lines, marker)
	if idx < 0 {
		t.Fatalf("marker %q not found", marker)
	}
	if got := strings.TrimSpace(lines[idx+1]); got != sentinelBegin {
		t.Fatalf("after %q expected %q, got %q", marker, sentinelBegin, got)
	}
	if got := strings.TrimSpace(lines[idx+2]); got != wantHeader {
		t.Fatalf("after %q expected stanza %q, got %q", marker, wantHeader, got)
	}
}

// TestMutateRollbackOnBadConfig verifies the load-bearing safety guarantee: if the rebuilt
// config fails validation, the overlay is restored to its original bytes (mail keeps flowing).
func TestMutateRollbackOnBadConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "exim.conf.local")
	if err := os.WriteFile(path, []byte(fixtureLocal), 0o640); err != nil {
		t.Fatal(err)
	}

	// Runner that fails `exim -bV` whenever the overlay currently contains our blocks —
	// i.e. the new (managed) config is rejected, but the original validates fine.
	runner := func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if strings.Contains(name, "exim") {
			cur, _ := os.ReadFile(path)
			if hasBlocks(string(cur)) {
				return []byte("bad config"), errFakeValidation
			}
		}
		return []byte("ok"), nil
	}
	m := &eximManager{localPath: path, run: runner}

	err := m.apply(context.Background(), "relay.example.com", 587, "u", "passw0rd")
	if err == nil {
		t.Fatal("expected apply to fail when validation rejects the new config")
	}
	got, _ := os.ReadFile(path)
	if string(got) != fixtureLocal {
		t.Fatalf("overlay was not rolled back to original:\n%q", string(got))
	}
}

func TestMutateAppliesAndRestarts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "exim.conf.local")
	if err := os.WriteFile(path, []byte(fixtureLocal), 0o640); err != nil {
		t.Fatal(err)
	}
	sr := &stubRunner{}
	m := &eximManager{localPath: path, run: sr.run}

	if err := m.apply(context.Background(), "relay.example.com", 587, "u", "passw0rd"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if en, _ := m.enabled(); !en {
		t.Fatal("expected enabled() true after apply")
	}
	joined := strings.Join(sr.calls, "|")
	for _, want := range []string{buildEximConf, "-bV", restartExim} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected %q to be run; calls=%v", want, sr.calls)
		}
	}

	if err := m.remove(context.Background()); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if en, _ := m.enabled(); en {
		t.Fatal("expected enabled() false after remove")
	}
}

var errFakeValidation = &stubErr{"validation failed"}

type stubErr struct{ s string }

func (e *stubErr) Error() string { return e.s }

// stubRunner records invoked commands and returns canned results.
type stubRunner struct {
	calls []string
}

func (s *stubRunner) run(_ context.Context, name string, args ...string) ([]byte, error) {
	s.calls = append(s.calls, strings.TrimSpace(name+" "+strings.Join(args, " ")))
	return []byte("ok"), nil
}

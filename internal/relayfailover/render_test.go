package relayfailover

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRenderSmarthost_SASLKeyMatchesNexthop(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "failover-domains")

	if err := RenderSmarthost(dir, "relay.mailbaby.net", 587, "user@x", "s3cret"); err != nil {
		t.Fatal(err)
	}

	sasl, err := os.ReadFile(filepath.Join(dir, SASLFileName))
	if err != nil {
		t.Fatal(err)
	}
	// The exact bug the installer had: the key must be [host]:port (port OUTSIDE the brackets)
	// so Postfix finds it by the transport nexthop.
	wantSASL := "[relay.mailbaby.net]:587 user@x:s3cret\n"
	if string(sasl) != wantSASL {
		t.Fatalf("sasl = %q, want %q", sasl, wantSASL)
	}

	trans, err := os.ReadFile(filepath.Join(dir, TransportFileName))
	if err != nil {
		t.Fatal(err)
	}
	wantTrans := "FALLBACK_TRANSPORT=relay-mailbaby:[relay.mailbaby.net]:587\n"
	if string(trans) != wantTrans {
		t.Fatalf("transport = %q, want %q", trans, wantTrans)
	}

	// SASL file must be 0600 (contains a plaintext password).
	fi, err := os.Stat(filepath.Join(dir, SASLFileName))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("sasl perm = %o, want 600", perm)
	}
	_ = statePath
}

func TestRenderSmarthost_ClearsWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := RenderSmarthost(dir, "h", 587, "u", "p"); err != nil {
		t.Fatal(err)
	}
	// Now clear it (disabled / no creds).
	if err := RenderSmarthost(dir, "", 0, "", ""); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{SASLFileName, TransportFileName} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s should have been removed, err=%v", name, err)
		}
	}
}

func TestRenderSmarthost_DefaultsPort(t *testing.T) {
	dir := t.TempDir()
	if err := RenderSmarthost(dir, "h.example", 0, "u", "p"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, SASLFileName))
	if want := "[h.example]:587 u:p\n"; string(b) != want {
		t.Fatalf("got %q want %q", b, want)
	}
}

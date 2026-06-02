package dns

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"math/big"
	"net"
	"testing"

	"github.com/zezekim/mxsentinel/pkg/contracts"
)

func codes(fs []contracts.DNSFinding) map[string]string {
	m := map[string]string{}
	for _, f := range fs {
		m[f.Code] = f.Severity
	}
	return m
}

func mustNotHave(t *testing.T, fs []contracts.DNSFinding, code string) {
	t.Helper()
	if _, ok := codes(fs)[code]; ok {
		t.Errorf("did not expect finding %s; got %v", code, codes(fs))
	}
}

func mustHave(t *testing.T, fs []contracts.DNSFinding, code, wantSev string) {
	t.Helper()
	sev, ok := codes(fs)[code]
	if !ok {
		t.Fatalf("expected finding %s (sev %s); got %v", code, wantSev, codes(fs))
	}
	if wantSev != "" && sev != wantSev {
		t.Errorf("finding %s severity = %s, want %s", code, sev, wantSev)
	}
}

// ---- SPF -------------------------------------------------------------------

func TestSPFMissing(t *testing.T) {
	r := NewStaticResolver()
	_, fs := evaluateSPF(context.Background(), r, "example.com")
	mustHave(t, fs, CodeSPFMissing, SevWarning)
}

func TestSPFMultiple(t *testing.T) {
	r := NewStaticResolver()
	r.TXTRecords["example.com"] = []string{"v=spf1 -all", "v=spf1 include:x.com -all"}
	_, fs := evaluateSPF(context.Background(), r, "example.com")
	mustHave(t, fs, CodeSPFMultiple, SevCritical)
}

func TestSPFLookupLimit(t *testing.T) {
	r := NewStaticResolver()
	rec := "v=spf1"
	for i := 1; i <= 11; i++ {
		host := fmt.Sprintf("a%d.com", i)
		rec += " include:" + host
		r.TXTRecords[host] = []string{"v=spf1 -all"} // no further lookups
	}
	rec += " -all"
	r.TXTRecords["example.com"] = []string{rec}

	_, fs := evaluateSPF(context.Background(), r, "example.com")
	mustHave(t, fs, CodeSPFLookupLimit, SevCritical)
}

func TestSPFPlusAll(t *testing.T) {
	r := NewStaticResolver()
	r.TXTRecords["example.com"] = []string{"v=spf1 +all"}
	_, fs := evaluateSPF(context.Background(), r, "example.com")
	mustHave(t, fs, CodeSPFPlusAll, SevCritical)
}

func TestSPFGood(t *testing.T) {
	r := NewStaticResolver()
	r.TXTRecords["example.com"] = []string{"v=spf1 include:_spf.example.com -all"}
	r.TXTRecords["_spf.example.com"] = []string{"v=spf1 ip4:198.51.100.0/24 -all"}
	_, fs := evaluateSPF(context.Background(), r, "example.com")
	mustNotHave(t, fs, CodeSPFLookupLimit)
	mustNotHave(t, fs, CodeSPFPlusAll)
	mustNotHave(t, fs, CodeSPFMissing)
	mustNotHave(t, fs, CodeSPFNoAll)
}

func TestSPFRecursiveLoop(t *testing.T) {
	r := NewStaticResolver()
	r.TXTRecords["example.com"] = []string{"v=spf1 include:loop.com -all"}
	r.TXTRecords["loop.com"] = []string{"v=spf1 include:example.com -all"}
	_, fs := evaluateSPF(context.Background(), r, "example.com")
	mustHave(t, fs, CodeSPFRecursive, SevCritical)
}

// ---- DKIM ------------------------------------------------------------------

// dkimRecord builds a DKIM TXT record carrying an RSA public key with a modulus of the
// given bit length. The key is synthetic (we set the top bit so N.BitLen()==bits) — only
// the size matters for the weak-key check, and this sidesteps the keygen minimum-size
// restriction for the <1024-bit case.
func dkimRecord(t *testing.T, bits int) string {
	t.Helper()
	n := new(big.Int).Lsh(big.NewInt(1), uint(bits-1))
	n.Or(n, big.NewInt(1)) // make it odd, like a real modulus
	der, err := x509.MarshalPKIXPublicKey(&rsa.PublicKey{N: n, E: 65537})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return "v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(der)
}

func TestDKIMMissingSelector(t *testing.T) {
	r := NewStaticResolver()
	_, fs := evaluateDKIM(context.Background(), r, "example.com", []string{"sel1"})
	mustHave(t, fs, CodeDKIMMissingSelector, SevCritical)
}

func TestDKIMRevoked(t *testing.T) {
	r := NewStaticResolver()
	r.TXTRecords["sel1._domainkey.example.com"] = []string{"v=DKIM1; k=rsa; p="}
	_, fs := evaluateDKIM(context.Background(), r, "example.com", []string{"sel1"})
	mustHave(t, fs, CodeDKIMRevoked, SevCritical)
}

func TestDKIMWeakKey(t *testing.T) {
	r := NewStaticResolver()
	r.TXTRecords["weak._domainkey.example.com"] = []string{dkimRecord(t, 512)}
	_, fs := evaluateDKIM(context.Background(), r, "example.com", []string{"weak"})
	mustHave(t, fs, CodeDKIMWeakKey, SevCritical)
}

func TestDKIMStrongKey(t *testing.T) {
	r := NewStaticResolver()
	r.TXTRecords["strong._domainkey.example.com"] = []string{dkimRecord(t, 2048)}
	recs, fs := evaluateDKIM(context.Background(), r, "example.com", []string{"strong"})
	mustNotHave(t, fs, CodeDKIMWeakKey)
	mustNotHave(t, fs, CodeDKIMRevoked)
	if recs["strong"] == "" {
		t.Error("expected the strong selector record to be recorded")
	}
}

// ---- DMARC -----------------------------------------------------------------

func TestDMARCMissing(t *testing.T) {
	r := NewStaticResolver()
	_, fs := evaluateDMARC(context.Background(), r, "example.com")
	mustHave(t, fs, CodeDMARCMissing, SevCritical)
}

func TestDMARCPolicyNone(t *testing.T) {
	r := NewStaticResolver()
	r.TXTRecords["_dmarc.example.com"] = []string{"v=DMARC1; p=none; rua=mailto:agg@example.com"}
	_, fs := evaluateDMARC(context.Background(), r, "example.com")
	mustHave(t, fs, CodeDMARCPolicyNone, SevWarning)
	mustNotHave(t, fs, CodeDMARCMissingRUA)
}

func TestDMARCMissingRUA(t *testing.T) {
	r := NewStaticResolver()
	r.TXTRecords["_dmarc.example.com"] = []string{"v=DMARC1; p=reject"}
	_, fs := evaluateDMARC(context.Background(), r, "example.com")
	mustHave(t, fs, CodeDMARCMissingRUA, SevWarning)
	mustNotHave(t, fs, CodeDMARCPolicyNone)
}

func TestDMARCGood(t *testing.T) {
	r := NewStaticResolver()
	r.TXTRecords["_dmarc.example.com"] = []string{"v=DMARC1; p=reject; rua=mailto:agg@example.com"}
	_, fs := evaluateDMARC(context.Background(), r, "example.com")
	if len(fs) != 0 {
		t.Errorf("expected no DMARC findings, got %v", codes(fs))
	}
}

// ---- Inspect (snapshot + checksum + acceptance scenario) -------------------

func healthyResolver() *StaticResolver {
	r := NewStaticResolver()
	r.MXRecords["example.com"] = []*net.MX{{Host: "mx1.example.com.", Pref: 10}}
	r.TXTRecords["example.com"] = []string{"v=spf1 ip4:198.51.100.0/24 -all"}
	r.TXTRecords["_dmarc.example.com"] = []string{"v=DMARC1; p=reject; rua=mailto:agg@example.com"}
	return r
}

func TestInspectChecksumStableAndChanges(t *testing.T) {
	r := healthyResolver()
	ctx := context.Background()

	s1, err := Inspect(ctx, r, "example.com", Options{})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	s2, _ := Inspect(ctx, r, "example.com", Options{})
	if s1.Checksum != s2.Checksum {
		t.Errorf("checksum not stable: %s != %s", s1.Checksum, s2.Checksum)
	}
	if !s1.Healthy {
		t.Errorf("expected healthy snapshot, findings=%v", codes(s1.Findings))
	}

	// Mutate DNS — checksum must change.
	r.TXTRecords["example.com"] = []string{"v=spf1 ip4:203.0.113.0/24 -all"}
	s3, _ := Inspect(ctx, r, "example.com", Options{})
	if s3.Checksum == s1.Checksum {
		t.Error("checksum did not change after DNS mutation")
	}
}

// The WS3 acceptance scenario: bad SPF (>10 lookups) + stale DKIM selector.
func TestInspectAcceptanceScenario(t *testing.T) {
	r := healthyResolver()
	rec := "v=spf1"
	for i := 1; i <= 11; i++ {
		host := fmt.Sprintf("a%d.com", i)
		rec += " include:" + host
		r.TXTRecords[host] = []string{"v=spf1 -all"}
	}
	rec += " -all"
	r.TXTRecords["example.com"] = []string{rec}

	snap, err := Inspect(context.Background(), r, "example.com", Options{Selectors: []string{"selector2"}})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	mustHave(t, snap.Findings, CodeSPFLookupLimit, SevCritical)
	mustHave(t, snap.Findings, CodeDKIMMissingSelector, SevCritical)
	if snap.Healthy {
		t.Error("expected unhealthy snapshot")
	}
}

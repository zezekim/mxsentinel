package dns

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"strings"

	"github.com/zezekim/mxsentinel/pkg/contracts"
)

// dkimRecommendedBits is the recommended minimum RSA key size; below dkimMinBits is
// considered dangerously weak.
const (
	dkimRecommendedBits = 2048
	dkimMinBits         = 1024
)

// evaluateDKIM checks each provided selector at <selector>._domainkey.<domain>. Selectors
// to check come from configuration / observed mail; this validator does not discover them.
// Returns the raw records found (by selector) and findings.
func evaluateDKIM(ctx context.Context, r Resolver, domain string, selectors []string) (map[string]string, []contracts.DNSFinding) {
	records := map[string]string{}
	var findings []contracts.DNSFinding

	for _, sel := range selectors {
		name := sel + "._domainkey." + domain
		txts, err := r.TXT(ctx, name)
		if err != nil {
			findings = append(findings, finding(CatDKIM, SevWarning, CodeDKIMInvalid,
				"DKIM lookup failed for selector "+sel+": "+err.Error(),
				map[string]any{"selector": sel}))
			continue
		}

		record := pickDKIM(txts)
		if record == "" {
			findings = append(findings, finding(CatDKIM, SevCritical, CodeDKIMMissingSelector,
				"DKIM selector "+sel+" has no record (stale or removed selector)",
				map[string]any{"selector": sel, "name": name}))
			continue
		}
		records[sel] = record

		tags := parseTags(record)
		if t := tags["t"]; strings.Contains(t, "y") {
			findings = append(findings, finding(CatDKIM, SevInfo, CodeDKIMTesting,
				"DKIM selector "+sel+" is in testing mode (t=y)",
				map[string]any{"selector": sel}))
		}

		p, ok := tags["p"]
		if !ok || strings.TrimSpace(p) == "" {
			findings = append(findings, finding(CatDKIM, SevCritical, CodeDKIMRevoked,
				"DKIM selector "+sel+" has an empty public key (revoked)",
				map[string]any{"selector": sel}))
			continue
		}

		bits, perr := rsaKeyBits(p)
		switch {
		case perr != nil:
			// Non-RSA keys (e.g. Ed25519, k=ed25519) won't parse as RSA; flag only if it
			// claims RSA or has no k tag (default rsa).
			if k := tags["k"]; k == "" || strings.EqualFold(k, "rsa") {
				findings = append(findings, finding(CatDKIM, SevWarning, CodeDKIMInvalid,
					"DKIM selector "+sel+" public key could not be parsed: "+perr.Error(),
					map[string]any{"selector": sel}))
			}
		case bits < dkimMinBits:
			findings = append(findings, finding(CatDKIM, SevCritical, CodeDKIMWeakKey,
				"DKIM selector "+sel+" uses a dangerously weak key",
				map[string]any{"selector": sel, "bits": bits}))
		case bits < dkimRecommendedBits:
			findings = append(findings, finding(CatDKIM, SevWarning, CodeDKIMWeakKey,
				"DKIM selector "+sel+" key is below the recommended 2048 bits",
				map[string]any{"selector": sel, "bits": bits}))
		}
	}
	return records, findings
}

// pickDKIM returns the first TXT that looks like a DKIM key record.
func pickDKIM(txts []string) string {
	for _, t := range txts {
		lt := strings.ToLower(t)
		if strings.HasPrefix(strings.TrimSpace(lt), "v=dkim1") || strings.Contains(lt, "p=") {
			return t
		}
	}
	return ""
}

// rsaKeyBits decodes a base64 DKIM public key and returns its RSA modulus bit length.
func rsaKeyBits(p string) (int, error) {
	der, err := base64.StdEncoding.DecodeString(strings.TrimSpace(p))
	if err != nil {
		return 0, err
	}
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return 0, err
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return 0, errNotRSA
	}
	return rsaPub.N.BitLen(), nil
}

// parseTags parses "k=v; k=v" tag lists (DKIM/DMARC) into a map. The first '=' in each
// part separates key from value, so base64 padding in values is preserved.
func parseTags(record string) map[string]string {
	m := map[string]string{}
	for _, part := range strings.Split(record, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		eq := strings.IndexByte(part, '=')
		if eq < 0 {
			continue
		}
		k := strings.TrimSpace(part[:eq])
		v := strings.TrimSpace(part[eq+1:])
		m[k] = v
	}
	return m
}

// errNotRSA is returned when a DKIM key is not an RSA key.
var errNotRSA = errStr("dkim key is not RSA")

type errStr string

func (e errStr) Error() string { return string(e) }

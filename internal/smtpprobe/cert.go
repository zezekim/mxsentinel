package smtpprobe

import (
	"crypto/x509"
	"strings"
	"time"
)

// DefaultCertWarnThreshold is the lead time before certificate expiry at which a probe
// starts flagging the certificate as "expiring" (and emits a warning event).
const DefaultCertWarnThreshold = 14 * 24 * time.Hour

// InspectCerts derives CertInfo from a negotiated TLS peer-certificate chain. It is pure
// (no clock or network access — `now` and `warn` are injected) so it can be unit-tested
// with fabricated certificates.
//
//   - peer[0] is the leaf (end-entity) certificate; its NotAfter drives expiry.
//   - verifiedChains is non-empty only when the TLS stack built a chain to a trusted root
//     (i.e. the certificate is trusted); we surface that as ChainValid.
//   - DaysUntilExpiry is floored days from now to the leaf's NotAfter (negative if expired).
//   - Expiring is true when the leaf expires within `warn`, OR is already expired.
func InspectCerts(peer []*x509.Certificate, verifiedChains [][]*x509.Certificate, now time.Time, warn time.Duration) CertInfo {
	var ci CertInfo
	ci.ChainValid = len(verifiedChains) > 0
	if len(peer) == 0 {
		return ci
	}
	leaf := peer[0]
	ci.Subject = leaf.Subject.CommonName
	ci.Issuer = leaf.Issuer.CommonName
	ci.DNSNames = leaf.DNSNames
	ci.NotBefore = leaf.NotBefore
	ci.NotAfter = leaf.NotAfter

	remaining := leaf.NotAfter.Sub(now)
	ci.DaysUntilExpiry = int(remaining / (24 * time.Hour))
	ci.Expired = now.After(leaf.NotAfter)
	if warn <= 0 {
		warn = DefaultCertWarnThreshold
	}
	ci.Expiring = ci.Expired || remaining <= warn
	return ci
}

// CertSummary renders a short human string for logs/events, e.g.
// "CN=mail.example.com issuer=R3 expires in 9d".
func (c CertInfo) CertSummary() string {
	var b strings.Builder
	if c.Subject != "" {
		b.WriteString("CN=" + c.Subject)
	}
	if c.Issuer != "" {
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		b.WriteString("issuer=" + c.Issuer)
	}
	if b.Len() > 0 {
		b.WriteString(" ")
	}
	switch {
	case c.Expired:
		b.WriteString("EXPIRED")
	default:
		b.WriteString("expires in ")
		b.WriteString(itoa(c.DaysUntilExpiry))
		b.WriteString("d")
	}
	return b.String()
}

func itoa(n int) string {
	// small, allocation-light int->string without importing strconv here
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

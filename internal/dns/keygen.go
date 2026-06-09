package dns

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"time"
)

// DKIMKeyPair holds a newly generated DKIM key pair.
type DKIMKeyPair struct {
	Selector    string // e.g. "mxs-20260610"
	PublicKey   string // base64-encoded public key (the p= value for the DNS TXT record)
	PrivateKey  string // PEM-encoded PKCS#1 RSA private key
	KeyBits     int
	DNSRecord   string // full DNS TXT record value: "v=DKIM1; k=rsa; p=<PublicKey>"
	GeneratedAt time.Time
}

// GenerateDKIMKeyPair generates a new RSA key pair for DKIM signing.
// bits should be 2048 (minimum recommended) or 4096.
// selector is the DKIM selector name; if empty, one is auto-generated as "mxs-YYYYMMDD".
func GenerateDKIMKeyPair(bits int, selector string) (*DKIMKeyPair, error) {
	if bits < 1024 {
		return nil, fmt.Errorf("dns: key size %d is too small; minimum is 1024 bits", bits)
	}

	key, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		return nil, fmt.Errorf("dns: failed to generate RSA key: %w", err)
	}

	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})

	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("dns: failed to marshal public key: %w", err)
	}
	pubB64 := base64.StdEncoding.EncodeToString(pubDER)

	if selector == "" {
		selector = fmt.Sprintf("mxs-%s", time.Now().Format("20060102"))
	}

	return &DKIMKeyPair{
		Selector:    selector,
		PublicKey:   pubB64,
		PrivateKey:  string(privPEM),
		KeyBits:     bits,
		DNSRecord:   "v=DKIM1; k=rsa; p=" + pubB64,
		GeneratedAt: time.Now(),
	}, nil
}

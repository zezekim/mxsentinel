package bimi

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// maxLogoBytes is the practical ceiling BIMI implementations apply to the SVG logo. Files
// larger than this are rejected by most mailbox providers.
const maxLogoBytes = 32 * 1024

var (
	reBaseProfile = regexp.MustCompile(`baseProfile\s*=\s*["']tiny-ps["']`)
	reVersion12   = regexp.MustCompile(`\bversion\s*=\s*["']1\.2["']`)
	reTitle       = regexp.MustCompile(`(?is)<title[\s>].*?</title>`)
	reSVGRoot     = regexp.MustCompile(`(?is)<svg[\s>]`)
	// Forbidden constructs for SVG Tiny P/S (Portable/Secure): no scripting, external
	// references, raster images, or animation.
	forbidden = []struct {
		pattern *regexp.Regexp
		msg     string
	}{
		{regexp.MustCompile(`(?is)<script[\s/>]`), "contains <script> (scripting is forbidden)"},
		{regexp.MustCompile(`(?is)<image[\s/>]`), "contains <image> (raster/embedded images are forbidden)"},
		{regexp.MustCompile(`(?is)<foreignObject[\s/>]`), "contains <foreignObject> (forbidden)"},
		{regexp.MustCompile(`(?is)<a[\s/>]`), "contains <a> hyperlink (forbidden)"},
		{regexp.MustCompile(`(?is)<(animate|animateTransform|animateMotion|set)[\s/>]`), "contains animation elements (forbidden)"},
		{regexp.MustCompile(`(?is)xlink:href\s*=\s*["']\s*https?:`), "references an external URL (forbidden)"},
		{regexp.MustCompile(`(?is)\bhref\s*=\s*["']\s*https?:`), "references an external URL (forbidden)"},
	}
)

// ValidateSVG checks that data is a plausible SVG Tiny P/S document as BIMI requires. It
// returns a list of human-readable problems; an empty slice means the logo passes. This is a
// pragmatic, dependency-free lint (not a full XML validator): it enforces the checks that most
// commonly block BIMI adoption — the tiny-ps base profile, version 1.2, a <title>, a size
// ceiling, and the absence of scripting/external/animation constructs.
func ValidateSVG(data []byte) []string {
	var problems []string
	if len(data) == 0 {
		return []string{"logo is empty"}
	}
	if len(data) > maxLogoBytes {
		problems = append(problems, fmt.Sprintf("logo is %d bytes, over the %d-byte limit", len(data), maxLogoBytes))
	}

	s := string(data)
	if !reSVGRoot.MatchString(s) {
		return append(problems, "not an SVG document (no <svg> root element)")
	}
	if !reBaseProfile.MatchString(s) {
		problems = append(problems, `missing baseProfile="tiny-ps" on the <svg> element`)
	}
	if !reVersion12.MatchString(s) {
		problems = append(problems, `missing version="1.2" on the <svg> element`)
	}
	if !reTitle.MatchString(s) {
		problems = append(problems, "missing <title> element (required)")
	}
	if strings.Contains(strings.ToLower(s), "<?xml-stylesheet") {
		problems = append(problems, "contains an external stylesheet reference (forbidden)")
	}
	for _, f := range forbidden {
		if f.pattern.MatchString(s) {
			problems = append(problems, f.msg)
		}
	}
	return problems
}

// ParseVMCExpiry parses a PEM-encoded VMC (or its certificate chain) and returns the leaf
// certificate's NotAfter. It reads the first CERTIFICATE block, which for a VMC is the entity
// certificate carrying the logotype extension. No network I/O.
func ParseVMCExpiry(pemBytes []byte) (time.Time, error) {
	rest := pemBytes
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return time.Time{}, fmt.Errorf("parse certificate: %w", err)
		}
		return cert.NotAfter, nil
	}
	return time.Time{}, errors.New("no CERTIFICATE block found in VMC PEM")
}

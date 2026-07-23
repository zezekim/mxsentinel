package snds

import (
	"bufio"
	"net"
	"strings"

	"github.com/zezekim/mxsentinel/internal/fbl"
)

// JMRPComplaint is a parsed Microsoft Junk Mail Reporting Program (JMRP) ARF complaint. JMRP
// uses the same RFC 5965 ARF format Google/Yahoo do, so we reuse internal/fbl.ParseARF for the
// feedback-type / sending-domain / provider fields and additionally recover the SENDING IP
// (the Source-IP feedback-report field, or the last Received: hop of the embedded original
// message) because SNDS reputation is keyed by IP — JMRP attribution must join to it.
type JMRPComplaint struct {
	FeedbackType string // abuse, fraud, ... (ARF Feedback-Type; defaults to "abuse")
	SourceIP     string // sending IP the complaint is attributed to ("" if not recoverable)
	SenderDomain string // From: domain of the complained-about message
	Provider     string // reporting provider (expected "microsoft" for JMRP)
	MessageID    string // original Message-ID, if present
}

// ParseJMRP parses a JMRP ARF complaint email. It delegates the shared ARF fields to
// internal/fbl.ParseARF (keeping the two subsystems' parsing identical) and layers on the
// Microsoft-specific sending-IP extraction. Privacy boundary: only metadata is read — never
// the message body or subject.
func ParseJMRP(raw []byte) (JMRPComplaint, error) {
	pc, err := fbl.ParseARF(raw)
	if err != nil {
		return JMRPComplaint{}, err
	}
	return JMRPComplaint{
		FeedbackType: pc.FeedbackType,
		SourceIP:     sourceIPFromARF(raw),
		SenderDomain: pc.SenderDomain,
		Provider:     pc.Provider,
		MessageID:    pc.MessageID,
	}, nil
}

// sourceIPFromARF scans the raw ARF message for the sending IP. It first looks for the
// machine-readable feedback-report fields Microsoft populates (Source-IP: / Sender-IP: /
// X-Sender-IP:), then falls back to the first bracketed IP literal in a Received: header of
// the embedded original message. It never returns a private/unspecified address.
func sourceIPFromARF(raw []byte) string {
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var receivedIP string
	for sc.Scan() {
		line := sc.Text()
		lower := strings.ToLower(line)
		switch {
		case strings.HasPrefix(lower, "source-ip:"),
			strings.HasPrefix(lower, "sender-ip:"),
			strings.HasPrefix(lower, "x-sender-ip:"),
			strings.HasPrefix(lower, "x-originating-ip:"):
			if ip := firstIP(line[strings.IndexByte(line, ':')+1:]); ip != "" {
				return ip
			}
		case strings.HasPrefix(lower, "received:"):
			if receivedIP == "" {
				receivedIP = firstIP(line)
			}
		}
	}
	return receivedIP
}

// firstIP extracts the first parseable IPv4/IPv6 literal from s, stripping surrounding
// brackets/parens. Returns "" if none is a valid, non-unspecified address.
func firstIP(s string) string {
	repl := strings.NewReplacer("[", " ", "]", " ", "(", " ", ")", " ", ",", " ", ";", " ")
	for _, tok := range strings.Fields(repl.Replace(s)) {
		tok = strings.Trim(tok, "<>\"")
		if ip := net.ParseIP(tok); ip != nil && !ip.IsUnspecified() {
			return ip.String()
		}
	}
	return ""
}

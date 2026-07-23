package smtpprobe

import (
	"fmt"
	"strconv"
	"strings"
)

// netJoin builds a host:port string. It is a tiny helper kept here so types.go stays
// import-free; it intentionally does not use net.JoinHostPort (which needs a string port and
// would bracket IPv6 — probe hosts are hostnames or IPv4 addresses in practice).
func netJoin(host string, port int) string {
	return fmt.Sprintf("%s:%d", host, port)
}

// ParseEHLO interprets the lines of a 250 multiline EHLO response into Capabilities. The
// input is the set of reply lines with the numeric code and the code separator ("-"/" ")
// already stripped, i.e. the text portion of each "250-…" line. The first line is the
// server greeting; the remainder are capability keywords. Parsing is case-insensitive on
// the keyword but preserves the greeting verbatim.
//
// It is a pure function (no I/O) so it is unit-tested directly.
func ParseEHLO(lines []string) Capabilities {
	var caps Capabilities
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if i == 0 {
			caps.Greeting = line
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		keyword := strings.ToUpper(fields[0])
		caps.Keywords = append(caps.Keywords, keyword)
		switch keyword {
		case "STARTTLS":
			caps.STARTTLS = true
		case "AUTH":
			caps.Auth = true
			for _, m := range fields[1:] {
				if m = strings.ToUpper(strings.TrimSpace(m)); m != "" {
					caps.AuthMechs = append(caps.AuthMechs, m)
				}
			}
		case "PIPELINING":
			caps.Pipelining = true
		case "8BITMIME":
			caps.EightBitMIME = true
		case "ENHANCEDSTATUSCODES":
			caps.Enhanced = true
		case "SIZE":
			if len(fields) > 1 {
				if n, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
					caps.Size = n
				}
			}
		}
	}
	return caps
}

// splitReplyLines splits a textproto multiline reply message (lines joined by "\n") into
// its individual lines. textproto.Reader.ReadResponse returns the code once and joins the
// human-readable portion of each line with "\n"; for EHLO that portion is exactly the
// capability text we want to feed to ParseEHLO.
func splitReplyLines(message string) []string {
	if message == "" {
		return nil
	}
	return strings.Split(message, "\n")
}

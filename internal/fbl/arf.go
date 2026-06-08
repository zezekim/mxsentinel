package fbl

import (
	"bufio"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
)

// ParsedComplaint is the data extracted from one ARF feedback report.
type ParsedComplaint struct {
	FeedbackType string // Feedback-Type from the message/feedback-report part (abuse, fraud, ...)
	SenderDomain string // From: domain of the complained-about (original) message
	SASLUsername string // submission login, if an X-MXS-* / Authentication header survived
	Provider     string // reporting provider, inferred from the report's User-Agent / From
	MessageID    string // original Message-ID, if present
}

// ParseARF parses an RFC 5965 ARF complaint email (Content-Type: multipart/report;
// report-type=feedback-report). It is tolerant: an ARF message has three parts —
// (1) a human-readable explanation, (2) a message/feedback-report with machine fields,
// (3) the original message (or just its headers) as message/rfc822. Some providers send
// only headers, some the whole message; some mislabel parts. We pull Feedback-Type from
// the feedback-report part and the original From/Message-ID from the rfc822 part, falling
// back to scanning every part so a slightly-off provider format still yields a domain.
func ParseARF(raw []byte) (ParsedComplaint, error) {
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		return ParsedComplaint{}, fmt.Errorf("read message: %w", err)
	}

	var pc ParsedComplaint
	pc.Provider = providerFromReport(msg.Header)

	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		return ParsedComplaint{}, fmt.Errorf("parse top content-type: %w", err)
	}
	boundary := params["boundary"]
	if !strings.HasPrefix(mediaType, "multipart/") || boundary == "" {
		return ParsedComplaint{}, fmt.Errorf("not a multipart ARF report (content-type %q)", mediaType)
	}

	mr := multipart.NewReader(msg.Body, boundary)
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return ParsedComplaint{}, fmt.Errorf("read part: %w", err)
		}
		ct := part.Header.Get("Content-Type")
		ptype, _, _ := mime.ParseMediaType(ct)
		body, _ := io.ReadAll(part)
		_ = part.Close()

		switch ptype {
		case "message/feedback-report":
			applyFeedbackReport(&pc, body)
		case "message/rfc822", "text/rfc822-headers":
			applyOriginalMessage(&pc, body)
		default:
			// Some providers (notably older Yahoo) put feedback fields or the original
			// headers in a text/plain part. Try both extractors best-effort; they only
			// fill empty fields.
			applyFeedbackReport(&pc, body)
			applyOriginalMessage(&pc, body)
		}
	}

	if pc.FeedbackType == "" {
		pc.FeedbackType = "abuse" // RFC 5965 default class for an FBL complaint
	}
	return pc, nil
}

// applyFeedbackReport reads the machine-readable feedback-report part: a block of
// header-style "Field: value" lines. We care about Feedback-Type, and use
// Original-Mail-From / Reported-Domain / User-Agent as fallbacks for sender + provider.
func applyFeedbackReport(pc *ParsedComplaint, body []byte) {
	hdr := readHeaderLines(body)
	if pc.FeedbackType == "" {
		pc.FeedbackType = strings.ToLower(strings.TrimSpace(hdr.get("Feedback-Type")))
	}
	if pc.SenderDomain == "" {
		if d := domainOfAddress(hdr.get("Original-Mail-From")); d != "" {
			pc.SenderDomain = d
		} else if rd := strings.TrimSpace(hdr.get("Reported-Domain")); rd != "" {
			pc.SenderDomain = strings.ToLower(rd)
		}
	}
	if pc.Provider == "" {
		if ua := hdr.get("User-Agent"); ua != "" {
			pc.Provider = providerFromString(ua)
		}
	}
	if pc.SASLUsername == "" {
		// Some relays stamp the authenticated login into a custom field carried through.
		for _, k := range []string{"X-Mxs-Sasl-Username", "X-Sasl-Username", "X-Authenticated-As"} {
			if v := strings.TrimSpace(hdr.get(k)); v != "" {
				pc.SASLUsername = v
				break
			}
		}
	}
}

// applyOriginalMessage reads the embedded original message's headers (message/rfc822 or
// text/rfc822-headers) to recover the true sending domain (From:), Message-ID, and any
// custom SASL-login header the relay added on the way out.
func applyOriginalMessage(pc *ParsedComplaint, body []byte) {
	hdr := readHeaderLines(body)

	if pc.MessageID == "" {
		if mid := strings.Trim(strings.TrimSpace(hdr.get("Message-ID")), "<>"); mid != "" {
			pc.MessageID = mid
		}
	}
	// Prefer the From: domain; fall back to the envelope/Return-Path or Sender.
	if d := domainOfAddress(hdr.get("From")); d != "" {
		pc.SenderDomain = d
	} else if pc.SenderDomain == "" {
		if d := domainOfAddress(hdr.get("Sender")); d != "" {
			pc.SenderDomain = d
		} else if d := domainOfAddress(hdr.get("Return-Path")); d != "" {
			pc.SenderDomain = d
		}
	}
	if pc.SASLUsername == "" {
		for _, k := range []string{"X-Mxs-Sasl-Username", "X-Sasl-Username", "X-Authenticated-As"} {
			if v := strings.TrimSpace(hdr.get(k)); v != "" {
				pc.SASLUsername = v
				break
			}
		}
	}
}

// header is a case-insensitive multimap of the first value seen per key. We parse headers
// manually (rather than mail.ReadMessage) because embedded parts may have body content we
// don't want, and providers vary in whether a blank line terminates the headers.
type header map[string]string

func (h header) get(key string) string { return h[strings.ToLower(key)] }

// readHeaderLines parses RFC 5322 style header lines (with folded-line continuation) from
// the start of a part body up to the first blank line, lower-casing keys.
func readHeaderLines(body []byte) header {
	h := header{}
	sc := bufio.NewScanner(strings.NewReader(string(body)))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var lastKey string
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			break // end of headers
		}
		if (line[0] == ' ' || line[0] == '\t') && lastKey != "" {
			h[lastKey] = strings.TrimSpace(h[lastKey] + " " + strings.TrimSpace(line))
			continue
		}
		idx := strings.IndexByte(line, ':')
		if idx <= 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:idx]))
		val := strings.TrimSpace(line[idx+1:])
		if _, seen := h[key]; !seen {
			h[key] = val
			lastKey = key
		}
	}
	return h
}

// domainOfAddress extracts the lower-cased domain from an address header value
// ("Name <user@dom>" or "user@dom" or "<user@dom>"). Returns "" if unparseable.
func domainOfAddress(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if addr, err := mail.ParseAddress(v); err == nil {
		if at := strings.LastIndexByte(addr.Address, '@'); at >= 0 {
			return strings.ToLower(addr.Address[at+1:])
		}
	}
	// Fallback: strip angle brackets and split on '@'.
	v = strings.Trim(v, "<>")
	if at := strings.LastIndexByte(v, '@'); at >= 0 {
		return strings.ToLower(strings.TrimSpace(v[at+1:]))
	}
	return ""
}

// providerFromReport guesses the reporting mailbox provider from the report email's own
// From:/User-Agent header (this is the FBL operator, not the original sender).
func providerFromReport(h mail.Header) string {
	if p := providerFromString(h.Get("From")); p != "" {
		return p
	}
	return providerFromString(h.Get("User-Agent"))
}

// providerFromString maps common feedback-loop operators to a short label.
func providerFromString(s string) string {
	s = strings.ToLower(s)
	switch {
	case strings.Contains(s, "google") || strings.Contains(s, "gmail"):
		return "google"
	case strings.Contains(s, "yahoo") || strings.Contains(s, "ymail"):
		return "yahoo"
	case strings.Contains(s, "microsoft") || strings.Contains(s, "outlook") ||
		strings.Contains(s, "hotmail") || strings.Contains(s, "sndsfeedback"):
		return "microsoft"
	case strings.Contains(s, "comcast"):
		return "comcast"
	case strings.Contains(s, "aol"):
		return "aol"
	default:
		return ""
	}
}

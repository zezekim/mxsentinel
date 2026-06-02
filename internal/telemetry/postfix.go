package telemetry

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/zezekim/mxsentinel/pkg/contracts"
)

// Event is a parsed SMTP telemetry event ready to be wrapped in an envelope. FromDomain is
// surfaced separately so the daemon can resolve the owning tenant.
type Event struct {
	Type        contracts.EventType
	OccurredAt  time.Time
	FromDomain  string
	Payload     contracts.SMTPPayload
	Correlation contracts.Correlation
}

var (
	reLine   = regexp.MustCompile(`postfix/(\w+)\[\d+\]:\s+(.*)$`)
	reTS     = regexp.MustCompile(`^(\w{3}\s+\d+\s+\d+:\d+:\d+)`)
	reQID    = regexp.MustCompile(`^([0-9A-Za-z]{5,}):\s+(.*)$`)
	reFrom   = regexp.MustCompile(`\bfrom=<([^>]*)>`)
	reSize   = regexp.MustCompile(`\bsize=(\d+)`)
	reMsgID  = regexp.MustCompile(`\bmessage-id=<?([^>\s,]+)>?`)
	reTo     = regexp.MustCompile(`\bto=<([^>]*)>`)
	reRelay  = regexp.MustCompile(`\brelay=([^,]+)`)
	reDSN    = regexp.MustCompile(`\bdsn=(\d+\.\d+\.\d+)`)
	reStatus = regexp.MustCompile(`\bstatus=(\w+)`)
	reResp   = regexp.MustCompile(`status=\w+\s+\((.*)\)\s*$`)
	reCode   = regexp.MustCompile(`\b([2-5]\d\d)\b`)
	reHostIP = regexp.MustCompile(`^([^\[]+)\[([^\]]+)\]`)
)

// msgState accumulates the per-message context that arrives across qmgr/cleanup lines
// before a delivery (smtp) line lets us emit an event.
type msgState struct {
	envelopeFrom string
	fromDomain   string
	size         int
	messageID    string
}

// Parser consumes Postfix maillog lines and emits Events. It is stateful (keyed by queue
// id) but not safe for concurrent use; feed one log stream per Parser.
//
// Collection mechanism: maillog parsing is the lowest-friction start. A milter/policy
// daemon could provide richer real-time data later — see docs/phase-1-plan.md WS4.
type Parser struct {
	year    int
	relayIP string
	h       hasher
	states  map[string]*msgState
}

// NewParser builds a parser. year stamps the (yearless) BSD-syslog timestamps; relayIP is
// this node's outbound IP; hashKey keys the recipient hash.
func NewParser(year int, relayIP string, hashKey []byte) *Parser {
	return &Parser{
		year:    year,
		relayIP: relayIP,
		h:       hasher{key: hashKey},
		states:  map[string]*msgState{},
	}
}

func (p *Parser) state(qid string) *msgState {
	st := p.states[qid]
	if st == nil {
		st = &msgState{}
		p.states[qid] = st
	}
	return st
}

// Parse processes one log line and returns 0 or 1 events (a delivery line yields one).
func (p *Parser) Parse(line string) []Event {
	lm := reLine.FindStringSubmatch(line)
	if lm == nil {
		return nil
	}
	rest := lm[2]

	qm := reQID.FindStringSubmatch(rest)
	if qm == nil {
		return nil
	}
	qid, kv := qm[1], qm[2]
	if qid == "NOQUEUE" { // inbound smtpd rejects, not outbound telemetry
		return nil
	}

	switch {
	case strings.Contains(kv, "status="):
		return p.emit(qid, kv, p.parseTime(line))
	case strings.Contains(kv, "from=") && strings.Contains(kv, "size="):
		st := p.state(qid)
		if m := reFrom.FindStringSubmatch(kv); m != nil {
			st.envelopeFrom = m[1]
			st.fromDomain = domainOf(m[1])
		}
		if m := reSize.FindStringSubmatch(kv); m != nil {
			st.size, _ = strconv.Atoi(m[1])
		}
	case strings.Contains(kv, "message-id="):
		if m := reMsgID.FindStringSubmatch(kv); m != nil {
			p.state(qid).messageID = ensureAngle(m[1])
		}
	case kv == "removed":
		delete(p.states, qid)
	}
	return nil
}

func (p *Parser) emit(qid, kv string, ts time.Time) []Event {
	statusM := reStatus.FindStringSubmatch(kv)
	if statusM == nil {
		return nil
	}
	outcome, eventType, ok := mapStatus(statusM[1])
	if !ok {
		return nil
	}

	st := p.states[qid]
	if st == nil {
		st = &msgState{}
	}

	var recipient string
	if m := reTo.FindStringSubmatch(kv); m != nil {
		recipient = m[1]
	}

	var relayHost string
	if m := reRelay.FindStringSubmatch(kv); m != nil {
		relayHost = parseRelayHost(strings.TrimSpace(m[1]))
	}

	dsn := ""
	if m := reDSN.FindStringSubmatch(kv); m != nil {
		dsn = m[1]
	}
	resp := ""
	if m := reResp.FindStringSubmatch(kv); m != nil {
		resp = m[1]
	}
	code := 0
	if m := reCode.FindStringSubmatch(resp); m != nil {
		code, _ = strconv.Atoi(m[1])
	}

	payload := contracts.SMTPPayload{
		Outcome:         outcome,
		BounceClass:     classifyBounce(outcome, dsn, code, resp),
		MessageID:       st.messageID,
		QueueID:         qid,
		EnvelopeFrom:    st.envelopeFrom,
		FromDomain:      st.fromDomain,
		RecipientDomain: domainOf(recipient),
		RecipientHash:   p.h.hash(recipient),
		Provider:        classifyProvider(relayHost),
		MXHost:          relayHost,
		RelayIP:         p.relayIP,
		SMTPCode:        code,
		EnhancedStatus:  dsn,
		ResponseText:    truncate(resp, maxResponseText),
		SizeBytes:       st.size,
	}

	return []Event{{
		Type:       eventType,
		OccurredAt: ts,
		FromDomain: st.fromDomain,
		Payload:    payload,
		Correlation: contracts.Correlation{
			MessageID:    st.messageID,
			QueueID:      qid,
			EnvelopeFrom: st.envelopeFrom,
			RelayIP:      p.relayIP,
			Domain:       st.fromDomain,
		},
	}}
}

// mapStatus maps a Postfix delivery status word to our outcome + event type.
func mapStatus(status string) (string, contracts.EventType, bool) {
	switch strings.ToLower(status) {
	case "sent":
		return "delivered", contracts.EventSMTPDelivered, true
	case "deferred":
		return "deferred", contracts.EventSMTPDeferred, true
	case "bounced", "expired":
		return "bounced", contracts.EventSMTPBounced, true
	default:
		return "", "", false
	}
}

// classifyBounce derives a bounce class from the outcome, enhanced status, SMTP code, and
// response text. Heuristic, but stable enough for analytics.
func classifyBounce(outcome, dsn string, code int, resp string) string {
	if outcome == "delivered" {
		return "none"
	}
	r := strings.ToLower(resp)
	switch {
	case strings.Contains(r, "spam") || strings.Contains(r, "bulk") || strings.Contains(r, "junk"):
		return "spam"
	case strings.Contains(r, "blocked") || strings.Contains(r, "blacklist") ||
		strings.Contains(r, "blocklist") || strings.Contains(r, "denylist") ||
		strings.Contains(r, "reputation"):
		return "block"
	case strings.Contains(r, "spf") || strings.Contains(r, "dkim") ||
		strings.Contains(r, "dmarc") || strings.Contains(r, "authentication"):
		return "auth"
	case strings.HasPrefix(dsn, "5.7"):
		return "policy"
	}
	if outcome == "deferred" {
		return "soft"
	}
	// bounced
	if strings.HasPrefix(dsn, "5.1") || strings.Contains(r, "user unknown") ||
		strings.Contains(r, "no such user") || strings.Contains(r, "does not exist") {
		return "hard"
	}
	if code >= 500 {
		return "hard"
	}
	return "unknown"
}

// parseRelayHost extracts the hostname from a Postfix relay value like
// "mx.example.com[1.2.3.4]:25". Returns "" for "none"/"local"/empty.
func parseRelayHost(relay string) string {
	if relay == "" || relay == "none" || strings.HasPrefix(relay, "local") {
		return ""
	}
	if m := reHostIP.FindStringSubmatch(relay); m != nil {
		return strings.TrimSpace(m[1])
	}
	// strip any ":port" suffix
	if i := strings.IndexByte(relay, ':'); i > 0 {
		return relay[:i]
	}
	return relay
}

func ensureAngle(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if !strings.HasPrefix(id, "<") {
		id = "<" + id
	}
	if !strings.HasSuffix(id, ">") {
		id += ">"
	}
	return id
}

func (p *Parser) parseTime(line string) time.Time {
	m := reTS.FindStringSubmatch(line)
	if m == nil {
		return time.Now().UTC()
	}
	t, err := time.Parse("Jan _2 15:04:05", m[1])
	if err != nil {
		return time.Now().UTC()
	}
	return time.Date(p.year, t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
}

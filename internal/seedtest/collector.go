package seedtest

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// CollectResult is the outcome of polling a single seed mailbox for one probe.
type CollectResult struct {
	Found     bool
	Placement Placement   // inbox or spam when Found; unknown otherwise
	Mailbox   string      // the folder the probe was found in
	Auth      AuthResults // parsed from the delivered probe's Authentication-Results header
}

// Collector locates a probe (by its tag) in a seed mailbox and reports where it landed.
// Implementations wrap a network seam (IMAPFetcher) so tests use a fake and touch no network.
type Collector interface {
	Collect(ctx context.Context, tag string) (CollectResult, error)
}

// FetchResult is what an IMAPFetcher returns for a single search: whether a matching message
// was found, in which mailbox, and its raw header block (used to parse auth results).
type FetchResult struct {
	Found      bool
	Mailbox    string
	RawHeaders string
}

// IMAPFetcher is the injectable network seam. FetchByTag searches the given mailboxes (in
// order) of the account for a message whose X-MXS-Seed-Tag header equals tag, returning the
// first hit. The real implementation (NetIMAPFetcher) talks IMAP over TLS; tests provide a fake.
type IMAPFetcher interface {
	FetchByTag(ctx context.Context, acct IMAPAccount, mailboxes []string, tag string) (FetchResult, error)
}

// IMAPCollector is a Collector backed by an IMAP account and a fetcher.
type IMAPCollector struct {
	acct     IMAPAccount
	provider string
	fetcher  IMAPFetcher
}

// NewIMAPCollector builds a collector for one seed account. provider selects the default set
// of junk-folder names when the account does not name one explicitly. fetcher must be non-nil.
func NewIMAPCollector(acct IMAPAccount, provider string, fetcher IMAPFetcher) *IMAPCollector {
	return &IMAPCollector{acct: acct, provider: NormalizeProvider(provider), fetcher: fetcher}
}

// mailboxes returns the ordered folder list to search: INBOX first (so a probe present in both
// inbox and spam — rare — is reported as inbox), then the provider's junk folders.
func (c *IMAPCollector) mailboxes() []string {
	boxes := []string{"INBOX"}
	if s := strings.TrimSpace(c.acct.SpamMailbox); s != "" {
		boxes = append(boxes, s)
		return boxes
	}
	boxes = append(boxes, SpamFolders[c.provider]...)
	return boxes
}

// Collect searches the account's mailboxes for the probe and classifies the result.
func (c *IMAPCollector) Collect(ctx context.Context, tag string) (CollectResult, error) {
	fr, err := c.fetcher.FetchByTag(ctx, c.acct, c.mailboxes(), tag)
	if err != nil {
		return CollectResult{}, err
	}
	if !fr.Found {
		return CollectResult{Found: false, Placement: PlacementUnknown}, nil
	}
	return CollectResult{
		Found:     true,
		Placement: ClassifyMailbox(fr.Mailbox),
		Mailbox:   fr.Mailbox,
		Auth:      ParseAuthResults(extractAuthHeaders(fr.RawHeaders)...),
	}, nil
}

// authHeaderRe pulls Authentication-Results header values out of a raw header block, joining
// folded continuation lines.
var authHeaderRe = regexp.MustCompile(`(?im)^Authentication-Results:\s*(.*(?:\r?\n[ \t].*)*)`)

func extractAuthHeaders(raw string) []string {
	var out []string
	for _, m := range authHeaderRe.FindAllStringSubmatch(raw, -1) {
		v := strings.ReplaceAll(m[1], "\r\n", " ")
		v = strings.ReplaceAll(v, "\n", " ")
		out = append(out, strings.TrimSpace(v))
	}
	return out
}

// ─── Real IMAP fetcher (network seam implementation) ────────────────────────────

// NetIMAPFetcher implements IMAPFetcher over an implicit-TLS IMAP connection (port 993). It is
// a deliberately small client: SELECT each mailbox, SEARCH for the tag header, FETCH the header
// block of the newest match. It is never exercised by unit tests (those inject a fake); the
// package's tests cover the pure classification/parsing paths instead.
type NetIMAPFetcher struct {
	// Dial opens the transport. Defaults to a TLS dial when nil.
	Dial    func(ctx context.Context, host string, port int) (net.Conn, error)
	Timeout time.Duration
}

// FetchByTag runs the SELECT/SEARCH/FETCH conversation across the given mailboxes.
func (f *NetIMAPFetcher) FetchByTag(ctx context.Context, acct IMAPAccount, mailboxes []string, tag string) (FetchResult, error) {
	port := acct.Port
	if port == 0 {
		port = DefaultIMAPPort
	}
	timeout := f.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}

	dial := f.Dial
	if dial == nil {
		dial = func(_ context.Context, host string, port int) (net.Conn, error) {
			d := &net.Dialer{Timeout: timeout}
			return tls.DialWithDialer(d, "tcp", net.JoinHostPort(host, strconv.Itoa(port)), &tls.Config{ServerName: host})
		}
	}

	conn, err := dial(ctx, acct.Host, port)
	if err != nil {
		return FetchResult{}, fmt.Errorf("imap dial %s: %w", acct.Host, err)
	}
	defer conn.Close()

	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}

	cl := &imapClient{r: bufio.NewReader(conn), w: conn}
	if _, err := cl.readLine(); err != nil { // server greeting
		return FetchResult{}, fmt.Errorf("imap greeting: %w", err)
	}
	if _, ok, err := cl.exec(fmt.Sprintf("LOGIN %s %s", imapQuote(acct.Username), imapQuote(acct.Password))); err != nil || !ok {
		return FetchResult{}, fmt.Errorf("imap login: %w", errOr(err, "rejected"))
	}
	defer func() { _, _, _ = cl.exec("LOGOUT") }()

	for _, mb := range mailboxes {
		if _, ok, err := cl.exec(fmt.Sprintf("SELECT %s", imapQuote(mb))); err != nil || !ok {
			continue // mailbox may not exist for this provider; try the next
		}
		lines, ok, err := cl.exec(fmt.Sprintf(`SEARCH HEADER "%s" %s`, TagHeader, imapQuote(tag)))
		if err != nil || !ok {
			continue
		}
		ids := parseSearchIDs(lines)
		if len(ids) == 0 {
			continue
		}
		fetchLines, ok, err := cl.exec(fmt.Sprintf("FETCH %s (BODY.PEEK[HEADER])", ids[len(ids)-1]))
		if err != nil || !ok {
			continue
		}
		return FetchResult{Found: true, Mailbox: mb, RawHeaders: strings.Join(fetchLines, "\n")}, nil
	}
	return FetchResult{Found: false}, nil
}

// imapClient is a minimal tagged-command IMAP client.
type imapClient struct {
	r   *bufio.Reader
	w   io.Writer
	seq int
}

var literalRe = regexp.MustCompile(`\{(\d+)\}\s*$`)

func (c *imapClient) readLine() (string, error) {
	line, err := c.r.ReadString('\n')
	return strings.TrimRight(line, "\r\n"), err
}

// exec sends a tagged command and returns every response line up to and including the tagged
// completion, plus whether the completion was "OK". Server literals ({n}) are read inline.
func (c *imapClient) exec(cmd string) ([]string, bool, error) {
	c.seq++
	tag := fmt.Sprintf("a%03d", c.seq)
	if _, err := io.WriteString(c.w, tag+" "+cmd+"\r\n"); err != nil {
		return nil, false, err
	}
	var lines []string
	for {
		line, err := c.readLine()
		if err != nil {
			return lines, false, err
		}
		for {
			m := literalRe.FindStringSubmatch(line)
			if m == nil {
				break
			}
			n, _ := strconv.Atoi(m[1])
			buf := make([]byte, n)
			if _, err := io.ReadFull(c.r, buf); err != nil {
				return lines, false, err
			}
			cont, err := c.readLine()
			if err != nil {
				return lines, false, err
			}
			line = line + "\n" + string(buf) + cont
		}
		lines = append(lines, line)
		if strings.HasPrefix(line, tag+" ") {
			return lines, strings.Contains(line, tag+" OK"), nil
		}
	}
}

// parseSearchIDs extracts message sequence numbers from an untagged "* SEARCH ..." response.
func parseSearchIDs(lines []string) []string {
	var ids []string
	for _, l := range lines {
		u := strings.ToUpper(l)
		if !strings.HasPrefix(u, "* SEARCH") {
			continue
		}
		for _, f := range strings.Fields(l[len("* SEARCH"):]) {
			if _, err := strconv.Atoi(f); err == nil {
				ids = append(ids, f)
			}
		}
	}
	return ids
}

// imapQuote wraps a string as an IMAP quoted-string, escaping quotes/backslashes.
func imapQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

func errOr(err error, fallback string) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("%s", fallback)
}

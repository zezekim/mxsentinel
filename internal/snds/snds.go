// Package snds is the Microsoft Outlook/Hotmail deliverability engine: it polls Microsoft's
// Smart Network Data Services (SNDS) automated-data CSV for per-IP reputation (filter result,
// complaint band, spam-trap hits) and ingests Junk Mail Reporting Program (JMRP) ARF complaint
// feedback from a drop directory. It is the Microsoft mirror of internal/fbl (which covers the
// Gmail Postmaster + Google FBL half), sharing the same table shapes, daemon-loop style, and
// per-sender attribution so the two feel like one subsystem.
//
// This file holds the SNDS CSV parser (pure, table-driven-tested with fixture strings — no
// network) and a thin HTTP client that fetches the CSV keyed by the operator's SNDS access key.
package snds

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// IPDataRow is one parsed SNDS automated-data record: activity + reputation for a single
// sending IP over one activity period. Microsoft returns one row per (IP, period); we key
// storage by (IP, data_date) taking the activity-start date as the day.
type IPDataRow struct {
	IP            string    // sending IP the record describes
	DataDate      time.Time // UTC date derived from the activity-period start
	ActivityStart time.Time // raw activity-period start
	ActivityEnd   time.Time // raw activity-period end
	RcptCount     int       // RCPT commands seen
	DataCount     int       // DATA commands (messages) seen
	MsgRecipients int       // message recipients
	FilterResult  string    // GREEN | YELLOW | RED (normalized; "" if unknown)
	ComplaintBand string    // complaint-rate band as reported, e.g. "< 0.1%"
	TrapHits      int       // spam-trap hits in the trap period
	SampleHELO    string    // sample HELO/EHLO string
	SampleFrom    string    // sample MAIL FROM (envelope sender)
}

// ParseSNDS parses the header-less SNDS automated-data CSV. Column order (Microsoft's
// documented format):
//
//	IP, ActivityStart, ActivityEnd, RcptCommands, DataCommands, MessageRecipients,
//	FilterResult, ComplaintRate, TrapPeriod, TrapHits, SampleHELO, SampleMAILFROM
//
// It is tolerant: rows with fewer columns are best-effort populated and short/blank lines are
// skipped rather than failing the whole batch, because Microsoft occasionally emits partial
// rows for very-low-volume IPs. A row with an unparseable IP is skipped.
func ParseSNDS(raw []byte) ([]IPDataRow, error) {
	r := csv.NewReader(strings.NewReader(string(raw)))
	r.FieldsPerRecord = -1 // variable column counts are allowed
	r.TrimLeadingSpace = true

	var out []IPDataRow
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read snds csv: %w", err)
		}
		if len(rec) < 7 {
			continue // too short to carry a filter result — skip
		}
		ip := strings.TrimSpace(rec[0])
		if net.ParseIP(ip) == nil {
			continue // header line or garbage
		}
		start := parseSNDSTime(field(rec, 1))
		row := IPDataRow{
			IP:            ip,
			ActivityStart: start,
			ActivityEnd:   parseSNDSTime(field(rec, 2)),
			DataDate:      dayOf(start),
			RcptCount:     atoi(field(rec, 3)),
			DataCount:     atoi(field(rec, 4)),
			MsgRecipients: atoi(field(rec, 5)),
			FilterResult:  NormalizeFilterResult(field(rec, 6)),
			ComplaintBand: field(rec, 7),
			TrapHits:      atoi(field(rec, 9)),
			SampleHELO:    field(rec, 10),
			SampleFrom:    field(rec, 11),
		}
		out = append(out, row)
	}
	return out, nil
}

// NormalizeFilterResult maps Microsoft's filter verdict to the canonical GREEN/YELLOW/RED the
// dashboard badges on. Unknown/blank -> "".
func NormalizeFilterResult(s string) string {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "GREEN":
		return "GREEN"
	case "YELLOW":
		return "YELLOW"
	case "RED":
		return "RED"
	default:
		return ""
	}
}

// IsBadFilterResult reports whether a filter result should trip a critical incident. RED means
// Microsoft is actively junking/blocking mail from the IP.
func IsBadFilterResult(s string) bool {
	return NormalizeFilterResult(s) == "RED"
}

// Client fetches the SNDS automated-data CSV. Like internal/fbl.PostmasterClient it does no
// OAuth: the operator provisions a static SNDS access key (per IP range) in the Microsoft
// SNDS portal and supplies it via MXS_SNDS_KEY.
type Client struct {
	key     string
	dataURL string
	hc      *http.Client
}

// NewClient returns a client, or ok=false when no access key is configured (caller then skips
// the SNDS poll half cleanly, JMRP ingest still running).
func NewClient(key, dataURL string) (*Client, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, false
	}
	if dataURL == "" {
		dataURL = DefaultDataURL
	}
	return &Client{
		key:     key,
		dataURL: dataURL,
		hc:      &http.Client{Timeout: 30 * time.Second},
	}, true
}

// FetchData pulls and parses the current SNDS automated-data CSV.
func (c *Client) FetchData(ctx context.Context) ([]IPDataRow, error) {
	u := c.dataURL + "?key=" + url.QueryEscape(c.key)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("build snds request: %w", err)
	}
	req.Header.Set("Accept", "text/csv")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("snds request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("snds api: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20)) // 32 MiB guard
	if err != nil {
		return nil, fmt.Errorf("read snds body: %w", err)
	}
	return ParseSNDS(body)
}

// field returns the trimmed value at index i, or "" if out of range.
func field(rec []string, i int) string {
	if i < 0 || i >= len(rec) {
		return ""
	}
	return strings.TrimSpace(rec[i])
}

func atoi(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

// sndsTimeLayouts covers the date/time formats SNDS has been observed to emit. All are parsed
// as UTC (Microsoft reports in UTC).
var sndsTimeLayouts = []string{
	"1/2/2006 3:04 PM",
	"01/02/2006 3:04 PM",
	"1/2/2006 15:04",
	"1/2/06 3:04 PM",
	"2006-01-02 15:04:05",
	"2006-01-02",
	"1/2/2006",
}

func parseSNDSTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, l := range sndsTimeLayouts {
		if t, err := time.ParseInLocation(l, s, time.UTC); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// dayOf returns the UTC midnight of t, or today (UTC) when t is zero so a row with an
// unparseable timestamp still lands on the current day rather than the zero epoch.
func dayOf(t time.Time) time.Time {
	if t.IsZero() {
		t = time.Now().UTC()
	}
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

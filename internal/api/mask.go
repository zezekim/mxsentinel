package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// ViewerRole is the role whose responses are masked. Viewers are the only role handed out
// on the white-label domain, and the only one that must not learn who operates the relay.
const ViewerRole = "viewer"

// AliasRow is one persisted hostname → sequence assignment.
type AliasRow struct {
	RealHost string
	Seq      int
}

// aliasStore persists hostname → sequence assignments so an alias is stable for a given
// host forever, not just for the lifetime of one process.
type aliasStore interface {
	ListViewerAliases(ctx context.Context) ([]AliasRow, error)
	AssignViewerAlias(ctx context.Context, host string) (int, error)
}

// Masker rewrites provider-owned hostnames out of responses bound for viewer sessions.
//
// It works on the serialised body rather than on typed values because the hostnames are
// not confined to a few fields: they turn up in sending domains, SASL usernames, DMARC
// sources, relay settings, incident titles and free-text summaries, and in CSV exports.
// Rewriting at the one place every response passes through is what makes the guarantee
// hold for endpoints nobody remembered to audit — including ones added later.
//
// The mapping is stable and one-to-one, so a viewer still sees a coherent picture: the
// same server is the same "relay-04" on every page and every day. That is the difference
// between a usable deliverability dashboard and a wall of [redacted].
type Masker struct {
	store aliasStore
	log   *slog.Logger

	// base is the domain aliases are minted under, e.g. "mxsentinel.app".
	base string
	// hostRe matches a run of labels ending in one of the configured suffixes.
	hostRe *regexp.Regexp
	// suffixLabels[suffix] is how many labels that suffix has, used to bound the alias
	// key so a Message-ID like <248.sentinel.squidix.net> does not mint its own alias.
	suffixLabels map[string]int
	// brandRe catches any bare brand word left behind in free text after host rewriting.
	brandRe *regexp.Regexp

	mu    sync.RWMutex
	cache map[string]string // real host → alias host
}

// NewMasker builds a masker for the given domain suffixes (e.g. "squidix.net",
// "srvon.com"), minting aliases under base. Returns nil when no suffixes are configured,
// which disables masking entirely.
func NewMasker(store aliasStore, log *slog.Logger, base string, suffixes []string) (*Masker, error) {
	var clean []string
	labels := make(map[string]int)
	for _, s := range suffixes {
		s = strings.ToLower(strings.Trim(strings.TrimSpace(s), "."))
		if s == "" {
			continue
		}
		clean = append(clean, s)
		labels[s] = strings.Count(s, ".") + 1
	}
	if len(clean) == 0 {
		return nil, nil
	}
	if base == "" {
		return nil, fmt.Errorf("viewer mask: alias base domain is required")
	}

	// Longest suffix first so an overlapping pair (a.com, b.a.com) matches the specific one.
	quoted := make([]string, 0, len(clean))
	for _, s := range longestFirst(clean) {
		quoted = append(quoted, regexp.QuoteMeta(s))
	}
	hostRe, err := regexp.Compile(`(?i)\b(?:[a-z0-9_-]+\.)*(?:` + strings.Join(quoted, "|") + `)\b`)
	if err != nil {
		return nil, fmt.Errorf("viewer mask: host pattern: %w", err)
	}

	// The brand word is the suffix's second-level label ("squidix" from "squidix.net").
	// This sweep is deliberately NOT word-bounded: the name also turns up welded into
	// larger identifiers that the hostname pattern cannot see, such as a Seznam DMARC
	// report id ("szn_squidix.com-2026-07-23") or a report recipient
	// ("squidixtest@gmail.com"). Over-masking an unrelated word is a cosmetic cost;
	// leaking the operator's name is the failure this whole layer exists to prevent.
	brands := make(map[string]struct{})
	for _, s := range clean {
		parts := strings.Split(s, ".")
		if len(parts) >= 2 {
			brands[regexp.QuoteMeta(parts[len(parts)-2])] = struct{}{}
		}
	}
	brandList := make([]string, 0, len(brands))
	for b := range brands {
		brandList = append(brandList, b)
	}
	brandRe, err := regexp.Compile(`(?i)(?:` + strings.Join(longestFirst(brandList), "|") + `)`)
	if err != nil {
		return nil, fmt.Errorf("viewer mask: brand pattern: %w", err)
	}

	m := &Masker{
		store: store, log: log, base: strings.Trim(base, "."),
		hostRe: hostRe, suffixLabels: labels, brandRe: brandRe,
		cache: make(map[string]string),
	}
	return m, nil
}

func longestFirst(in []string) []string {
	out := append([]string(nil), in...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if len(out[j]) > len(out[i]) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// Warm preloads the existing assignments so the common case never touches the database.
func (m *Masker) Warm(ctx context.Context) error {
	if m == nil {
		return nil
	}
	rows, err := m.store.ListViewerAliases(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range rows {
		m.cache[r.RealHost] = m.aliasFor(r.Seq)
	}
	return nil
}

func (m *Masker) aliasFor(seq int) string {
	return fmt.Sprintf("relay-%02d.%s", seq, m.base)
}

// AppliesTo reports whether responses for this role must be masked.
func (m *Masker) AppliesTo(role string) bool {
	return m != nil && role == ViewerRole
}

// alias returns the stable alias for host, allocating one on first sight.
//
// A database failure must never fall through to the real hostname, so the fallback is a
// deterministic hash-derived alias: still stable for that host, still leaks nothing, and
// distinguishable from an assigned one so the logs explain any odd name a viewer reports.
func (m *Masker) alias(ctx context.Context, host string) string {
	m.mu.RLock()
	a, ok := m.cache[host]
	m.mu.RUnlock()
	if ok {
		return a
	}

	seq, err := m.store.AssignViewerAlias(ctx, host)
	if err != nil {
		m.log.Error("viewer mask: alias assignment failed, using hashed fallback",
			"host", host, "err", err)
		sum := sha256.Sum256([]byte(host))
		return fmt.Sprintf("relay-x%06d.%s", binary.BigEndian.Uint32(sum[:4])%1000000, m.base)
	}

	a = m.aliasFor(seq)
	m.mu.Lock()
	m.cache[host] = a
	m.mu.Unlock()
	return a
}

// aliasKey trims a matched host to the labels that actually identify a machine: the
// configured suffix plus one label. Anything further left (a Message-ID fragment, a
// per-message counter) is kept verbatim in front of the alias, so the mapping stays
// bounded by real hosts instead of growing a row per message.
func (m *Masker) aliasKey(host string) (prefix, key string) {
	lower := strings.ToLower(host)
	want := 0
	for suf, n := range m.suffixLabels {
		if lower == suf || strings.HasSuffix(lower, "."+suf) {
			if n+1 > want {
				want = n + 1
			}
		}
	}
	if want == 0 {
		return "", lower
	}
	labels := strings.Split(lower, ".")
	if len(labels) <= want {
		return "", lower
	}
	cut := len(labels) - want
	return strings.Join(labels[:cut], ".") + ".", strings.Join(labels[cut:], ".")
}

// Mask rewrites body for a viewer. Host runs go first; the brand sweep afterwards is a
// backstop for prose that names the provider without spelling out a hostname.
func (m *Masker) Mask(ctx context.Context, body []byte) []byte {
	if m == nil || len(body) == 0 {
		return body
	}
	out := m.hostRe.ReplaceAllFunc(body, func(match []byte) []byte {
		prefix, key := m.aliasKey(string(match))
		return []byte(prefix + m.alias(ctx, key))
	})
	return m.brandRe.ReplaceAll(out, []byte("provider"))
}

// maskableContentType reports whether a body is text we can safely rewrite. Anything else
// (an image, a signed blob) is passed through untouched — a hostname is not going to be
// hiding in it, and a blind rewrite could corrupt it.
func maskableContentType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	switch {
	case ct == "", strings.HasPrefix(ct, "text/"):
		return true
	case ct == "application/json", ct == "application/csv", ct == "application/xml":
		return true
	case strings.HasSuffix(ct, "+json"), strings.HasSuffix(ct, "+xml"):
		return true
	}
	return false
}

// bufferingWriter holds a response so it can be rewritten before it reaches the client.
type bufferingWriter struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
	wrote  bool
}

func (b *bufferingWriter) WriteHeader(code int) {
	if !b.wrote {
		b.status = code
		b.wrote = true
	}
}

func (b *bufferingWriter) Write(p []byte) (int, error) {
	if !b.wrote {
		b.status = http.StatusOK
		b.wrote = true
	}
	return b.buf.Write(p)
}

// serveMasked runs next, then rewrites the buffered body before it reaches the client.
func (s *Server) serveMasked(w http.ResponseWriter, r *http.Request, next http.Handler) {
	bw := &bufferingWriter{ResponseWriter: w, status: http.StatusOK}
	next.ServeHTTP(bw, r)

	body := bw.buf.Bytes()
	if maskableContentType(w.Header().Get("Content-Type")) {
		body = s.masker.Mask(r.Context(), body)
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(bw.status)
	if _, err := w.Write(body); err != nil {
		s.log.Warn("viewer mask: write failed", "path", r.URL.Path, "err", err)
	}
}

// maskViewerResponses rewrites provider hostnames out of everything a viewer session is
// served. It sits inside requireAuth (which resolves the role) and outside the handlers,
// so no endpoint can opt out of it by accident.
func (s *Server) maskViewerResponses(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, ok := authFromContext(r.Context())
		if !ok || !s.masker.AppliesTo(a.Role) {
			next.ServeHTTP(w, r)
			return
		}
		s.serveMasked(w, r, next)
	})
}

// maskPublicOnAliasHost masks the handful of endpoints that serve telemetry without any
// authentication — the per-tenant status page and the message-trace link. They have no
// role to key off, so the decision is made on the hostname instead: a request arriving on
// a white-label domain is masked, one on the operator's own domain is not.
//
// Without this the whole viewer guarantee is decorative: /status/<slug> on the white-label
// domain would hand the provider's hostnames to anyone who loads it, no login required.
func (s *Server) maskPublicOnAliasHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.masker == nil || s.primaryHost == "" || sameHost(r.Host, s.primaryHost) {
			next.ServeHTTP(w, r)
			return
		}
		s.serveMasked(w, r, next)
	})
}

// sameHost compares Host headers ignoring port and case.
func sameHost(a, b string) bool {
	strip := func(h string) string {
		h = strings.ToLower(strings.TrimSpace(h))
		if i := strings.LastIndexByte(h, ':'); i >= 0 && !strings.Contains(h[i:], "]") {
			h = h[:i]
		}
		return strings.Trim(h, ".")
	}
	return strip(a) == strip(b)
}

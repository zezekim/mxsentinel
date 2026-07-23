package nlquery

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zezekim/mxsentinel/internal/correlate"
	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
)

// Executor is the narrow, AGGREGATE-ONLY data surface the whitelisted tools may touch.
// It is deliberately a subset of the ClickHouse store's methods — every method returns
// grouped counts, never per-message rows and never message content. *chstore.Store
// satisfies this interface directly; tests supply a fake.
type Executor interface {
	DeliverabilityByProvider(ctx context.Context, tenantID string, since, until time.Time) ([]chstore.ProviderStats, error)
	RejectionGroups(ctx context.Context, tenantID string, since, until time.Time, limit int) ([]chstore.RejectionGroup, error)
	TopSenders(ctx context.Context, tenantID, dimension, metric string, since time.Time, limit int) ([]chstore.SenderCount, error)
	DMARCAlignmentSummary(ctx context.Context, tenantID, domain string, since, until time.Time) (chstore.DMARCAlignment, error)
}

// paramKind is the value type of a tool parameter.
type paramKind string

const (
	kindEnum   paramKind = "enum"   // one of Allowed
	kindInt    paramKind = "int"    // integer, clamped to [Min,Max]
	kindTime   paramKind = "time"   // RFC3339 timestamp
	kindString paramKind = "string" // free string (used only for identifiers like domain)
)

// Param describes one whitelisted, validated argument a tool accepts. There is NO param
// anywhere in the registry that carries message content — see forbiddenParamNames and the
// test that enforces it.
type Param struct {
	Name        string
	Kind        paramKind
	Required    bool
	Allowed     []string // for kindEnum
	Min, Max    int      // for kindInt
	Description string
}

// QueryResult is the aggregate output of one executed tool: a labelled table of rows the
// answer-composition step (and the UI) render. Every value is a count or a small
// categorical string — never a body or subject.
type QueryResult struct {
	Tool    string           `json:"tool"`
	Label   string           `json:"label"`
	Columns []string         `json:"columns"`
	Rows    []map[string]any `json:"rows"`
}

// Tool is one whitelisted analytics query the model may plan.
type Tool struct {
	Name        string
	Description string
	Params      []Param
	// run executes the tool with already-VALIDATED args (produced by ValidateArgs).
	run func(ctx context.Context, ex Executor, tenantID string, args map[string]any) (QueryResult, error)
}

// forbiddenParamNames are field names that would imply raw mail content. No tool may declare
// a parameter with any of these names; ValidateArgs also rejects them if they ever appear in
// a model-proposed arg map. This is the code-level guarantee behind the privacy design.
var forbiddenParamNames = map[string]bool{
	"body": true, "subject": true, "message_body": true, "subject_line": true,
	"content": true, "text": true, "raw": true, "headers": true, "html": true,
}

// windowDurations maps the shared "window" enum to a lookback duration.
var windowDurations = map[string]time.Duration{
	"1h":  time.Hour,
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
	"90d": 90 * 24 * time.Hour,
}

// timeWindowParams are the standard optional time-range params shared by most tools.
// Either give a "window" (relative lookback) or explicit "since"/"until" RFC3339 bounds.
var timeWindowParams = []Param{
	{Name: "window", Kind: kindEnum, Allowed: []string{"1h", "24h", "7d", "30d", "90d"},
		Description: "relative lookback window; ignored if 'since' is set"},
	{Name: "since", Kind: kindTime, Description: "RFC3339 start of range (e.g. 2026-07-14T00:00:00Z)"},
	{Name: "until", Kind: kindTime, Description: "RFC3339 end of range"},
}

// Registry is the immutable whitelist of queries the model may call. Adding a tool here is
// the ONLY way to expand what the model can reach — there is no free-form SQL path.
var Registry = buildRegistry()

func buildRegistry() []Tool {
	tools := []Tool{
		{
			Name:        "deliverability_by_provider",
			Description: "Per-receiving-provider delivery outcome counts (delivered, deferred, bounced, rejected, total) and delivered rate. Use for questions about how mail to a provider (Gmail, Yahoo, Microsoft, …) is doing or how it changed over time.",
			Params:      timeWindowParams,
			run:         runDeliverability,
		},
		{
			Name:        "rejection_reasons",
			Description: "Rejected and bounced messages grouped into normalized reasons (reputation, blocklist, auth, spam_content, rate_limit, greylist, policy, user_unknown, …) with SMTP code, provider and counts. Use for 'why is mail being rejected/bounced'.",
			Params: append([]Param{
				{Name: "limit", Kind: kindInt, Min: 1, Max: 200, Description: "max reason groups to return (default 50)"},
			}, timeWindowParams...),
			run: runRejectionReasons,
		},
		{
			Name:        "top_senders",
			Description: "Ranked top sending entities by a metric. Use for 'who is sending the most', 'which sender is getting rejected/flagged as spam'.",
			Params: []Param{
				{Name: "dimension", Kind: kindEnum, Required: true, Allowed: []string{"ip", "sender", "domain"},
					Description: "rank by egress IP, authenticated SMTP user, or envelope-sender domain"},
				{Name: "metric", Kind: kindEnum, Required: true, Allowed: []string{"volume", "spam", "rejected"},
					Description: "total volume, spam/complaint-classed, or rejected/bounced"},
				{Name: "window", Kind: kindEnum, Allowed: []string{"1h", "24h", "7d", "30d", "90d"},
					Description: "relative lookback window (default 7d)"},
				{Name: "limit", Kind: kindInt, Min: 1, Max: 100, Description: "how many to return (default 10)"},
			},
			run: runTopSenders,
		},
		{
			Name:        "dmarc_alignment",
			Description: "Aggregate DMARC pass/alignment counts (total messages, DKIM-aligned, SPF-aligned) and pass rates from received DMARC aggregate reports. Use for 'what is my DMARC pass rate' or per-domain alignment questions.",
			Params: append([]Param{
				{Name: "domain", Kind: kindString, Description: "restrict to one sending domain (optional)"},
			}, timeWindowParams...),
			run: runDMARCAlignment,
		},
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools
}

// ToolByName looks up a whitelisted tool. ok=false means the name is not in the whitelist
// (the planner rejects it — the model cannot invent tools).
func ToolByName(name string) (Tool, bool) {
	for _, t := range Registry {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// paramByName finds a declared param on a tool.
func (t Tool) paramByName(name string) (Param, bool) {
	for _, p := range t.Params {
		if p.Name == name {
			return p, true
		}
	}
	return Param{}, false
}

// ValidateArgs checks a model-proposed argument map against a tool's whitelisted params and
// returns a normalized map (enums lower-cased, ints clamped, times as time.Time). It is
// PURE (no I/O) and is the security boundary for arguments:
//
//   - unknown arg keys are REJECTED (so a forbidden field like "body"/"subject" can never
//     slip through — the whitelist only declares aggregate-safe params);
//   - missing required params are REJECTED;
//   - enum values not in Allowed are REJECTED;
//   - non-integer / unparseable-time values are REJECTED.
func ValidateArgs(t Tool, args map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for key, raw := range args {
		if forbiddenParamNames[strings.ToLower(strings.TrimSpace(key))] {
			return nil, fmt.Errorf("argument %q is not permitted: the model may never request message content", key)
		}
		p, ok := t.paramByName(key)
		if !ok {
			return nil, fmt.Errorf("unknown argument %q for tool %q", key, t.Name)
		}
		v, err := coerceParam(p, raw)
		if err != nil {
			return nil, err
		}
		if v != nil {
			out[key] = v
		}
	}
	for _, p := range t.Params {
		if p.Required {
			if _, ok := out[p.Name]; !ok {
				return nil, fmt.Errorf("missing required argument %q for tool %q", p.Name, t.Name)
			}
		}
	}
	return out, nil
}

func coerceParam(p Param, raw any) (any, error) {
	s := strings.TrimSpace(fmt.Sprintf("%v", raw))
	if s == "" || strings.EqualFold(s, "null") {
		return nil, nil // treat empty/null as "omitted"
	}
	switch p.Kind {
	case kindEnum:
		s = strings.ToLower(s)
		for _, a := range p.Allowed {
			if s == a {
				return s, nil
			}
		}
		return nil, fmt.Errorf("argument %q must be one of %v, got %q", p.Name, p.Allowed, s)
	case kindInt:
		n, err := strconv.Atoi(s)
		if err != nil {
			// JSON numbers arrive as float64 → "10" via Sprintf, but "10.0" is possible.
			if f, ferr := strconv.ParseFloat(s, 64); ferr == nil {
				n = int(f)
			} else {
				return nil, fmt.Errorf("argument %q must be an integer, got %q", p.Name, s)
			}
		}
		if p.Min != 0 && n < p.Min {
			n = p.Min
		}
		if p.Max != 0 && n > p.Max {
			n = p.Max
		}
		return n, nil
	case kindTime:
		ts, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return nil, fmt.Errorf("argument %q must be an RFC3339 timestamp, got %q", p.Name, s)
		}
		return ts, nil
	default: // kindString
		return s, nil
	}
}

// ---- shared arg helpers ----------------------------------------------------

// resolveWindow turns validated {window|since|until} args into a since/until range. An
// explicit "since" wins over "window". If neither is present the tool's default window is
// applied by the caller.
func resolveWindow(args map[string]any, def time.Duration) (since, until time.Time) {
	now := time.Now()
	if s, ok := args["since"].(time.Time); ok {
		since = s
	}
	if u, ok := args["until"].(time.Time); ok {
		until = u
	}
	if since.IsZero() {
		if w, ok := args["window"].(string); ok {
			if d, ok := windowDurations[w]; ok {
				since = now.Add(-d)
			}
		}
	}
	if since.IsZero() && def > 0 {
		since = now.Add(-def)
	}
	return since, until
}

func intArg(args map[string]any, name string, def int) int {
	if v, ok := args[name].(int); ok {
		return v
	}
	return def
}

func strArg(args map[string]any, name string) string {
	if v, ok := args[name].(string); ok {
		return v
	}
	return ""
}

// ---- tool implementations --------------------------------------------------

func runDeliverability(ctx context.Context, ex Executor, tenantID string, args map[string]any) (QueryResult, error) {
	since, until := resolveWindow(args, 7*24*time.Hour)
	stats, err := ex.DeliverabilityByProvider(ctx, tenantID, since, until)
	if err != nil {
		return QueryResult{}, err
	}
	rows := make([]map[string]any, 0, len(stats))
	for _, s := range stats {
		rate := 0.0
		if s.Total > 0 {
			rate = float64(s.Delivered) / float64(s.Total)
		}
		rows = append(rows, map[string]any{
			"provider": s.Provider, "delivered": s.Delivered, "deferred": s.Deferred,
			"bounced": s.Bounced, "rejected": s.Rejected, "total": s.Total,
			"delivered_rate": round4(rate),
		})
	}
	return QueryResult{
		Tool: "deliverability_by_provider", Label: "Deliverability by provider",
		Columns: []string{"provider", "delivered", "deferred", "bounced", "rejected", "total", "delivered_rate"},
		Rows:    rows,
	}, nil
}

func runRejectionReasons(ctx context.Context, ex Executor, tenantID string, args map[string]any) (QueryResult, error) {
	since, until := resolveWindow(args, 7*24*time.Hour)
	limit := intArg(args, "limit", 50)
	groups, err := ex.RejectionGroups(ctx, tenantID, since, until, limit)
	if err != nil {
		return QueryResult{}, err
	}
	// Aggregate the raw (code,status,provider) groups into normalized reason categories.
	type key struct{ reason, provider string }
	agg := map[key]uint64{}
	codes := map[key]uint16{}
	for _, g := range groups {
		rej := correlate.ClassifyRejection(int(g.SMTPCode), g.EnhancedStatus, g.Sample, g.Provider)
		k := key{reason: string(rej.Category), provider: g.Provider}
		agg[k] += g.Count
		codes[k] = g.SMTPCode
	}
	rows := make([]map[string]any, 0, len(agg))
	for k, count := range agg {
		rows = append(rows, map[string]any{
			"reason": k.reason, "provider": k.provider, "smtp_code": codes[k], "count": count,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i]["count"].(uint64) > rows[j]["count"].(uint64) })
	return QueryResult{
		Tool: "rejection_reasons", Label: "Rejection/bounce reasons",
		Columns: []string{"reason", "provider", "smtp_code", "count"},
		Rows:    rows,
	}, nil
}

func runTopSenders(ctx context.Context, ex Executor, tenantID string, args map[string]any) (QueryResult, error) {
	since, _ := resolveWindow(args, 7*24*time.Hour)
	dimension := strArg(args, "dimension")
	metric := strArg(args, "metric")
	limit := intArg(args, "limit", 10)
	senders, err := ex.TopSenders(ctx, tenantID, dimension, metric, since, limit)
	if err != nil {
		return QueryResult{}, err
	}
	rows := make([]map[string]any, 0, len(senders))
	for _, s := range senders {
		rows = append(rows, map[string]any{"key": s.Key, "count": s.Count})
	}
	return QueryResult{
		Tool:    "top_senders",
		Label:   fmt.Sprintf("Top senders by %s (%s)", dimension, metric),
		Columns: []string{"key", "count"},
		Rows:    rows,
	}, nil
}

func runDMARCAlignment(ctx context.Context, ex Executor, tenantID string, args map[string]any) (QueryResult, error) {
	since, until := resolveWindow(args, 30*24*time.Hour)
	domain := strArg(args, "domain")
	a, err := ex.DMARCAlignmentSummary(ctx, tenantID, domain, since, until)
	if err != nil {
		return QueryResult{}, err
	}
	dkimRate, spfRate := 0.0, 0.0
	if a.Total > 0 {
		dkimRate = float64(a.DKIMAligned) / float64(a.Total)
		spfRate = float64(a.SPFAligned) / float64(a.Total)
	}
	label := "DMARC alignment (all domains)"
	if domain != "" {
		label = "DMARC alignment for " + domain
	}
	return QueryResult{
		Tool: "dmarc_alignment", Label: label,
		Columns: []string{"total", "dkim_aligned", "spf_aligned", "dkim_pass_rate", "spf_pass_rate"},
		Rows: []map[string]any{{
			"total": a.Total, "dkim_aligned": a.DKIMAligned, "spf_aligned": a.SPFAligned,
			"dkim_pass_rate": round4(dkimRate), "spf_pass_rate": round4(spfRate),
		}},
	}, nil
}

func round4(f float64) float64 {
	return float64(int64(f*10000+0.5)) / 10000
}

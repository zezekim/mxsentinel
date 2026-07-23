package bounce

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Suppression source labels. A suppression entry records where it came from so operators
// can audit and so manual entries survive re-classification passes.
const (
	SourceBounce    = "bounce"    // auto-added by the bounced daemon from a classified bounce
	SourceComplaint = "complaint" // fed from a feedback-loop (ARF) complaint
	SourceManual    = "manual"    // added via the API by an operator
	SourceImport    = "import"    // bulk-imported (e.g. migrated from a prior list)
)

// spamSuppressTTL bounds how long a spam-block suppression lasts. Spam blocks are often
// content/reputation driven and can clear, so the recipient is re-eligible after this
// window rather than being suppressed forever (unlike a non-existent mailbox).
const spamSuppressTTL = 30 * 24 * time.Hour

// SuppressionDecision is the outcome of the auto-suppression policy for a category.
type SuppressionDecision struct {
	Suppress bool
	Reason   string        // stable machine reason, stored on the entry
	TTL      time.Duration // 0 == permanent (no expiry)
	Priority int           // higher wins when the same recipient hits multiple categories
}

// SuppressionFor returns the auto-suppression policy for a classified category. Only
// terminal, recipient-scoped failures suppress: a non-existent mailbox and invalid
// recipients are permanent; spam blocks suppress with a TTL; everything transient (soft,
// mailbox full, greylist) and everything infrastructure-scoped (reputation, generic block)
// is NOT auto-suppressed — those are relay/IP problems, not per-recipient ones.
func SuppressionFor(cat Category) SuppressionDecision {
	switch cat {
	case CategoryInvalidRecipient:
		return SuppressionDecision{Suppress: true, Reason: "invalid_recipient", TTL: 0, Priority: 30}
	case CategoryHard:
		return SuppressionDecision{Suppress: true, Reason: "hard_bounce", TTL: 0, Priority: 20}
	case CategorySpamBlock:
		return SuppressionDecision{Suppress: true, Reason: "spam_block", TTL: spamSuppressTTL, Priority: 10}
	default:
		return SuppressionDecision{Suppress: false}
	}
}

// ExpiryFor turns a decision's TTL into an absolute expiry relative to now, or nil for a
// permanent suppression.
func (d SuppressionDecision) ExpiryFor(now time.Time) *time.Time {
	if d.TTL <= 0 {
		return nil
	}
	t := now.Add(d.TTL).UTC()
	return &t
}

// SuppressionRecord is a minimal, storage-agnostic view of a suppression entry used to
// build relay-sync exports. It intentionally carries only the hash and metadata — never a
// plaintext address.
type SuppressionRecord struct {
	RecipientHash string
	Reason        string
	Category      string
	Source        string
	ExpiresAt     *time.Time
}

// Export formats for BuildExport.
const (
	ExportFormatPlain   = "plain"   // one recipient hash per line
	ExportFormatPostfix = "postfix" // Postfix access(5) map: "<hash> REJECT <text>"
)

// BuildExport renders the suppression list into a relay-syncable artifact.
//
//   - "plain": one recipient hash per line — the canonical set the relay hook loads into a
//     membership check. The relay computes the SAME keyed HMAC-SHA256 of the recipient at
//     policy time (matching internal/telemetry's hasher) and rejects on membership.
//   - "postfix": a Postfix access(5)-style table keyed by the recipient hash, one
//     "<hash> REJECT <reason text>" line per entry, suitable for a check_recipient_access
//     lookup once the relay maps the incoming recipient to its hash.
//
// Entries are emitted in a stable (sorted) order so a byte-for-byte diff between syncs is
// meaningful. A header comment records provenance; it never contains any address.
func BuildExport(format string, records []SuppressionRecord, generatedAt time.Time) string {
	sorted := make([]SuppressionRecord, 0, len(records))
	for _, r := range records {
		if strings.TrimSpace(r.RecipientHash) == "" {
			continue // never emit a blank key
		}
		sorted = append(sorted, r)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].RecipientHash < sorted[j].RecipientHash })

	var b strings.Builder
	fmt.Fprintf(&b, "# MX Sentinel suppression list (%s)\n", format)
	fmt.Fprintf(&b, "# generated_at=%s entries=%d\n", generatedAt.UTC().Format(time.RFC3339), len(sorted))
	fmt.Fprintf(&b, "# keys are HMAC-SHA256 recipient hashes; no plaintext addresses are exported\n")

	for _, r := range sorted {
		switch format {
		case ExportFormatPostfix:
			fmt.Fprintf(&b, "%s REJECT %s\n", r.RecipientHash, postfixReason(r))
		default: // plain
			fmt.Fprintf(&b, "%s\n", r.RecipientHash)
		}
	}
	return b.String()
}

// postfixReason builds the human-facing REJECT text for a Postfix access-map line, kept on
// one line (Postfix maps are line-oriented).
func postfixReason(r SuppressionRecord) string {
	reason := r.Reason
	if reason == "" {
		reason = r.Category
	}
	if reason == "" {
		reason = "suppressed"
	}
	return "MX Sentinel suppressed: " + strings.ReplaceAll(reason, "\n", " ")
}

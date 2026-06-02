// Package dmarc implements parsing of DMARC aggregate report XML (RFC 7489).
package dmarc

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"time"
)

// Report is the top-level parsed DMARC aggregate report.
type Report struct {
	OrgName   string
	Email     string
	ReportID  string
	DateBegin time.Time // UTC
	DateEnd   time.Time // UTC
	Domain    string    // from policy_published/domain
	Policy    Policy
	Records   []Record
}

// Policy holds the published DMARC policy from the report.
type Policy struct {
	P     string // none|quarantine|reject
	SP    string
	ADKIM string // r|s
	ASPF  string
	Pct   int
}

// Record represents a single aggregate report record.
type Record struct {
	SourceIP     string       // row/source_ip as string
	Count        int          // row/count
	Disposition  string       // row/policy_evaluated/disposition
	DKIMAligned  bool         // row/policy_evaluated/dkim == "pass"
	SPFAligned   bool         // row/policy_evaluated/spf  == "pass"
	HeaderFrom   string       // identifiers/header_from
	EnvelopeFrom string       // identifiers/envelope_from (may be empty)
	DKIMAuth     []AuthResult // auth_results/dkim entries
	SPFAuth      []AuthResult // auth_results/spf entries
}

// AuthResult holds a single DKIM or SPF authentication result.
type AuthResult struct {
	Domain   string
	Selector string // dkim only; empty for spf
	Result   string
}

// --- unexported XML mirror structs ---

type xmlFeedback struct {
	XMLName         xml.Name           `xml:"feedback"`
	ReportMetadata  xmlReportMetadata  `xml:"report_metadata"`
	PolicyPublished xmlPolicyPublished `xml:"policy_published"`
	Records         []xmlRecord        `xml:"record"`
}

type xmlReportMetadata struct {
	OrgName   string       `xml:"org_name"`
	Email     string       `xml:"email"`
	ReportID  string       `xml:"report_id"`
	DateRange xmlDateRange `xml:"date_range"`
}

type xmlDateRange struct {
	Begin int64 `xml:"begin"`
	End   int64 `xml:"end"`
}

type xmlPolicyPublished struct {
	Domain string `xml:"domain"`
	ADKIM  string `xml:"adkim"`
	ASPF   string `xml:"aspf"`
	P      string `xml:"p"`
	SP     string `xml:"sp"`
	Pct    int    `xml:"pct"`
}

type xmlRecord struct {
	Row         xmlRow         `xml:"row"`
	Identifiers xmlIdentifiers `xml:"identifiers"`
	AuthResults xmlAuthResults `xml:"auth_results"`
}

type xmlRow struct {
	SourceIP        string             `xml:"source_ip"`
	Count           int                `xml:"count"`
	PolicyEvaluated xmlPolicyEvaluated `xml:"policy_evaluated"`
}

type xmlPolicyEvaluated struct {
	Disposition string `xml:"disposition"`
	DKIM        string `xml:"dkim"`
	SPF         string `xml:"spf"`
}

type xmlIdentifiers struct {
	HeaderFrom   string `xml:"header_from"`
	EnvelopeFrom string `xml:"envelope_from"`
}

type xmlAuthResults struct {
	DKIM []xmlDKIMResult `xml:"dkim"`
	SPF  []xmlSPFResult  `xml:"spf"`
}

type xmlDKIMResult struct {
	Domain   string `xml:"domain"`
	Selector string `xml:"selector"`
	Result   string `xml:"result"`
}

type xmlSPFResult struct {
	Domain string `xml:"domain"`
	Result string `xml:"result"`
}

// Parse decodes a DMARC aggregate report XML from the given reader.
// It returns a non-nil error for malformed or unreadable XML.
func Parse(r io.Reader) (*Report, error) {
	var raw xmlFeedback
	dec := xml.NewDecoder(r)
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("dmarc: XML decode error: %w", err)
	}

	report := &Report{
		OrgName:   raw.ReportMetadata.OrgName,
		Email:     raw.ReportMetadata.Email,
		ReportID:  raw.ReportMetadata.ReportID,
		DateBegin: time.Unix(raw.ReportMetadata.DateRange.Begin, 0).UTC(),
		DateEnd:   time.Unix(raw.ReportMetadata.DateRange.End, 0).UTC(),
		Domain:    raw.PolicyPublished.Domain,
		Policy: Policy{
			P:     raw.PolicyPublished.P,
			SP:    raw.PolicyPublished.SP,
			ADKIM: raw.PolicyPublished.ADKIM,
			ASPF:  raw.PolicyPublished.ASPF,
			Pct:   raw.PolicyPublished.Pct,
		},
	}

	for _, xr := range raw.Records {
		rec := Record{
			SourceIP:     xr.Row.SourceIP,
			Count:        xr.Row.Count,
			Disposition:  xr.Row.PolicyEvaluated.Disposition,
			DKIMAligned:  strings.EqualFold(xr.Row.PolicyEvaluated.DKIM, "pass"),
			SPFAligned:   strings.EqualFold(xr.Row.PolicyEvaluated.SPF, "pass"),
			HeaderFrom:   xr.Identifiers.HeaderFrom,
			EnvelopeFrom: xr.Identifiers.EnvelopeFrom,
		}

		for _, d := range xr.AuthResults.DKIM {
			rec.DKIMAuth = append(rec.DKIMAuth, AuthResult{
				Domain:   d.Domain,
				Selector: d.Selector,
				Result:   d.Result,
			})
		}

		for _, s := range xr.AuthResults.SPF {
			rec.SPFAuth = append(rec.SPFAuth, AuthResult{
				Domain: s.Domain,
				Result: s.Result,
			})
		}

		report.Records = append(report.Records, rec)
	}

	return report, nil
}

// ParseBytes is a convenience wrapper around Parse that accepts a byte slice.
func ParseBytes(b []byte) (*Report, error) {
	return Parse(strings.NewReader(string(b)))
}

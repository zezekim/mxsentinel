package clickhouse

import (
	"context"
	"fmt"
	"time"
)

// DMARCDomainPassRate holds the aggregate DMARC pass rate for a single domain over
// a window. Total is the message volume (weighted by count), Pass the messages where
// DKIM or SPF was aligned (the DMARC pass condition), and PassRate = Pass/Total
// (0 when Total is 0).
type DMARCDomainPassRate struct {
	Domain   string
	Total    uint64
	Pass     uint64
	PassRate float64
}

// DMARCDomainPassRates returns the per-domain DMARC pass rate for the tenant over
// records whose date_begin is at or after since. Domains whose message volume is
// below minMessages are excluded so noisy low-volume domains do not trigger alerts.
// Domains are returned worst (lowest pass rate) first. This is the evaluation source
// for the "dmarc_pass_rate" alert signal.
func (s *Store) DMARCDomainPassRates(ctx context.Context, tenantID string, since time.Time, minMessages int) ([]DMARCDomainPassRate, error) {
	if minMessages < 0 {
		minMessages = 0
	}
	const q = `SELECT domain,
	                  sum(count) AS total,
	                  sum(count * toUInt8(dkim_aligned = 1 OR spf_aligned = 1)) AS pass
	           FROM dmarc_records FINAL
	           WHERE tenant_id = ?
	             AND date_begin >= ?
	           GROUP BY domain
	           HAVING total >= ?
	           ORDER BY pass / total ASC, total DESC`

	rows, err := s.conn.Query(ctx, q, tenantID, since, uint64(minMessages))
	if err != nil {
		return nil, fmt.Errorf("dmarc domain pass rates: %w", err)
	}
	defer rows.Close()

	var out []DMARCDomainPassRate
	for rows.Next() {
		var d DMARCDomainPassRate
		if err := rows.Scan(&d.Domain, &d.Total, &d.Pass); err != nil {
			return nil, fmt.Errorf("scan dmarc domain pass rate: %w", err)
		}
		if d.Total > 0 {
			d.PassRate = float64(d.Pass) / float64(d.Total)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("dmarc domain pass rates: %w", err)
	}
	return out, nil
}

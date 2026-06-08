package rbl

import (
	"context"
	"time"
)

// ZoneStatus is one zone's listing state for an IP, as exposed by the API.
type ZoneStatus struct {
	Zone        string     `json:"zone"`
	Listed      bool       `json:"listed"`
	Reason      string     `json:"reason,omitempty"`
	ListedSince *time.Time `json:"listed_since,omitempty"`
}

// IPStatus is one egress IP's aggregate health plus its per-zone listings.
type IPStatus struct {
	IP       string       `json:"ip"`
	Healthy  bool         `json:"healthy"` // listed on zero zones AND checked at least once
	Checked  bool         `json:"checked"` // has at least one recorded result
	Listings []ZoneStatus `json:"listings"`
}

// Summary is the top-level rollup.
type Summary struct {
	TotalIPs int `json:"total_ips"`
	Healthy  int `json:"healthy"`
	Listed   int `json:"listed"`
}

// Status is the full payload returned by GET /v1/rbl/status.
type Status struct {
	CheckedAt *time.Time `json:"checked_at"`
	Summary   Summary    `json:"summary"`
	IPs       []IPStatus `json:"ips"`
}

// BuildStatus reads ip_blocklist_status and assembles the API view. The IP universe is the
// configured egress set (so an IP with no rows yet still appears, marked unchecked/unhealthy);
// rows for IPs not in `configured` are ignored. checked_at is the most recent checked_at seen.
func BuildStatus(ctx context.Context, store *Store, configured []string) (Status, error) {
	rows, err := store.List(ctx)
	if err != nil {
		return Status{}, err
	}

	byIP := make(map[string][]Row)
	for _, r := range rows {
		byIP[r.IP] = append(byIP[r.IP], r)
	}

	var latest *time.Time
	st := Status{IPs: make([]IPStatus, 0, len(configured))}
	for _, ip := range configured {
		ipRows := byIP[ip]
		ips := IPStatus{IP: ip, Listings: make([]ZoneStatus, 0, len(ipRows))}
		listedAny := false
		for _, r := range ipRows {
			ips.Checked = true
			if r.Listed {
				listedAny = true
			}
			zs := ZoneStatus{Zone: r.Zone, Listed: r.Listed, Reason: r.Reason, ListedSince: r.ListedSince}
			ips.Listings = append(ips.Listings, zs)
			if latest == nil || r.CheckedAt.After(*latest) {
				t := r.CheckedAt
				latest = &t
			}
		}
		ips.Healthy = ips.Checked && !listedAny
		st.IPs = append(st.IPs, ips)

		st.Summary.TotalIPs++
		if listedAny {
			st.Summary.Listed++
		} else if ips.Healthy {
			st.Summary.Healthy++
		}
	}
	st.CheckedAt = latest
	return st, nil
}

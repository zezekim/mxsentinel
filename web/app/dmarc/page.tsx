"use client";

import { useEffect, useState, useCallback } from "react";
import { apiGet, type DmarcReportsResponse, type DmarcReport, type DmarcAlignment } from "@/lib/api";
import LoadingError from "@/components/LoadingError";

function fmt(ts: string): string {
  if (!ts) return "—";
  try {
    return new Date(ts).toLocaleDateString();
  } catch {
    return ts;
  }
}

function pct(n: number): string {
  return `${(n * 100).toFixed(1)}%`;
}

export default function DmarcPage() {
  const [domainFilter, setDomainFilter] = useState("");
  const [reports, setReports] = useState<DmarcReport[]>([]);
  const [alignment, setAlignment] = useState<DmarcAlignment | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const fetchReports = useCallback((domain: string) => {
    setLoading(true);
    setError(null);
    const params = new URLSearchParams({ limit: "50" });
    if (domain) params.set("domain", domain);
    apiGet<DmarcReportsResponse>(`/v1/dmarc/reports?${params.toString()}`)
      .then((d) => {
        setReports(d.reports);
        setAlignment(d.alignment);
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    fetchReports("");
  }, [fetchReports]);

  function handleSearch(e: React.FormEvent) {
    e.preventDefault();
    fetchReports(domainFilter);
  }

  return (
    <>
      <h1>DMARC Reports</h1>

      <form className="filters" onSubmit={handleSearch}>
        <input
          type="text"
          placeholder="Filter by domain"
          value={domainFilter}
          onChange={(e) => setDomainFilter(e.target.value)}
        />
        <button type="submit">Filter</button>
      </form>

      <LoadingError loading={loading} error={error} />

      {!loading && !error && (
        <>
          {alignment && (
            <>
              <h2>Alignment Summary</h2>
              <div className="stat-cards">
                <div className="stat-card">
                  <div className="label">Total Messages</div>
                  <div className="value">{alignment.total.toLocaleString()}</div>
                </div>
                <div className="stat-card">
                  <div className="label">DKIM Pass Rate</div>
                  <div className="value" style={{ color: alignment.dkim_pass_rate >= 0.95 ? "#166534" : alignment.dkim_pass_rate >= 0.8 ? "#854d0e" : "#991b1b" }}>
                    {pct(alignment.dkim_pass_rate)}
                  </div>
                </div>
                <div className="stat-card">
                  <div className="label">SPF Pass Rate</div>
                  <div className="value" style={{ color: alignment.spf_pass_rate >= 0.95 ? "#166534" : alignment.spf_pass_rate >= 0.8 ? "#854d0e" : "#991b1b" }}>
                    {pct(alignment.spf_pass_rate)}
                  </div>
                </div>
                <div className="stat-card">
                  <div className="label">DKIM Aligned</div>
                  <div className="value">{alignment.dkim_aligned.toLocaleString()}</div>
                </div>
                <div className="stat-card">
                  <div className="label">SPF Aligned</div>
                  <div className="value">{alignment.spf_aligned.toLocaleString()}</div>
                </div>
              </div>
            </>
          )}

          <h2>Reports</h2>
          {reports.length === 0 ? (
            <p className="no-results">No DMARC reports found.</p>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Organisation</th>
                    <th>Domain</th>
                    <th>Report ID</th>
                    <th>Date Begin</th>
                    <th>Date End</th>
                    <th>Records</th>
                  </tr>
                </thead>
                <tbody>
                  {reports.map((r) => (
                    <tr key={r.id}>
                      <td>{r.org_name}</td>
                      <td>{r.domain}</td>
                      <td>
                        <span
                          style={{
                            fontSize: "0.75rem",
                            maxWidth: "160px",
                            display: "inline-block",
                            overflow: "hidden",
                            textOverflow: "ellipsis",
                            whiteSpace: "nowrap",
                            verticalAlign: "middle",
                          }}
                          title={r.report_id}
                        >
                          {r.report_id}
                        </span>
                      </td>
                      <td style={{ whiteSpace: "nowrap" }}>{fmt(r.date_begin)}</td>
                      <td style={{ whiteSpace: "nowrap" }}>{fmt(r.date_end)}</td>
                      <td>{r.record_count}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
    </>
  );
}

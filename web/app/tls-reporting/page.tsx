"use client";

import { useCallback, useEffect, useState } from "react";
import {
  getMTASTSDomains,
  getTLSRPTReports,
  type MTASTSDomain,
  type TLSRPTReport,
  type TLSRPTSummary,
} from "@/lib/api-tlsreporting";
import LoadingError from "@/components/LoadingError";

function fmtDate(ts: string): string {
  if (!ts) return "—";
  try {
    return new Date(ts).toLocaleDateString();
  } catch {
    return ts;
  }
}

function fmtDateTime(ts?: string): string {
  if (!ts) return "—";
  try {
    return new Date(ts).toLocaleString();
  } catch {
    return ts;
  }
}

function pct(n: number): string {
  return `${(n * 100).toFixed(1)}%`;
}

// Maps an MTA-STS mode + health to one of the shared badge classes.
function modeBadge(mode: string, healthy: boolean): { cls: string; label: string } {
  if (!mode) return { cls: "badge-critical", label: "no policy" };
  if (!healthy) return { cls: "badge-critical", label: mode };
  if (mode === "enforce") return { cls: "badge-ok", label: "enforce" };
  if (mode === "testing") return { cls: "badge-warning", label: "testing" };
  return { cls: "badge-warning", label: mode };
}

function certBadge(expiry?: string): { cls: string; label: string } {
  if (!expiry) return { cls: "badge-unknown", label: "—" };
  const days = (new Date(expiry).getTime() - Date.now()) / 86_400_000;
  const label = new Date(expiry).toLocaleDateString();
  if (days < 0) return { cls: "badge-critical", label: `expired ${label}` };
  if (days <= 14) return { cls: "badge-warning", label };
  return { cls: "badge-ok", label };
}

export default function TLSReportingPage() {
  const [domains, setDomains] = useState<MTASTSDomain[]>([]);
  const [reports, setReports] = useState<TLSRPTReport[]>([]);
  const [summary, setSummary] = useState<TLSRPTSummary | null>(null);
  const [domainFilter, setDomainFilter] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const fetchData = useCallback((domain: string) => {
    setLoading(true);
    setError(null);
    Promise.all([getMTASTSDomains(), getTLSRPTReports(domain)])
      .then(([mta, tls]) => {
        setDomains(mta.domains);
        setReports(tls.reports);
        setSummary(tls.summary);
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    fetchData("");
  }, [fetchData]);

  function handleSearch(e: React.FormEvent) {
    e.preventDefault();
    fetchData(domainFilter);
  }

  return (
    <>
      <h1>TLS Reporting</h1>
      <p className="subtitle">MTA-STS policy health and inbound TLS-RPT reports (RFC 8460 / 8461).</p>

      <LoadingError loading={loading} error={error} />

      {!loading && !error && (
        <>
          <h2>MTA-STS Policy State</h2>
          {domains.length === 0 ? (
            <p className="no-results">No MTA-STS snapshots yet. tlsrptd populates this once it polls your domains.</p>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Domain</th>
                    <th>Mode</th>
                    <th>MX Hosts</th>
                    <th>Max Age</th>
                    <th>Cert Expiry</th>
                    <th>Findings</th>
                    <th>Checked</th>
                  </tr>
                </thead>
                <tbody>
                  {domains.map((d) => {
                    const mb = modeBadge(d.mode, d.healthy);
                    const cb = certBadge(d.cert_expiry);
                    const alertable = (d.findings ?? []).filter(
                      (f) => f.severity === "warning" || f.severity === "critical",
                    );
                    return (
                      <tr key={d.id}>
                        <td>{d.domain}</td>
                        <td><span className={`badge ${mb.cls}`}>{mb.label}</span></td>
                        <td style={{ fontSize: "0.8rem" }}>{d.mx_hosts.join(", ") || "—"}</td>
                        <td>{d.max_age ? `${d.max_age}s` : "—"}</td>
                        <td><span className={`badge ${cb.cls}`}>{cb.label}</span></td>
                        <td>
                          {alertable.length === 0 ? (
                            <span className="badge badge-ok">ok</span>
                          ) : (
                            <span
                              className="badge badge-critical"
                              title={alertable.map((f) => `${f.code}: ${f.message}`).join("\n")}
                            >
                              {alertable.length} issue{alertable.length > 1 ? "s" : ""}
                            </span>
                          )}
                        </td>
                        <td style={{ whiteSpace: "nowrap" }}>{fmtDateTime(d.captured_at)}</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}

          {summary && (
            <>
              <h2>TLS Session Summary</h2>
              <div className="stat-cards">
                <div className="stat-card">
                  <div className="label">Successful Sessions</div>
                  <div className="value">{summary.success.toLocaleString()}</div>
                </div>
                <div className="stat-card">
                  <div className="label">Failed Sessions</div>
                  <div className="value">{summary.failure.toLocaleString()}</div>
                </div>
                <div className="stat-card">
                  <div className="label">Success Rate</div>
                  <div
                    className="value"
                    style={{ color: summary.success_rate >= 0.99 ? "#166534" : summary.success_rate >= 0.9 ? "#854d0e" : "#991b1b" }}
                  >
                    {summary.success + summary.failure > 0 ? pct(summary.success_rate) : "—"}
                  </div>
                </div>
              </div>
              {summary.by_type.length > 0 && (
                <div className="table-wrap" style={{ marginTop: "0.5rem" }}>
                  <table>
                    <thead>
                      <tr>
                        <th>Failure Type</th>
                        <th>Failed Sessions</th>
                      </tr>
                    </thead>
                    <tbody>
                      {summary.by_type.map((t) => (
                        <tr key={t.result_type}>
                          <td>{t.result_type}</td>
                          <td>{t.failures.toLocaleString()}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </>
          )}

          <h2>TLS-RPT Reports</h2>
          <form className="filters" onSubmit={handleSearch}>
            <input
              type="text"
              placeholder="Filter by domain"
              value={domainFilter}
              onChange={(e) => setDomainFilter(e.target.value)}
            />
            <button type="submit">Filter</button>
          </form>
          {reports.length === 0 ? (
            <p className="no-results">No TLS-RPT reports found.</p>
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
                    <th>Success</th>
                    <th>Failure</th>
                  </tr>
                </thead>
                <tbody>
                  {reports.map((r) => (
                    <tr key={r.id}>
                      <td>{r.org_name || "—"}</td>
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
                      <td style={{ whiteSpace: "nowrap" }}>{fmtDate(r.date_begin)}</td>
                      <td style={{ whiteSpace: "nowrap" }}>{fmtDate(r.date_end)}</td>
                      <td>{r.success_count.toLocaleString()}</td>
                      <td style={{ color: r.failure_count > 0 ? "#991b1b" : undefined }}>
                        {r.failure_count.toLocaleString()}
                      </td>
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

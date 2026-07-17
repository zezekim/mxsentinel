"use client";

import { useEffect, useState, useCallback, Fragment } from "react";
import {
  apiGet,
  getToken,
  type DmarcReportsResponse,
  type DmarcReport,
  type DmarcAlignment,
  type DmarcRecord,
  type DmarcRecordsResponse,
  type DmarcAnalyticsResponse,
} from "@/lib/api";
import LoadingError from "@/components/LoadingError";
import TrendChart from "./TrendChart";
import TopSources from "./TopSources";
import Threats from "./Threats";
import PolicyAdvisor from "./PolicyAdvisor";

const API_BASE = process.env.NEXT_PUBLIC_API_BASE ?? "http://localhost:8080";

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

function reportStatus(r: DmarcReport): "pass" | "fail" | "mixed" | "unknown" {
  if (r.pass_count === 0 && r.fail_count === 0) return "unknown";
  if (r.fail_count === 0) return "pass";
  if (r.pass_count === 0) return "fail";
  return "mixed";
}

const statusBadge: Record<string, { cls: string; label: string }> = {
  pass: { cls: "badge badge-ok", label: "Pass" },
  fail: { cls: "badge badge-critical", label: "Fail" },
  mixed: { cls: "badge badge-warning", label: "Mixed" },
  unknown: { cls: "badge badge-unknown", label: "—" },
};

// Convert a yyyy-mm-dd date input to an RFC3339 timestamp; "" stays "".
function sinceISO(d: string): string {
  return d ? new Date(`${d}T00:00:00`).toISOString() : "";
}
function untilISO(d: string): string {
  return d ? new Date(`${d}T23:59:59.999`).toISOString() : "";
}

// authDownload fetches an authenticated endpoint and triggers a browser download
// of the response body. A plain <a href> would not carry the Bearer token.
async function authDownload(path: string, filename: string): Promise<void> {
  const token = getToken();
  const res = await fetch(`${API_BASE}${path}`, {
    headers: token ? { Authorization: `Bearer ${token}` } : {},
    cache: "no-store",
  });
  if (!res.ok) {
    throw new Error(`HTTP ${res.status}`);
  }
  const blob = await res.blob();
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

function AlignBadge({ ok }: { ok: boolean }) {
  return (
    <span style={{ color: ok ? "#166534" : "#991b1b", fontWeight: 600 }}>
      {ok ? "pass" : "fail"}
    </span>
  );
}

function RecordRows({ reportId }: { reportId: string }) {
  const [records, setRecords] = useState<DmarcRecord[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    apiGet<DmarcRecordsResponse>(`/v1/dmarc/reports/${reportId}/records`)
      .then((d) => setRecords(d.records))
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)));
  }, [reportId]);

  if (error) return <p className="no-results">Failed to load records: {error}</p>;
  if (records === null) return <p className="no-results">Loading records…</p>;
  if (records.length === 0)
    return <p className="no-results">No per-source records stored for this report.</p>;

  return (
    <table>
      <thead>
        <tr>
          <th>Source IP</th>
          <th style={{ textAlign: "right" }}>Messages</th>
          <th>Disposition</th>
          <th style={{ textAlign: "center" }}>DKIM Align</th>
          <th style={{ textAlign: "center" }}>SPF Align</th>
          <th>Header From</th>
          <th>Envelope From</th>
          <th style={{ textAlign: "center" }}>Status</th>
        </tr>
      </thead>
      <tbody>
        {records.map((rec, i) => {
          const passes = rec.dkim_aligned || rec.spf_aligned;
          return (
            <tr key={`${rec.source_ip}-${i}`} style={{ background: passes ? "#f0fdf4" : "#fef2f2" }}>
              <td style={{ fontFamily: "var(--font-mono, monospace)" }}>{rec.source_ip}</td>
              <td style={{ textAlign: "right", fontFamily: "var(--font-mono, monospace)" }}>
                {rec.count.toLocaleString()}
              </td>
              <td>{rec.disposition}</td>
              <td style={{ textAlign: "center" }}>
                <AlignBadge ok={rec.dkim_aligned} />
              </td>
              <td style={{ textAlign: "center" }}>
                <AlignBadge ok={rec.spf_aligned} />
              </td>
              <td>{rec.header_from || "—"}</td>
              <td>{rec.envelope_from || "—"}</td>
              <td style={{ textAlign: "center" }}>
                <span
                  style={{
                    width: "0.6rem",
                    height: "0.6rem",
                    borderRadius: "9999px",
                    display: "inline-block",
                    background: passes ? "#22c55e" : "#ef4444",
                  }}
                />
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

type SortKey = "date" | "pass" | "fail" | "total";
type SortOrder = "asc" | "desc";

type TabKey = "reports" | "analytics" | "trend" | "sources" | "threats" | "policy";
const TABS: { key: TabKey; label: string }[] = [
  { key: "reports", label: "Reports" },
  { key: "analytics", label: "Analytics" },
  { key: "trend", label: "Trend" },
  { key: "sources", label: "Top Sources" },
  { key: "threats", label: "Threats" },
  { key: "policy", label: "Policy Advisor" },
];

const filterLabelStyle: React.CSSProperties = {
  display: "flex",
  flexDirection: "column",
  fontSize: "0.75rem",
  color: "#475569",
};

export default function DmarcPage() {
  // Which section is visible. Each tab owns its own data fetch / panel.
  const [tab, setTab] = useState<TabKey>("reports");

  // Shared filter state, applied across reports, analytics, and the feature panels.
  const [domainFilter, setDomainFilter] = useState("");
  const [domainInput, setDomainInput] = useState("");
  const [sinceDate, setSinceDate] = useState("");
  const [untilDate, setUntilDate] = useState("");
  const [statusFilter, setStatusFilter] = useState("");
  const [orgFilter, setOrgFilter] = useState("");
  const [sortKey, setSortKey] = useState<SortKey>("date");
  const [sortOrder, setSortOrder] = useState<SortOrder>("desc");

  const [reports, setReports] = useState<DmarcReport[]>([]);
  const [alignment, setAlignment] = useState<DmarcAlignment | null>(null);
  const [analytics, setAnalytics] = useState<DmarcAnalyticsResponse | null>(null);
  const [analyticsError, setAnalyticsError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [expanded, setExpanded] = useState<string | null>(null);
  const [exporting, setExporting] = useState(false);
  const [exportError, setExportError] = useState<string | null>(null);

  // RFC3339 bounds derived from the date inputs, threaded into every fetch.
  const since = sinceISO(sinceDate);
  const until = untilISO(untilDate);

  // Build the shared query string used by reports, the CSV export, and (subset) analytics.
  const reportsQuery = useCallback(
    (extra?: Record<string, string>): string => {
      const params = new URLSearchParams({ limit: "50" });
      if (domainFilter) params.set("domain", domainFilter);
      if (since) params.set("since", since);
      if (until) params.set("until", until);
      if (statusFilter) params.set("status", statusFilter);
      if (orgFilter) params.set("org", orgFilter);
      params.set("sort", sortKey);
      params.set("order", sortOrder);
      if (extra) {
        for (const [k, v] of Object.entries(extra)) params.set(k, v);
      }
      return params.toString();
    },
    [domainFilter, since, until, statusFilter, orgFilter, sortKey, sortOrder],
  );

  const fetchReports = useCallback(() => {
    setLoading(true);
    setError(null);
    setExpanded(null);
    apiGet<DmarcReportsResponse>(`/v1/dmarc/reports?${reportsQuery()}`)
      .then((d) => {
        setReports(d.reports);
        setAlignment(d.alignment);
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, [reportsQuery]);

  useEffect(() => {
    fetchReports();
  }, [fetchReports]);

  useEffect(() => {
    // Best-effort: analytics failures must not break the reports table. Analytics
    // accepts only since/until.
    const params = new URLSearchParams();
    if (since) params.set("since", since);
    if (until) params.set("until", until);
    const qs = params.toString();
    apiGet<DmarcAnalyticsResponse>(`/v1/dmarc/analytics${qs ? `?${qs}` : ""}`)
      .then((d) => {
        setAnalytics(d);
        setAnalyticsError(null);
      })
      .catch((e: unknown) =>
        setAnalyticsError(e instanceof Error ? e.message : String(e))
      );
  }, [since, until]);

  function handleSearch(e: React.FormEvent) {
    e.preventDefault();
    setDomainFilter(domainInput.trim());
  }

  function toggleSort(key: SortKey) {
    if (sortKey === key) {
      setSortOrder((o) => (o === "asc" ? "desc" : "asc"));
    } else {
      setSortKey(key);
      setSortOrder("desc");
    }
  }

  function sortIndicator(key: SortKey): string {
    if (sortKey !== key) return "";
    return sortOrder === "asc" ? " ▲" : " ▼";
  }

  async function exportReportsCSV() {
    setExporting(true);
    setExportError(null);
    try {
      // The CSV endpoint ignores limit/sort but accepts the same filters.
      const params = new URLSearchParams();
      if (domainFilter) params.set("domain", domainFilter);
      if (since) params.set("since", since);
      if (until) params.set("until", until);
      if (statusFilter) params.set("status", statusFilter);
      if (orgFilter) params.set("org", orgFilter);
      const qs = params.toString();
      await authDownload(
        `/v1/dmarc/reports.csv${qs ? `?${qs}` : ""}`,
        "dmarc-reports.csv",
      );
    } catch (e: unknown) {
      setExportError(e instanceof Error ? e.message : String(e));
    } finally {
      setExporting(false);
    }
  }

  async function exportRecordsCSV(reportId: string) {
    try {
      await authDownload(
        `/v1/dmarc/reports/${reportId}/records.csv`,
        `dmarc-records-${reportId}.csv`,
      );
    } catch (e: unknown) {
      setExportError(e instanceof Error ? e.message : String(e));
    }
  }

  const sortableHeaderStyle: React.CSSProperties = {
    cursor: "pointer",
    userSelect: "none",
    whiteSpace: "nowrap",
  };

  return (
    <>
      <h1>DMARC Reports</h1>

      <form
        className="filters"
        onSubmit={handleSearch}
        style={{ display: "flex", flexWrap: "wrap", gap: "0.75rem", alignItems: "flex-end" }}
      >
        <label style={filterLabelStyle}>
          Domain
          <input
            type="text"
            placeholder="Filter by domain"
            value={domainInput}
            onChange={(e) => setDomainInput(e.target.value)}
          />
        </label>
        <label style={filterLabelStyle}>
          From
          <input
            type="date"
            value={sinceDate}
            onChange={(e) => setSinceDate(e.target.value)}
          />
        </label>
        <label style={filterLabelStyle}>
          To
          <input
            type="date"
            value={untilDate}
            onChange={(e) => setUntilDate(e.target.value)}
          />
        </label>
        <label style={filterLabelStyle}>
          Status
          <select value={statusFilter} onChange={(e) => setStatusFilter(e.target.value)}>
            <option value="">All</option>
            <option value="pass">Pass</option>
            <option value="fail">Fail</option>
            <option value="mixed">Mixed</option>
          </select>
        </label>
        <label style={filterLabelStyle}>
          Organisation
          <input
            type="text"
            placeholder="Exact org name"
            value={orgFilter}
            onChange={(e) => setOrgFilter(e.target.value)}
          />
        </label>
        <button type="submit">Filter</button>
      </form>

      <div className="tabs" role="tablist">
        {TABS.map((t) => (
          <button
            key={t.key}
            type="button"
            role="tab"
            aria-selected={tab === t.key}
            className={`tab-btn ${tab === t.key ? "tab-active" : ""}`}
            onClick={() => setTab(t.key)}
          >
            {t.label}
          </button>
        ))}
      </div>

      {tab === "reports" && (
        <>
          <div
            style={{
              display: "flex",
              alignItems: "center",
              gap: "0.75rem",
              marginBottom: "0.75rem",
              flexWrap: "wrap",
            }}
          >
            <button type="button" onClick={exportReportsCSV} disabled={exporting}>
              {exporting ? "Exporting…" : "Export reports CSV"}
            </button>
            {exportError && (
              <span className="no-results" style={{ color: "#991b1b" }}>
                Export failed: {exportError}
              </span>
            )}
          </div>

          <LoadingError loading={loading} error={error} />

          {!loading && !error && (
            reports.length === 0 ? (
              <p className="no-results">No DMARC reports found.</p>
            ) : (
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th></th>
                      <th>Organisation</th>
                      <th>Domain</th>
                      <th style={sortableHeaderStyle} onClick={() => toggleSort("date")}>
                        Date Begin{sortIndicator("date")}
                      </th>
                      <th>Date End</th>
                      <th style={{ ...sortableHeaderStyle, textAlign: "right" }} onClick={() => toggleSort("pass")}>
                        Pass{sortIndicator("pass")}
                      </th>
                      <th style={{ ...sortableHeaderStyle, textAlign: "right" }} onClick={() => toggleSort("fail")}>
                        Fail{sortIndicator("fail")}
                      </th>
                      <th style={{ ...sortableHeaderStyle, textAlign: "right" }} onClick={() => toggleSort("total")}>
                        Total{sortIndicator("total")}
                      </th>
                      <th style={{ textAlign: "center" }}>Status</th>
                    </tr>
                  </thead>
                  <tbody>
                    {reports.map((r) => {
                      const st = statusBadge[reportStatus(r)];
                      const isOpen = expanded === r.id;
                      return (
                        <Fragment key={r.id}>
                          <tr
                            onClick={() => setExpanded(isOpen ? null : r.id)}
                            style={{ cursor: "pointer" }}
                            title={r.report_id}
                          >
                            <td style={{ width: "1.5rem", color: "#94a3b8" }}>{isOpen ? "▾" : "▸"}</td>
                            <td>{r.org_name}</td>
                            <td>{r.domain}</td>
                            <td style={{ whiteSpace: "nowrap" }}>{fmt(r.date_begin)}</td>
                            <td style={{ whiteSpace: "nowrap" }}>{fmt(r.date_end)}</td>
                            <td style={{ textAlign: "right", color: "#166534", fontFamily: "var(--font-mono, monospace)" }}>
                              {r.pass_count.toLocaleString()}
                            </td>
                            <td style={{ textAlign: "right", color: "#991b1b", fontFamily: "var(--font-mono, monospace)" }}>
                              {r.fail_count.toLocaleString()}
                            </td>
                            <td style={{ textAlign: "right", fontFamily: "var(--font-mono, monospace)" }}>
                              {(r.pass_count + r.fail_count).toLocaleString()}
                            </td>
                            <td style={{ textAlign: "center" }}>
                              <span className={st.cls}>{st.label}</span>
                            </td>
                          </tr>
                          {isOpen && (
                            <tr>
                              <td colSpan={9} style={{ padding: "0.75rem 1rem", background: "#f8fafc" }}>
                                <div
                                  style={{
                                    fontSize: "0.75rem",
                                    color: "#64748b",
                                    marginBottom: "0.5rem",
                                    display: "flex",
                                    alignItems: "center",
                                    justifyContent: "space-between",
                                    gap: "0.75rem",
                                    flexWrap: "wrap",
                                  }}
                                >
                                  <span>
                                    Report ID:{" "}
                                    <span style={{ fontFamily: "var(--font-mono, monospace)" }}>{r.report_id}</span>
                                    {" · "}
                                    {r.record_count} record{r.record_count === 1 ? "" : "s"}
                                  </span>
                                  <button
                                    type="button"
                                    onClick={(e) => {
                                      e.stopPropagation();
                                      exportRecordsCSV(r.id);
                                    }}
                                  >
                                    Export records CSV
                                  </button>
                                </div>
                                <RecordRows reportId={r.id} />
                              </td>
                            </tr>
                          )}
                        </Fragment>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )
          )}
        </>
      )}

      {tab === "analytics" && (
        analyticsError ? (
          <p className="no-results">Failed to load analytics: {analyticsError}</p>
        ) : analytics === null ? (
          <p className="no-results">Loading analytics…</p>
        ) : (
          <>
            <div className="stat-cards">
              <div className="stat-card">
                <div className="label">Domains</div>
                <div className="value">{analytics.stats.domains.toLocaleString()}</div>
              </div>
              <div className="stat-card">
                <div className="label">Reports</div>
                <div className="value">{analytics.stats.reports.toLocaleString()}</div>
              </div>
              <div className="stat-card">
                <div className="label">Total Messages</div>
                <div className="value">{analytics.stats.messages.toLocaleString()}</div>
              </div>
              <div className="stat-card">
                <div className="label">Pass Rate</div>
                <div
                  className="value"
                  style={{
                    color:
                      analytics.stats.pass_rate >= 0.9
                        ? "#166534"
                        : analytics.stats.pass_rate >= 0.7
                          ? "#854d0e"
                          : "#991b1b",
                  }}
                >
                  {pct(analytics.stats.pass_rate)}
                </div>
                <div className="sub">
                  {analytics.stats.pass_messages.toLocaleString()} pass /{" "}
                  {analytics.stats.fail_messages.toLocaleString()} fail
                </div>
              </div>
              {alignment && (
                <>
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
                </>
              )}
            </div>

            <div style={{ marginBottom: "1.75rem" }}>
              <div
                style={{
                  fontSize: "0.8rem",
                  fontWeight: 600,
                  color: "#475569",
                  marginBottom: "0.35rem",
                }}
              >
                {pct(analytics.stats.pass_rate)} pass
              </div>
              <div
                style={{
                  height: "0.75rem",
                  borderRadius: "9999px",
                  background: "#ef4444",
                  overflow: "hidden",
                }}
              >
                <div
                  style={{
                    height: "100%",
                    width: `${analytics.stats.pass_rate * 100}%`,
                    background: "#22c55e",
                  }}
                />
              </div>
              <div
                style={{
                  display: "flex",
                  justifyContent: "space-between",
                  fontSize: "0.75rem",
                  marginTop: "0.35rem",
                }}
              >
                <span style={{ color: "#166534", fontWeight: 600 }}>
                  {analytics.stats.pass_messages.toLocaleString()} pass
                </span>
                <span style={{ color: "#991b1b", fontWeight: 600 }}>
                  {analytics.stats.fail_messages.toLocaleString()} fail
                </span>
              </div>
            </div>

            <h2>Top Failing Domains</h2>
            {analytics.top_failing_domains.length === 0 ? (
              <p className="no-results">No failing domains.</p>
            ) : (
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>Domain</th>
                      <th style={{ textAlign: "right" }}>Fails</th>
                      <th style={{ textAlign: "right" }}>Total</th>
                      <th style={{ textAlign: "right" }}>Fail Rate</th>
                      <th style={{ width: "30%" }}>Distribution</th>
                    </tr>
                  </thead>
                  <tbody>
                    {analytics.top_failing_domains.map((d) => (
                      <tr key={d.domain}>
                        <td>{d.domain}</td>
                        <td style={{ textAlign: "right", color: "#991b1b", fontFamily: "var(--font-mono, monospace)" }}>
                          {d.fails.toLocaleString()}
                        </td>
                        <td style={{ textAlign: "right", fontFamily: "var(--font-mono, monospace)" }}>
                          {d.total.toLocaleString()}
                        </td>
                        <td
                          style={{
                            textAlign: "right",
                            fontFamily: "var(--font-mono, monospace)",
                            fontWeight: 600,
                            color:
                              d.fail_rate >= 0.5
                                ? "#991b1b"
                                : d.fail_rate >= 0.1
                                  ? "#854d0e"
                                  : "#64748b",
                          }}
                        >
                          {pct(d.fail_rate)}
                        </td>
                        <td>
                          <div
                            style={{
                              height: "0.5rem",
                              borderRadius: "9999px",
                              background: "#22c55e",
                              overflow: "hidden",
                            }}
                          >
                            <div
                              style={{
                                height: "100%",
                                width: `${d.fail_rate * 100}%`,
                                background: "#ef4444",
                              }}
                            />
                          </div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </>
        )
      )}

      {tab === "trend" && <TrendChart since={since} until={until} domain={domainFilter} />}

      {tab === "sources" && <TopSources since={since} until={until} domain={domainFilter} />}

      {tab === "threats" && <Threats since={since} until={until} domain={domainFilter} />}

      {tab === "policy" && <PolicyAdvisor since={since} until={until} />}
    </>
  );
}

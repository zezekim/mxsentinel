"use client";

import { useState } from "react";
import { getDomainReport, type DomainReport } from "@/lib/api";

function pct(n: number, total: number): string {
  return total > 0 ? `${((n / total) * 100).toFixed(1)}%` : "—";
}
function providerLabel(p: string): string {
  const m: Record<string, string> = { microsoft: "Microsoft", google: "Google", yahoo: "Yahoo", apple: "Apple" };
  return m[p.toLowerCase()] || (p ? p[0].toUpperCase() + p.slice(1) : "Other");
}

export default function DomainReportPage() {
  const today = new Date().toISOString().slice(0, 10);
  const monthAgo = new Date(Date.now() - 30 * 864e5).toISOString().slice(0, 10);

  const [domain, setDomain] = useState("");
  const [since, setSince] = useState(monthAgo);
  const [until, setUntil] = useState(today);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [report, setReport] = useState<DomainReport | null>(null);
  const [text, setText] = useState("");
  const [copied, setCopied] = useState(false);

  async function generate(e: React.FormEvent) {
    e.preventDefault();
    if (!domain.trim()) return;
    setLoading(true);
    setError(null);
    setCopied(false);
    try {
      const d = await getDomainReport(domain.trim(), since, until);
      setReport(d.report);
      setText(d.text);
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to build report");
      setReport(null);
    } finally {
      setLoading(false);
    }
  }

  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      /* clipboard blocked; the textarea below is selectable as a fallback */
    }
  }

  const c = report?.core;

  return (
    <>
      <h1>Domain Report</h1>
      <p className="section-desc">
        An on-demand deliverability summary for one sending domain — copy the block straight into a
        client report. (For scheduled email reports, see Reports.)
      </p>

      <form onSubmit={generate} className="settings-form" style={{ marginBottom: "1.5rem" }}>
        <fieldset className="settings-group">
          <div className="field">
            <label htmlFor="rd_domain">Sending domain</label>
            <input id="rd_domain" placeholder="pchslive.com" value={domain} onChange={(e) => setDomain(e.target.value)} />
          </div>
          <div className="advanced-grid">
            <div className="field">
              <label htmlFor="rd_since">From</label>
              <input id="rd_since" type="date" value={since} onChange={(e) => setSince(e.target.value)} />
            </div>
            <div className="field">
              <label htmlFor="rd_until">To</label>
              <input id="rd_until" type="date" value={until} onChange={(e) => setUntil(e.target.value)} />
            </div>
          </div>
          <div className="settings-actions">
            <button type="submit" className="btn btn-primary" disabled={loading}>
              {loading ? "Building…" : "Generate report"}
            </button>
            {error && <span className="state-msg error" style={{ padding: 0 }}>{error}</span>}
          </div>
        </fieldset>
      </form>

      {report && c && (
        <div className="settings-group" style={{ maxWidth: 760 }}>
          <div style={{ display: "flex", justifyContent: "space-between", alignItems: "baseline", gap: "1rem", flexWrap: "wrap" }}>
            <h2 style={{ margin: 0 }}>{report.domain}</h2>
            {report.score && (
              <span className="field-hint">
                Health score <strong>{Math.round(report.score.score)}</strong> ({report.score.grade}) · coverage {(report.score.coverage * 100).toFixed(0)}%
              </span>
            )}
          </div>
          <p className="field-hint">{report.period_start.slice(0, 10)} → {report.period_end.slice(0, 10)}</p>

          {c.total === 0 ? (
            <p className="section-desc">No mail sent from this domain in the period.</p>
          ) : (
            <>
              <div style={{ display: "flex", gap: "1.5rem", flexWrap: "wrap", margin: "0.75rem 0" }}>
                <Stat label="Sent" value={c.total.toLocaleString()} />
                <Stat label="Delivered" value={`${c.delivered.toLocaleString()} (${pct(c.delivered, c.total)})`} />
                <Stat label="Bounced" value={`${c.bounced.toLocaleString()} (${pct(c.bounced, c.total)})`} />
                <Stat label="Deferred" value={`${c.deferred.toLocaleString()} (${pct(c.deferred, c.total)})`} />
                <Stat label="Rejected" value={`${c.rejected.toLocaleString()} (${pct(c.rejected, c.total)})`} />
              </div>

              {report.providers && report.providers.length > 0 && (
                <>
                  <h3 className="subhead">By provider</h3>
                  <div className="table-wrap"><table>
                    <thead><tr><th>Provider</th><th>Sent</th><th>Delivered</th><th>Bounced</th></tr></thead>
                    <tbody>
                      {report.providers.map((p) => (
                        <tr key={p.provider}>
                          <td>{providerLabel(p.provider)}</td>
                          <td>{p.total.toLocaleString()}</td>
                          <td>{p.delivered.toLocaleString()} ({pct(p.delivered, p.total)})</td>
                          <td>{p.bounced.toLocaleString()} ({pct(p.bounced, p.total)})</td>
                        </tr>
                      ))}
                    </tbody>
                  </table></div>
                </>
              )}

              {report.placement && report.placement.length > 0 && (
                <>
                  <h3 className="subhead">Inbox placement (seed tests, relay-wide)</h3>
                  <div className="table-wrap"><table>
                    <thead><tr><th>Provider</th><th>Inbox</th><th>Spam</th><th>Missing</th></tr></thead>
                    <tbody>
                      {report.placement.map((p) => (
                        <tr key={p.provider}>
                          <td>{providerLabel(p.provider)}</td>
                          <td>{pct(p.inbox, p.total)} ({p.inbox}/{p.total})</td>
                          <td>{p.spam}</td>
                          <td>{p.missing}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table></div>
                </>
              )}
            </>
          )}

          <div className="settings-actions" style={{ marginTop: "1.25rem" }}>
            <button type="button" className="btn btn-primary" onClick={copy}>
              {copied ? "Copied ✓" : "Copy report"}
            </button>
            <span className="field-hint">Pastes as plain text into any report.</span>
          </div>
          <textarea
            readOnly
            value={text}
            onFocus={(e) => e.currentTarget.select()}
            style={{ width: "100%", marginTop: "0.75rem", minHeight: 180, fontFamily: "var(--font-mono, monospace)", fontSize: "0.8rem" }}
          />
        </div>
      )}
    </>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <div className="field-hint">{label}</div>
      <div style={{ fontSize: "1.1rem", fontWeight: 600 }}>{value}</div>
    </div>
  );
}

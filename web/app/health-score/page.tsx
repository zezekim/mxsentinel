"use client";

import { Fragment, useCallback, useEffect, useState } from "react";
import LoadingError from "@/components/LoadingError";
import {
  getHealthScoreSummary,
  getDomainHealthScore,
  getDomainHealthScoreHistory,
  gradeBadgeClass,
  type HealthScoreSummary,
  type HealthScoreDomain,
  type HealthScorePoint,
} from "@/lib/api-healthscore";

function fmt(ts: string | null): string {
  return ts ? new Date(ts).toLocaleString() : "—";
}

function scoreText(d: { has_data: boolean; score: number }): string {
  return d.has_data ? d.score.toFixed(0) : "—";
}

// A slim inline trend of the last N scores (newest last), drawn as CSS bars — no chart library.
function Trend({ points }: { points: HealthScorePoint[] }) {
  const rated = points.filter((p) => p.has_data);
  if (rated.length === 0) return <span className="hs-muted">no history yet</span>;
  const ordered = [...rated].reverse(); // oldest -> newest
  return (
    <div className="hs-trend" aria-label="score trend">
      {ordered.map((p, i) => (
        <span
          key={i}
          className="hs-trend-bar"
          style={{ height: `${Math.max(6, p.score)}%` }}
          title={`${p.score.toFixed(0)} (${p.grade}) · ${fmt(p.computed_at)}`}
        />
      ))}
    </div>
  );
}

function DomainDetail({ id }: { id: string }) {
  const [detail, setDetail] = useState<HealthScoreDomain | null>(null);
  const [history, setHistory] = useState<HealthScorePoint[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    setLoading(true);
    setError(null);
    Promise.all([getDomainHealthScore(id), getDomainHealthScoreHistory(id, 60)])
      .then(([d, h]) => {
        setDetail(d);
        setHistory(h.history);
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, [id]);

  return (
    <div className="hs-detail">
      <LoadingError loading={loading} error={error} />
      {!loading && !error && detail && (
        <>
          <div className="hs-detail-head">
            <div>
              <span className={`badge ${gradeBadgeClass(detail.grade)}`}>{detail.grade}</span>{" "}
              <strong>{scoreText(detail)}</strong>
              <span className="hs-muted">
                {" "}
                / 100 · {(detail.coverage * 100).toFixed(0)}% signal coverage
                {detail.pending ? " · computed live (not yet snapshotted)" : ""}
              </span>
            </div>
            <Trend points={history} />
          </div>

          <table className="hs-components">
            <thead>
              <tr>
                <th>Component</th>
                <th>Score</th>
                <th>Weight</th>
                <th>Drag</th>
                <th>Detail</th>
              </tr>
            </thead>
            <tbody>
              {(detail.components ?? []).map((c) => (
                <tr key={c.name} className={c.present ? "" : "hs-absent"}>
                  <td>{c.label}</td>
                  <td>{c.present ? c.score.toFixed(0) : "—"}</td>
                  <td>{c.present ? `${(c.weight * 100).toFixed(0)}%` : "—"}</td>
                  <td>{c.present && c.impact > 0 ? `−${c.impact.toFixed(1)}` : "—"}</td>
                  <td className="hs-muted">{c.detail}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}
    </div>
  );
}

export default function HealthScorePage() {
  const [summary, setSummary] = useState<HealthScoreSummary | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [expanded, setExpanded] = useState<string | null>(null);

  const fetchData = useCallback(() => {
    setLoading(true);
    setError(null);
    getHealthScoreSummary()
      .then(setSummary)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    fetchData();
  }, [fetchData]);

  return (
    <>
      <h1>Deliverability Health Score</h1>

      <p className="hs-intro">
        A composite 0–100 score per domain that fuses signals already collected across the
        platform — DMARC / auth alignment, feedback-loop complaint volume, blocklist &amp;
        reputation posture, bounce ratio, send-volume anomalies, and Gmail Postmaster reputation.
        Missing signals are neutral (excluded), never counted as failures. Click a domain for the
        component breakdown and trend.
      </p>

      <LoadingError loading={loading} error={error} />

      {!loading && !error && summary && (
        <>
          <div className="hs-tenant">
            <div className="hs-tenant-score">
              <span className={`badge ${gradeBadgeClass(summary.tenant.grade)}`}>
                {summary.tenant.grade}
              </span>
              <span className="hs-tenant-num">
                {summary.tenant.domains_rated > 0 ? summary.tenant.score.toFixed(0) : "—"}
              </span>
              <span className="hs-muted">/ 100</span>
            </div>
            <div className="hs-muted">
              Tenant composite over {summary.tenant.domains_rated} of{" "}
              {summary.tenant.domains_total} domain(s)
            </div>
          </div>

          {summary.domains.length === 0 ? (
            <p className="no-results">
              No domains yet. Add a domain to start scoring its deliverability health.
            </p>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Domain</th>
                    <th>Grade</th>
                    <th>Score</th>
                    <th>Coverage</th>
                    <th>Updated</th>
                    <th></th>
                  </tr>
                </thead>
                <tbody>
                  {summary.domains.map((d) => (
                    <Fragment key={d.domain_id}>
                      <tr>
                        <td>{d.domain}</td>
                        <td>
                          <span className={`badge ${gradeBadgeClass(d.grade)}`}>{d.grade}</span>
                        </td>
                        <td>{scoreText(d)}</td>
                        <td>{d.has_data ? `${(d.coverage * 100).toFixed(0)}%` : "—"}</td>
                        <td style={{ whiteSpace: "nowrap" }}>
                          {d.pending ? "pending" : fmt(d.computed_at)}
                        </td>
                        <td>
                          <button
                            className="hs-link"
                            onClick={() =>
                              setExpanded(expanded === d.domain_id ? null : d.domain_id)
                            }
                          >
                            {expanded === d.domain_id ? "Hide" : "Breakdown"}
                          </button>
                        </td>
                      </tr>
                      {expanded === d.domain_id && (
                        <tr>
                          <td colSpan={6} style={{ background: "transparent", padding: 0 }}>
                            <DomainDetail id={d.domain_id} />
                          </td>
                        </tr>
                      )}
                    </Fragment>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}

      <style jsx>{`
        .hs-intro {
          margin-bottom: 1rem;
          color: #555;
          font-size: 0.875rem;
          max-width: 60rem;
        }
        .hs-muted {
          color: #888;
          font-size: 0.85em;
        }
        .hs-tenant {
          display: flex;
          align-items: baseline;
          gap: 1rem;
          padding: 1rem 1.25rem;
          border: 1px solid #e3e3e3;
          border-radius: 8px;
          margin-bottom: 1.25rem;
          background: #fafafa;
        }
        .hs-tenant-score {
          display: flex;
          align-items: baseline;
          gap: 0.5rem;
        }
        .hs-tenant-num {
          font-size: 2.25rem;
          font-weight: 700;
          line-height: 1;
        }
        .hs-link {
          background: none;
          border: none;
          color: #2563eb;
          cursor: pointer;
          font-size: 0.85rem;
          padding: 0;
        }
        .hs-link:hover {
          text-decoration: underline;
        }
        .hs-detail {
          padding: 0.75rem 1rem 1rem;
          border-left: 3px solid #d0d0d0;
          margin: 0.25rem 0 0.75rem;
          background: #fafafa;
        }
        .hs-detail-head {
          display: flex;
          justify-content: space-between;
          align-items: flex-end;
          gap: 1rem;
          margin-bottom: 0.75rem;
        }
        .hs-components {
          width: 100%;
          border-collapse: collapse;
          font-size: 0.85rem;
        }
        .hs-components th,
        .hs-components td {
          text-align: left;
          padding: 0.35rem 0.5rem;
          border-bottom: 1px solid #ececec;
        }
        .hs-absent {
          opacity: 0.5;
        }
        .hs-trend {
          display: inline-flex;
          align-items: flex-end;
          gap: 2px;
          height: 40px;
          min-width: 80px;
        }
        .hs-trend-bar {
          width: 6px;
          background: #60a5fa;
          border-radius: 1px 1px 0 0;
          display: inline-block;
        }
        @media (prefers-color-scheme: dark) {
          .hs-intro,
          .hs-muted {
            color: #9ca3af;
          }
          .hs-tenant,
          .hs-detail {
            background: #1c1c1e;
            border-color: #333;
          }
          .hs-components th,
          .hs-components td {
            border-bottom-color: #2a2a2a;
          }
        }
      `}</style>
    </>
  );
}

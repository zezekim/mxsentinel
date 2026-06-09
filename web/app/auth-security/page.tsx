"use client";

import { useCallback, useEffect, useState } from "react";
import {
  apiGet,
  getAuthSecurity,
  lockCredential,
  type AuthCredential,
  type AuthSignal,
} from "@/lib/api";
import LoadingError from "@/components/LoadingError";

// ─── Types ────────────────────────────────────────────────────────────────────

type StatsWindow = "24h" | "7d" | "30d";

interface CredentialStats {
  sent: number;
  delivered: number;
  bounced: number;
  rejected: number;
  unique_recipients: number;
  bounce_rate: number;
}

interface CredentialStatsResponse {
  sasl_username: string;
  stats: CredentialStats;
  signals: AuthSignal[];
  locked: boolean;
  locked_at: string | null;
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

function fmt(ts: string): string {
  if (!ts) return "—";
  const d = new Date(ts);
  return isNaN(d.getTime()) ? ts : d.toLocaleString();
}

// Map a signal label to a badge severity. The composite trip and bounce spike are the
// strongest tells, so they render critical; the rest render as warnings.
function signalBadgeClass(signal: string): string {
  switch (signal) {
    case "composite_trip":
    case "bounce_spike":
      return "badge-critical";
    default:
      return "badge-warning";
  }
}

function bounceRateColor(rate: number): string {
  if (rate < 5) return "#22c55e";   // green
  if (rate <= 15) return "#f59e0b"; // yellow
  return "#ef4444";                 // red
}

// ─── Sub-components ──────────────────────────────────────────────────────────

function SignalBadges({ signals }: { signals: AuthSignal[] }) {
  if (signals.length === 0) return <span className="badge badge-unknown">no signals</span>;
  return (
    <div className="row-actions">
      {signals.map((s, i) => (
        <span
          key={i}
          className={`badge ${signalBadgeClass(s.signal)}`}
          title={`${fmt(s.detected_at)} — ${JSON.stringify(s.detail)}`}
        >
          {s.signal}
        </span>
      ))}
    </div>
  );
}

function StatCell({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div style={{ textAlign: "center", minWidth: "90px" }}>
      <div style={{ fontSize: "1.1rem", fontWeight: 600, color: "#f1f5f9" }}>{value}</div>
      <div style={{ fontSize: "0.7rem", color: "#94a3b8", marginTop: "0.15rem", textTransform: "uppercase", letterSpacing: "0.04em" }}>{label}</div>
    </div>
  );
}

interface ExpandedStatsProps {
  username: string;
  window: StatsWindow;
}

function ExpandedStats({ username, window }: ExpandedStatsProps) {
  const [data, setData] = useState<CredentialStatsResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [fetched, setFetched] = useState(false);

  const fetchStats = useCallback(() => {
    setLoading(true);
    setError(null);
    apiGet<CredentialStatsResponse>(
      `/v1/auth-security/${encodeURIComponent(username)}/stats?window=${window}`
    )
      .then((d) => {
        setData(d);
        setFetched(true);
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, [username, window]);

  // Re-fetch automatically when the window changes (if already loaded)
  useEffect(() => {
    if (fetched) {
      fetchStats();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [window]);

  return (
    <tr>
      <td
        colSpan={5}
        style={{
          padding: "0.75rem 1rem 1rem 2.5rem",
          background: "#0f172a",
          borderTop: "1px solid #1e293b",
        }}
      >
        {!fetched && !loading && (
          <button
            type="button"
            className="btn-sm"
            onClick={fetchStats}
            style={{ marginBottom: "0.5rem" }}
          >
            View Stats
          </button>
        )}

        {loading && (
          <p style={{ color: "#94a3b8", fontSize: "0.85rem", margin: 0 }}>Loading stats…</p>
        )}

        {error && (
          <p style={{ color: "#ef4444", fontSize: "0.85rem", margin: 0 }}>Error: {error}</p>
        )}

        {data && !loading && (
          <div>
            {/* Stats row */}
            <div
              style={{
                display: "flex",
                gap: "1.5rem",
                flexWrap: "wrap",
                padding: "0.75rem 1rem",
                background: "#1e293b",
                borderRadius: "6px",
                marginBottom: "0.75rem",
                alignItems: "center",
              }}
            >
              <StatCell label="Sent" value={data.stats.sent.toLocaleString()} />
              <div style={{ width: "1px", background: "#334155", alignSelf: "stretch" }} />
              <StatCell label="Delivered" value={data.stats.delivered.toLocaleString()} />
              <div style={{ width: "1px", background: "#334155", alignSelf: "stretch" }} />
              <StatCell label="Bounced" value={data.stats.bounced.toLocaleString()} />
              <div style={{ width: "1px", background: "#334155", alignSelf: "stretch" }} />
              <StatCell label="Rejected" value={data.stats.rejected.toLocaleString()} />
              <div style={{ width: "1px", background: "#334155", alignSelf: "stretch" }} />
              <StatCell label="Uniq Recipients" value={data.stats.unique_recipients.toLocaleString()} />
              <div style={{ width: "1px", background: "#334155", alignSelf: "stretch" }} />
              <StatCell
                label="Bounce Rate"
                value={
                  <span style={{ color: bounceRateColor(data.stats.bounce_rate) }}>
                    {data.stats.bounce_rate.toFixed(1)}%
                  </span>
                }
              />
            </div>

            {/* Signals mini-table */}
            {data.signals && data.signals.length > 0 ? (
              <div>
                <div style={{ fontSize: "0.75rem", color: "#94a3b8", marginBottom: "0.35rem", textTransform: "uppercase", letterSpacing: "0.05em" }}>
                  Signals ({window})
                </div>
                <table style={{ width: "100%", fontSize: "0.8rem", borderCollapse: "collapse" }}>
                  <thead>
                    <tr style={{ color: "#64748b" }}>
                      <th style={{ textAlign: "left", padding: "0.25rem 0.5rem", fontWeight: 500 }}>Signal</th>
                      <th style={{ textAlign: "left", padding: "0.25rem 0.5rem", fontWeight: 500 }}>Detected At</th>
                      <th style={{ textAlign: "left", padding: "0.25rem 0.5rem", fontWeight: 500 }}>Detail</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.signals.map((s, i) => (
                      <tr key={i} style={{ borderTop: "1px solid #1e293b" }}>
                        <td style={{ padding: "0.25rem 0.5rem" }}>
                          <span className={`badge ${signalBadgeClass(s.signal)}`} style={{ fontSize: "0.7rem" }}>
                            {s.signal}
                          </span>
                        </td>
                        <td style={{ padding: "0.25rem 0.5rem", color: "#94a3b8", whiteSpace: "nowrap" }}>
                          {fmt(s.detected_at)}
                        </td>
                        <td style={{ padding: "0.25rem 0.5rem", color: "#cbd5e1", fontFamily: "monospace", fontSize: "0.75rem" }}>
                          {JSON.stringify(s.detail)}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : (
              <p style={{ fontSize: "0.8rem", color: "#64748b", margin: 0 }}>No signals in this window.</p>
            )}
          </div>
        )}
      </td>
    </tr>
  );
}

// ─── Page ─────────────────────────────────────────────────────────────────────

const WINDOWS: StatsWindow[] = ["24h", "7d", "30d"];

export default function AuthSecurityPage() {
  const [creds, setCreds] = useState<AuthCredential[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [expandedRows, setExpandedRows] = useState<Set<string>>(new Set());
  const [statsWindow, setStatsWindow] = useState<StatsWindow>("7d");

  const refresh = useCallback(() => {
    setLoading(true);
    setError(null);
    getAuthSecurity()
      .then((d) => setCreds(d.credentials))
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh]);

  function toggleRow(username: string) {
    setExpandedRows((prev) => {
      const next = new Set(prev);
      if (next.has(username)) {
        next.delete(username);
      } else {
        next.add(username);
      }
      return next;
    });
  }

  async function handleLock(c: AuthCredential) {
    if (
      !window.confirm(
        `Lock credential "${c.sasl_username}"? On a shared relay this disables submission for EVERY client that authenticates with this login.`,
      )
    ) {
      return;
    }
    const reason = window.prompt("Reason for locking (optional):") ?? "";
    try {
      await lockCredential(c.sasl_username, true, reason);
      setNotice(`Locked "${c.sasl_username}". The relay will reject its logins.`);
      refresh();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  async function handleUnlock(c: AuthCredential) {
    try {
      const res = await lockCredential(c.sasl_username, false, "");
      if (res.reenable_via_smtp_users) {
        setNotice(
          `Lock record cleared for "${c.sasl_username}". To re-enable submission, enable the credential on the SMTP Users page.`,
        );
      } else {
        setNotice(`Lock record cleared for "${c.sasl_username}".`);
      }
      refresh();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  return (
    <>
      <h1>Auth Security</h1>
      <p className="section-desc">
        Behavioral credential-compromise detection. authwatchd watches per-SASL-credential
        sending behavior — recipient-domain bursts (list-blasting), spam/block bounce-rate
        spikes, and volume far above the credential&apos;s recent norm — and flags
        compromise-shaped changes. On a shared relay one credential serves every client, so
        per-credential signals are coarse and locking is drastic (auto-lock is off by default).
      </p>

      {/* Window selector */}
      <div style={{ display: "flex", alignItems: "center", gap: "0.5rem", marginBottom: "1rem" }}>
        <span style={{ fontSize: "0.8rem", color: "#94a3b8", textTransform: "uppercase", letterSpacing: "0.05em" }}>
          Stats window:
        </span>
        {WINDOWS.map((w) => (
          <button
            key={w}
            type="button"
            onClick={() => setStatsWindow(w)}
            style={{
              padding: "0.25rem 0.65rem",
              fontSize: "0.8rem",
              borderRadius: "4px",
              border: "1px solid",
              cursor: "pointer",
              borderColor: statsWindow === w ? "#6366f1" : "#334155",
              background: statsWindow === w ? "#312e81" : "transparent",
              color: statsWindow === w ? "#e0e7ff" : "#94a3b8",
              fontWeight: statsWindow === w ? 600 : 400,
              transition: "background 0.15s, color 0.15s",
            }}
          >
            {w}
          </button>
        ))}
      </div>

      {notice && <p className="notice-banner">{notice}</p>}

      <LoadingError loading={loading} error={error} />

      {!loading && !error && (
        <>
          {creds.length === 0 ? (
            <p className="no-results">
              No flagged credentials. authwatchd records a signal here only when a credential
              trips the behavioral threshold.
            </p>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th style={{ width: "1.5rem" }} />
                    <th>SASL Credential</th>
                    <th>Recent Signals</th>
                    <th>Status</th>
                    <th>Locked At</th>
                    <th style={{ textAlign: "right" }}>Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {creds.map((c) => {
                    const isExpanded = expandedRows.has(c.sasl_username);
                    return (
                      <>
                        <tr
                          key={c.sasl_username}
                          style={{ cursor: "pointer" }}
                          onClick={() => toggleRow(c.sasl_username)}
                        >
                          {/* Expand chevron */}
                          <td style={{ textAlign: "center", color: "#64748b", fontSize: "0.75rem", userSelect: "none" }}>
                            {isExpanded ? "▾" : "▸"}
                          </td>
                          <td>{c.sasl_username}</td>
                          <td>
                            <SignalBadges signals={c.recent_signals} />
                          </td>
                          <td>
                            <span className={`badge ${c.locked ? "badge-critical" : "badge-ok"}`}>
                              {c.locked ? "locked" : "active"}
                            </span>
                            {c.locked && c.reason && (
                              <div style={{ fontSize: "0.75rem", color: "#64748b", marginTop: "0.25rem" }}>
                                {c.reason}
                              </div>
                            )}
                          </td>
                          <td style={{ whiteSpace: "nowrap" }}>{c.locked_at ? fmt(c.locked_at) : "—"}</td>
                          <td>
                            <div
                              className="row-actions"
                              onClick={(e) => e.stopPropagation()}
                            >
                              {c.locked ? (
                                <button type="button" className="btn-sm" onClick={() => handleUnlock(c)}>
                                  Unlock
                                </button>
                              ) : (
                                <button
                                  type="button"
                                  className="btn-sm btn-sm-danger"
                                  onClick={() => handleLock(c)}
                                >
                                  Lock
                                </button>
                              )}
                            </div>
                          </td>
                        </tr>
                        {isExpanded && (
                          <ExpandedStats
                            key={`${c.sasl_username}-stats`}
                            username={c.sasl_username}
                            window={statsWindow}
                          />
                        )}
                      </>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
    </>
  );
}

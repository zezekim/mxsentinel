"use client";

import { useCallback, useEffect, useState } from "react";
import {
  getAuthSecurity,
  lockCredential,
  type AuthCredential,
  type AuthSignal,
} from "@/lib/api";
import LoadingError from "@/components/LoadingError";

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

export default function AuthSecurityPage() {
  const [creds, setCreds] = useState<AuthCredential[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

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
                    <th>SASL Credential</th>
                    <th>Recent Signals</th>
                    <th>Status</th>
                    <th>Locked At</th>
                    <th style={{ textAlign: "right" }}>Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {creds.map((c) => (
                    <tr key={c.sasl_username}>
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
                        <div className="row-actions">
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

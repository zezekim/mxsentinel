"use client";

import { useCallback, useEffect, useState } from "react";
import LoadingError from "@/components/LoadingError";
import {
  getBounces,
  getSuppression,
  addSuppression,
  deleteSuppression,
  getSuppressionExport,
  type BouncesResponse,
  type BounceWindow,
  type BounceCategory,
  type SuppressionEntry,
} from "@/lib/api-suppression";

const WINDOWS: BounceWindow[] = ["1h", "24h", "7d", "30d"];

function fmt(ts: string | null): string {
  return ts ? new Date(ts).toLocaleString() : "—";
}

function fmtRate(rate: number): string {
  return `${(rate * 100).toFixed(2)}%`;
}

// Maps a bounce category to one of the shared badge classes.
function categoryBadge(cat: BounceCategory | string): string {
  switch (cat) {
    case "invalid_recipient":
    case "hard":
      return "badge-critical";
    case "spam_block":
    case "reputation":
    case "block":
      return "badge-warning";
    case "soft":
    case "mailbox_full":
      return "badge-ok";
    default:
      return "badge-unknown";
  }
}

function shortHash(h: string): string {
  return h.length > 16 ? `${h.slice(0, 16)}…` : h;
}

export default function SuppressionPage() {
  const [window, setWindow] = useState<BounceWindow>("24h");
  const [bounces, setBounces] = useState<BouncesResponse | null>(null);
  const [entries, setEntries] = useState<SuppressionEntry[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  // Add-suppression form
  const [email, setEmail] = useState("");
  const [hash, setHash] = useState("");
  const [submitting, setSubmitting] = useState(false);

  const fetchData = useCallback(() => {
    setLoading(true);
    setError(null);
    Promise.all([getBounces(window), getSuppression(false)])
      .then(([b, s]) => {
        setBounces(b);
        setEntries(s.entries);
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, [window]);

  useEffect(() => {
    fetchData();
  }, [fetchData]);

  async function handleAdd(e: React.FormEvent) {
    e.preventDefault();
    if (!email.trim() && !hash.trim()) return;
    setSubmitting(true);
    setNotice(null);
    try {
      await addSuppression({
        email: email.trim() || undefined,
        recipient_hash: hash.trim() || undefined,
        source: "manual",
        reason: "manual",
      });
      setEmail("");
      setHash("");
      setNotice("Suppression added.");
      fetchData();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSubmitting(false);
    }
  }

  async function handleDelete(h: string) {
    if (!confirm("Remove this recipient from the suppression list?")) return;
    try {
      await deleteSuppression(h);
      setEntries((prev) => prev.filter((x) => x.recipient_hash !== h));
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  async function handleExport(format: "plain" | "postfix") {
    try {
      const text = await getSuppressionExport(format);
      const blob = new Blob([text], { type: "text/plain" });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `suppression-${format}.txt`;
      a.click();
      URL.revokeObjectURL(url);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  return (
    <>
      <h1>Bounces &amp; Suppression</h1>

      <p style={{ marginBottom: "0.75rem", color: "#555", fontSize: "0.875rem" }}>
        Classified SMTP bounces (RFC 3463 enhanced codes + response-text patterns) and the
        per-tenant suppression list. Hard bounces, invalid recipients, and spam blocks are
        auto-suppressed by recipient hash. Recipient addresses are never stored — only their
        keyed hash. Export the list to sync it back to the relay.
      </p>

      <div style={{ display: "flex", gap: "0.5rem", marginBottom: "1rem", alignItems: "center" }}>
        <span style={{ fontSize: "0.8rem", color: "#666" }}>Window:</span>
        {WINDOWS.map((w) => (
          <button
            key={w}
            type="button"
            className={"badge " + (w === window ? "badge-ok" : "badge-unknown")}
            style={{ cursor: "pointer", border: "none" }}
            onClick={() => setWindow(w)}
          >
            {w}
          </button>
        ))}
        <div style={{ flex: 1 }} />
        <button type="button" className="badge badge-unknown" style={{ cursor: "pointer", border: "none" }} onClick={() => handleExport("plain")}>
          Export hashes
        </button>
        <button type="button" className="badge badge-unknown" style={{ cursor: "pointer", border: "none" }} onClick={() => handleExport("postfix")}>
          Export Postfix map
        </button>
      </div>

      <LoadingError loading={loading} error={error} />
      {notice && <p style={{ color: "#2a7", fontSize: "0.85rem" }}>{notice}</p>}

      {!loading && !error && bounces && (
        <>
          {/* Category summary */}
          <h2 style={{ fontSize: "1rem", marginTop: "1rem" }}>Bounce categories ({bounces.window})</h2>
          {bounces.categories.length === 0 ? (
            <p className="no-results">No classified bounces recorded yet for this window.</p>
          ) : (
            <div style={{ display: "flex", flexWrap: "wrap", gap: "0.5rem", marginBottom: "1rem" }}>
              {bounces.categories.map((c) => (
                <span key={c.category} className={`badge ${categoryBadge(c.category)}`}>
                  {c.category}: {c.count.toLocaleString()}
                </span>
              ))}
            </div>
          )}

          {/* Per-domain rates */}
          <h2 style={{ fontSize: "1rem" }}>Per-domain bounce rate</h2>
          {bounces.domain_rates.length === 0 ? (
            <p className="no-results">No per-domain volume in this window.</p>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Sending Domain</th>
                    <th>Attempts</th>
                    <th>Bounced</th>
                    <th>Rate</th>
                  </tr>
                </thead>
                <tbody>
                  {bounces.domain_rates.map((d) => (
                    <tr key={d.domain}>
                      <td>{d.domain || "(none)"}</td>
                      <td>{d.total.toLocaleString()}</td>
                      <td>{d.bounced.toLocaleString()}</td>
                      <td>
                        <span className={`badge ${d.rate >= 0.1 ? "badge-critical" : d.rate >= 0.05 ? "badge-warning" : "badge-ok"}`}>
                          {fmtRate(d.rate)}
                        </span>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}

          {/* Recent classified feed */}
          <h2 style={{ fontSize: "1rem" }}>Recent classified bounces</h2>
          {bounces.recent.length === 0 ? (
            <p className="no-results">No recent bounces in this window.</p>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Time</th>
                    <th>From Domain</th>
                    <th>Provider</th>
                    <th>Code</th>
                    <th>DSN</th>
                    <th>Category</th>
                    <th>Response</th>
                  </tr>
                </thead>
                <tbody>
                  {bounces.recent.map((b, i) => (
                    <tr key={`${b.event_time}-${i}`}>
                      <td style={{ whiteSpace: "nowrap" }}>{fmt(b.event_time)}</td>
                      <td>{b.from_domain || "—"}</td>
                      <td>{b.provider || "—"}</td>
                      <td>{b.smtp_code || "—"}</td>
                      <td>{b.enhanced_status || "—"}</td>
                      <td>
                        <span className={`badge ${categoryBadge(b.category)}`}>{b.category}</span>
                        {b.suppressed && <span className="badge badge-critical" style={{ marginLeft: 4 }}>suppressed</span>}
                      </td>
                      <td style={{ maxWidth: 320, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }} title={b.response_text}>
                        {b.response_text || "—"}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}

          {/* Suppression list management */}
          <h2 style={{ fontSize: "1rem", marginTop: "1.5rem" }}>Suppression list ({entries.length})</h2>

          <form onSubmit={handleAdd} style={{ display: "flex", gap: "0.5rem", flexWrap: "wrap", marginBottom: "1rem", alignItems: "center" }}>
            <input
              type="email"
              placeholder="recipient@example.com"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              style={{ padding: "0.4rem", minWidth: 220 }}
            />
            <span style={{ color: "#999", fontSize: "0.8rem" }}>or</span>
            <input
              type="text"
              placeholder="recipient hash"
              value={hash}
              onChange={(e) => setHash(e.target.value)}
              style={{ padding: "0.4rem", minWidth: 220 }}
            />
            <button type="submit" className="badge badge-ok" style={{ cursor: "pointer", border: "none" }} disabled={submitting}>
              {submitting ? "Adding…" : "Suppress"}
            </button>
          </form>

          {entries.length === 0 ? (
            <p className="no-results">No suppressed recipients yet.</p>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Recipient Hash</th>
                    <th>Reason</th>
                    <th>Category</th>
                    <th>Source</th>
                    <th>Added</th>
                    <th>Expires</th>
                    <th></th>
                  </tr>
                </thead>
                <tbody>
                  {entries.map((e) => (
                    <tr key={e.recipient_hash}>
                      <td style={{ fontFamily: "monospace" }} title={e.recipient_hash}>{shortHash(e.recipient_hash)}</td>
                      <td>{e.reason || "—"}</td>
                      <td>{e.category ? <span className={`badge ${categoryBadge(e.category)}`}>{e.category}</span> : "—"}</td>
                      <td>{e.source}</td>
                      <td style={{ whiteSpace: "nowrap" }}>{fmt(e.created_at)}</td>
                      <td style={{ whiteSpace: "nowrap" }}>{e.expires_at ? fmt(e.expires_at) : "permanent"}</td>
                      <td>
                        <button
                          type="button"
                          className="badge badge-unknown"
                          style={{ cursor: "pointer", border: "none" }}
                          onClick={() => handleDelete(e.recipient_hash)}
                        >
                          Remove
                        </button>
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

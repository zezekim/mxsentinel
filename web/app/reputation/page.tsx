"use client";

import { useCallback, useEffect, useState } from "react";
import { getReputation, type ReputationDomain } from "@/lib/api";
import LoadingError from "@/components/LoadingError";

function fmt(ts: string | null): string {
  return ts ? new Date(ts).toLocaleString() : "—";
}

// Maps a Gmail Postmaster grade to one of the shared badge classes.
function reputationBadge(rep: string): string {
  switch (rep.toUpperCase()) {
    case "HIGH":
    case "GOOD":
      return "badge-ok";
    case "MEDIUM":
      return "badge-warning";
    case "LOW":
    case "BAD":
      return "badge-critical";
    default:
      return "badge-unknown";
  }
}

function fmtSpamRate(rate: number | null): string {
  if (rate === null || rate === undefined) return "—";
  return `${(rate * 100).toFixed(2)}%`;
}

export default function ReputationPage() {
  const [domains, setDomains] = useState<ReputationDomain[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const fetchData = useCallback(() => {
    setLoading(true);
    setError(null);
    getReputation()
      .then((d) => setDomains(d.domains))
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    fetchData();
  }, [fetchData]);

  return (
    <>
      <h1>Reputation</h1>

      <p style={{ marginBottom: "0.75rem", color: "#555", fontSize: "0.875rem" }}>
        Per-sending-domain complaint reputation from feedback-loop (ARF) complaints and Gmail
        Postmaster Tools. Attribution is by sending domain, not the shared SMTP login.
      </p>

      <LoadingError loading={loading} error={error} />

      {!loading && !error && (
        <>
          {domains.length === 0 ? (
            <p className="no-results">
              No complaint or reputation data yet. Enroll your sending IPs/domains with provider
              feedback loops and (optionally) configure Gmail Postmaster Tools.
            </p>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Sending Domain</th>
                    <th>Complaints (24h)</th>
                    <th>Complaints (total)</th>
                    <th>Postmaster Reputation</th>
                    <th>Spam Rate</th>
                    <th>Updated</th>
                  </tr>
                </thead>
                <tbody>
                  {domains.map((d) => (
                    <tr key={d.domain}>
                      <td>{d.domain}</td>
                      <td>{d.complaints_24h.toLocaleString()}</td>
                      <td>{d.complaints_total.toLocaleString()}</td>
                      <td>
                        {d.postmaster_reputation ? (
                          <span className={`badge ${reputationBadge(d.postmaster_reputation)}`}>
                            {d.postmaster_reputation}
                          </span>
                        ) : (
                          <span className="badge badge-unknown">unknown</span>
                        )}
                      </td>
                      <td>{fmtSpamRate(d.spam_rate)}</td>
                      <td style={{ whiteSpace: "nowrap" }}>{fmt(d.fetched_at)}</td>
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

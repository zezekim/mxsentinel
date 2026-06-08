"use client";

import { useCallback, useEffect, useState } from "react";
import { getAnomalyRecent, type AnomalyRecentResponse, type VolumeAnomaly, type VolumeMover } from "@/lib/api";
import LoadingError from "@/components/LoadingError";

function fmtTime(ts: string): string {
  return new Date(ts).toLocaleString();
}

function fmtNum(n: number): string {
  return Math.round(n).toLocaleString();
}

function fmtFactor(f: number): string {
  if (!isFinite(f) || f <= 0) return "—";
  return `${f.toFixed(1)}×`;
}

// A spike "severity" badge: the configured trip factor is 5× by default, so treat a very
// large multiple as critical and a more modest one as a warning.
function factorBadge(f: number): string {
  if (f >= 10) return "badge badge-critical";
  if (f >= 5) return "badge badge-warning";
  return "badge badge-unknown";
}

function MoversColumn({ rows }: { rows: VolumeMover[] }) {
  // Bars are scaled to the largest "current" so a domain's spike reads against its peers;
  // each row also overlays its baseline so the gap (current vs baseline) is visible.
  const max = rows.reduce((m, r) => Math.max(m, r.current), 0);
  return (
    <div className="senders-col">
      <h2>Top Movers (current vs baseline)</h2>
      {rows.length === 0 ? (
        <p className="no-results">No movers in this window.</p>
      ) : (
        <ol className="senders-list">
          {rows.map((r, i) => (
            <li key={r.sender_domain} className="senders-row" title={r.sender_domain}>
              <span className="senders-rank">{i + 1}</span>
              <span className="senders-key">{r.sender_domain}</span>
              <span className="senders-count">
                {fmtNum(r.current)} / {fmtNum(r.baseline)}
              </span>
              <span
                className="senders-bar velocity-baseline-bar"
                style={{ width: max > 0 ? `${(r.baseline / max) * 100}%` : "0%" }}
              />
              <span
                className="senders-bar"
                style={{ width: max > 0 ? `${(r.current / max) * 100}%` : "0%" }}
              />
            </li>
          ))}
        </ol>
      )}
    </div>
  );
}

export default function VelocityPage() {
  const [data, setData] = useState<AnomalyRecentResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const fetchData = useCallback(() => {
    setLoading(true);
    setError(null);
    getAnomalyRecent()
      .then(setData)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    fetchData();
  }, [fetchData]);

  const anomalies: VolumeAnomaly[] = data?.anomalies ?? [];
  const movers: VolumeMover[] = data?.top_movers ?? [];

  return (
    <>
      <h1>Velocity</h1>
      <p className="velocity-intro">
        Send-volume spikes: sending domains whose current-hour outbound volume jumped well
        above their learned hourly baseline. Domains need a short warm-up before spikes can
        be detected.
      </p>

      <LoadingError loading={loading} error={error} />

      {!loading && !error && data && (
        <>
          <div className="senders-grid">
            <MoversColumn rows={movers} />
          </div>

          <h2 style={{ marginTop: "1.5rem" }}>Recent Anomalies</h2>
          {anomalies.length === 0 ? (
            <p className="no-results">No volume anomalies detected.</p>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Detected</th>
                    <th>Sender Domain</th>
                    <th>Observed (this hour)</th>
                    <th>Baseline (hourly)</th>
                    <th>Factor</th>
                  </tr>
                </thead>
                <tbody>
                  {anomalies.map((a, i) => (
                    <tr key={`${a.sender_domain}-${a.detected_at}-${i}`}>
                      <td style={{ whiteSpace: "nowrap" }}>{fmtTime(a.detected_at)}</td>
                      <td>{a.sender_domain}</td>
                      <td>{fmtNum(a.observed_hour_count)}</td>
                      <td>{fmtNum(a.baseline)}</td>
                      <td>
                        <span className={factorBadge(a.factor)}>{fmtFactor(a.factor)}</span>
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

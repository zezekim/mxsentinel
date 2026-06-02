"use client";

import { useEffect, useState, useCallback } from "react";
import Link from "next/link";
import { useParams } from "next/navigation";
import {
  apiGet,
  apiPost,
  type DomainHealthResponse,
  type RecheckResponse,
  type SnapshotsResponse,
  type SnapshotSummary,
  type StatusColor,
  type OverallStatus,
  type Severity,
} from "@/lib/api";
import Badge from "@/components/Badge";
import LoadingError from "@/components/LoadingError";

function fmt(ts: string | null | undefined): string {
  if (!ts) return "—";
  return new Date(ts).toLocaleString();
}

function shortHash(h: string): string {
  return h.length > 12 ? h.slice(0, 12) + "…" : h;
}

export default function DomainDetailPage() {
  const params = useParams();
  const id = params?.id as string;

  const [health, setHealth] = useState<DomainHealthResponse | null>(null);
  const [snapshots, setSnapshots] = useState<SnapshotSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [rechecking, setRechecking] = useState(false);
  const [recheckMsg, setRecheckMsg] = useState<string | null>(null);

  const loadHealth = useCallback(() => {
    return apiGet<DomainHealthResponse>(`/v1/domains/${id}/health`);
  }, [id]);

  const loadSnapshots = useCallback(() => {
    return apiGet<SnapshotsResponse>(`/v1/domains/${id}/dns/snapshots?limit=50`);
  }, [id]);

  useEffect(() => {
    if (!id) return;
    setLoading(true);
    Promise.all([loadHealth(), loadSnapshots()])
      .then(([h, s]) => {
        setHealth(h);
        setSnapshots(s.snapshots);
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, [id, loadHealth, loadSnapshots]);

  function handleRecheck() {
    setRechecking(true);
    setRecheckMsg(null);
    apiPost<RecheckResponse>(`/v1/domains/${id}/dns/recheck`)
      .then((r) => {
        setHealth(r);
        setRecheckMsg(r.changed ? "DNS changed since last check." : "No changes detected.");
        return loadSnapshots();
      })
      .then((s) => setSnapshots(s.snapshots))
      .catch((e: unknown) => setRecheckMsg(`Error: ${e instanceof Error ? e.message : String(e)}`))
      .finally(() => setRechecking(false));
  }

  return (
    <>
      <Link href="/" className="back-link">
        ← All Domains
      </Link>
      <LoadingError loading={loading} error={error} />

      {!loading && !error && health && (
        <>
          <div className="detail-header">
            <h1>{health.domain.name}</h1>
            <Badge kind="status" value={health.overall as OverallStatus} />
            <button
              className="btn btn-primary"
              onClick={handleRecheck}
              disabled={rechecking}
            >
              {rechecking ? "Checking…" : "Recheck now"}
            </button>
            {recheckMsg && (
              <span className="changed-pill">{recheckMsg}</span>
            )}
          </div>

          <h2>Category Status</h2>
          <div className="category-badges">
            {(["spf", "dkim", "dmarc", "mx"] as const).map((cat) => (
              <span key={cat} className="cat">
                <span style={{ color: "#888", textTransform: "uppercase", fontSize: "0.75rem" }}>
                  {cat}
                </span>
                <Badge kind="status" value={health.categories[cat] as StatusColor} />
              </span>
            ))}
          </div>

          <h2>Findings {health.findings.length > 0 && `(${health.findings.length})`}</h2>
          {health.findings.length === 0 ? (
            <p className="no-results">No findings — everything looks good.</p>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Severity</th>
                    <th>Category</th>
                    <th>Code</th>
                    <th>Message</th>
                  </tr>
                </thead>
                <tbody>
                  {health.findings.map((f, i) => (
                    <tr key={i}>
                      <td>
                        <Badge kind="severity" value={f.severity as Severity} />
                      </td>
                      <td>{f.category}</td>
                      <td>
                        <code style={{ fontSize: "0.8rem" }}>{f.code}</code>
                      </td>
                      <td>{f.message}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}

          <h2>DNS Drift Timeline</h2>
          {snapshots.length === 0 ? (
            <p className="no-results">No snapshots yet.</p>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Captured At</th>
                    <th>Healthy</th>
                    <th>Findings</th>
                    <th>Checksum</th>
                  </tr>
                </thead>
                <tbody>
                  {snapshots.map((s) => (
                    <tr key={s.id}>
                      <td style={{ whiteSpace: "nowrap" }}>{fmt(s.captured_at)}</td>
                      <td>
                        {s.healthy ? (
                          <Badge kind="status" value="ok" />
                        ) : (
                          <Badge kind="status" value="critical" />
                        )}
                      </td>
                      <td>{s.finding_count}</td>
                      <td>
                        <code style={{ fontSize: "0.8rem", color: "#555" }}>
                          {shortHash(s.checksum)}
                        </code>
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

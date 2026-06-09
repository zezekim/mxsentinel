"use client";

import React, { useState, useEffect, useCallback } from "react";
import { apiGet } from "@/lib/api";
import LoadingError from "@/components/LoadingError";

type HeatmapWindow = "24h" | "7d" | "30d";
type HeatmapView = "providers" | "recipients";

interface HeatmapRow {
  provider: string;
  recipient_domain: string;
  delivered: number;
  deferred: number;
  bounced: number;
  rejected: number;
  total: number;
  acceptance_rate: number;
}

interface HeatmapResponse {
  view: HeatmapView;
  window: HeatmapWindow;
  rows: HeatmapRow[];
}

const WINDOWS: { key: HeatmapWindow; label: string }[] = [
  { key: "24h", label: "24h" },
  { key: "7d", label: "7d" },
  { key: "30d", label: "30d" },
];

const VIEWS: { key: HeatmapView; label: string }[] = [
  { key: "providers", label: "By Provider" },
  { key: "recipients", label: "By Domain" },
];

function fmtNum(n: number): string {
  return n.toLocaleString();
}

function fmtPct(rate: number): string {
  return `${(rate * 100).toFixed(1)}%`;
}

function acceptanceBarColor(rate: number): string {
  if (rate >= 0.95) return "#166534"; // green
  if (rate >= 0.8) return "#854d0e";  // yellow/amber
  return "#991b1b";                   // red
}

function acceptanceBgColor(rate: number): string {
  if (rate >= 0.95) return "#dcfce7"; // green-100
  if (rate >= 0.8) return "#fef9c3";  // yellow-100
  return "#fee2e2";                   // red-100
}

function AcceptanceBar({ rate }: { rate: number }) {
  return (
    <div style={{ display: "flex", alignItems: "center", gap: "0.5rem" }}>
      <div
        style={{
          width: "80px",
          height: "10px",
          background: "#e5e7eb",
          borderRadius: "4px",
          overflow: "hidden",
          flexShrink: 0,
        }}
      >
        <div
          style={{
            width: `${Math.min(rate * 100, 100)}%`,
            height: "100%",
            background: acceptanceBarColor(rate),
            borderRadius: "4px",
            transition: "width 0.3s ease",
          }}
        />
      </div>
      <span
        style={{
          fontSize: "0.75rem",
          fontWeight: 600,
          color: acceptanceBarColor(rate),
          background: acceptanceBgColor(rate),
          padding: "1px 5px",
          borderRadius: "3px",
          whiteSpace: "nowrap",
        }}
      >
        {fmtPct(rate)}
      </span>
    </div>
  );
}

export default function HeatmapPage() {
  const [win, setWin] = useState<HeatmapWindow>("7d");
  const [view, setView] = useState<HeatmapView>("providers");
  const [data, setData] = useState<HeatmapResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const fetchData = useCallback(() => {
    setLoading(true);
    setError(null);
    apiGet<HeatmapResponse>(`/v1/heatmap?window=${win}&view=${view}`)
      .then(setData)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, [win, view]);

  useEffect(() => {
    fetchData();
  }, [fetchData]);

  const rows = data?.rows ?? [];
  const sorted = [...rows].sort((a, b) => b.total - a.total);

  const totalSent = rows.reduce((sum, r) => sum + r.total, 0);
  const totalDelivered = rows.reduce((sum, r) => sum + r.delivered, 0);
  const overallRate = totalSent > 0 ? totalDelivered / totalSent : 0;
  const providerCount = new Set(rows.map((r) => (view === "providers" ? r.provider : r.recipient_domain))).size;

  const rowLabel = (r: HeatmapRow) =>
    view === "providers" ? r.provider || "—" : r.recipient_domain || "—";

  const colHeader = view === "providers" ? "Provider" : "Domain";

  return (
    <>
      <h1>Delivery Heatmap</h1>

      <div className="senders-controls">
        <div className="senders-tabs">
          {VIEWS.map((v) => (
            <button
              key={v.key}
              type="button"
              className={view === v.key ? "senders-tab active" : "senders-tab"}
              onClick={() => setView(v.key)}
            >
              {v.label}
            </button>
          ))}
        </div>
        <div className="senders-windows">
          {WINDOWS.map((wn) => (
            <button
              key={wn.key}
              type="button"
              className={win === wn.key ? "senders-win active" : "senders-win"}
              onClick={() => setWin(wn.key)}
            >
              {wn.label}
            </button>
          ))}
        </div>
      </div>

      <LoadingError loading={loading} error={error} />

      {!loading && !error && data && (
        <>
          <div className="stat-cards">
            <div className="stat-card">
              <div className="label">Total Sent</div>
              <div className="value">{fmtNum(totalSent)}</div>
            </div>
            <div className="stat-card">
              <div className="label">Overall Acceptance</div>
              <div
                className="value"
                style={{
                  color: acceptanceBarColor(overallRate),
                }}
              >
                {fmtPct(overallRate)}
              </div>
            </div>
            <div className="stat-card">
              <div className="label">
                {view === "providers" ? "Providers Tracked" : "Domains Tracked"}
              </div>
              <div className="value">{fmtNum(providerCount)}</div>
            </div>
          </div>

          {sorted.length === 0 ? (
            <p className="no-results">No delivery data for this window.</p>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>{colHeader}</th>
                    <th>Delivered</th>
                    <th>Deferred</th>
                    <th>Bounced</th>
                    <th>Rejected</th>
                    <th>Total</th>
                    <th>Acceptance Rate</th>
                  </tr>
                </thead>
                <tbody>
                  {sorted.map((r, i) => (
                    <tr key={`${rowLabel(r)}-${i}`}>
                      <td style={{ fontWeight: 500 }}>{rowLabel(r)}</td>
                      <td>{fmtNum(r.delivered)}</td>
                      <td>{fmtNum(r.deferred)}</td>
                      <td>{fmtNum(r.bounced)}</td>
                      <td>{fmtNum(r.rejected)}</td>
                      <td>{fmtNum(r.total)}</td>
                      <td>
                        <AcceptanceBar rate={r.acceptance_rate} />
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

"use client";

import { useCallback, useEffect, useState } from "react";
import {
  getTopSenders,
  type SenderCount,
  type SenderMetric,
  type SenderWindow,
  type TopSendersResponse,
} from "@/lib/api";
import LoadingError from "@/components/LoadingError";

const METRICS: { key: SenderMetric; label: string }[] = [
  { key: "volume", label: "Total Volume" },
  { key: "spam", label: "Detected Spam" },
  { key: "rejected", label: "Rejections" },
];

const WINDOWS: { key: SenderWindow; label: string }[] = [
  { key: "1h", label: "1h" },
  { key: "24h", label: "24h" },
  { key: "7d", label: "7d" },
  { key: "30d", label: "30d" },
];

function fmtCount(n: number): string {
  return n.toLocaleString();
}

function SenderColumn({
  title,
  rows,
}: {
  title: string;
  rows: SenderCount[];
}) {
  const max = rows.reduce((m, r) => Math.max(m, r.count), 0);
  return (
    <div className="senders-col">
      <h2>{title}</h2>
      {rows.length === 0 ? (
        <p className="no-results">No data in this window.</p>
      ) : (
        <ol className="senders-list">
          {rows.map((r, i) => (
            <li key={r.key} className="senders-row" title={r.key}>
              <span className="senders-rank">{i + 1}</span>
              <span className="senders-key">{r.key}</span>
              <span className="senders-count">{fmtCount(r.count)}</span>
              <span
                className="senders-bar"
                style={{ width: max > 0 ? `${(r.count / max) * 100}%` : "0%" }}
              />
            </li>
          ))}
        </ol>
      )}
    </div>
  );
}

export default function SendersPage() {
  const [metric, setMetric] = useState<SenderMetric>("volume");
  const [window, setWindow] = useState<SenderWindow>("24h");
  const [data, setData] = useState<TopSendersResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const fetchData = useCallback(() => {
    setLoading(true);
    setError(null);
    getTopSenders(metric, window)
      .then(setData)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, [metric, window]);

  useEffect(() => {
    fetchData();
  }, [fetchData]);

  return (
    <>
      <h1>Top Senders</h1>

      <div className="senders-controls">
        <div className="senders-tabs">
          {METRICS.map((m) => (
            <button
              key={m.key}
              type="button"
              className={metric === m.key ? "senders-tab active" : "senders-tab"}
              onClick={() => setMetric(m.key)}
            >
              {m.label}
            </button>
          ))}
        </div>
        <div className="senders-windows">
          {WINDOWS.map((wn) => (
            <button
              key={wn.key}
              type="button"
              className={window === wn.key ? "senders-win active" : "senders-win"}
              onClick={() => setWindow(wn.key)}
            >
              {wn.label}
            </button>
          ))}
        </div>
      </div>

      <LoadingError loading={loading} error={error} />

      {!loading && !error && data && (
        <div className="senders-grid">
          <SenderColumn title="By IP Address" rows={data.by_ip} />
          <SenderColumn title="By Sender ID" rows={data.by_sender} />
          <SenderColumn title="By Sender Domain" rows={data.by_domain} />
        </div>
      )}
    </>
  );
}

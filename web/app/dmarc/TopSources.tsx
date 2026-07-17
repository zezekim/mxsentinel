"use client";

import { useEffect, useState } from "react";
import { apiGet } from "@/lib/api";

interface DmarcSource {
  source_ip: string;
  ptr: string;
  total: number;
  pass: number;
  fail: number;
  pass_rate: number;
}

interface DmarcSourcesResponse {
  sources: DmarcSource[];
}

const MONO = "var(--font-mono, monospace)";

function pct(n: number): string {
  return `${(n * 100).toFixed(1)}%`;
}

export default function TopSources(props: {
  since?: string;
  until?: string;
  domain?: string;
}) {
  const { since, until, domain } = props;
  const [sources, setSources] = useState<DmarcSource[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    setSources(null);
    setError(null);

    const params = new URLSearchParams();
    if (since) params.set("since", since);
    if (until) params.set("until", until);
    if (domain) params.set("domain", domain);
    const qs = params.toString();

    apiGet<DmarcSourcesResponse>(`/v1/dmarc/sources${qs ? `?${qs}` : ""}`)
      .then((d) => {
        if (!cancelled) setSources(d.sources ?? []);
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e));
      });

    return () => {
      cancelled = true;
    };
  }, [since, until, domain]);

  if (error) {
    return <p className="no-results">Failed to load sources: {error}</p>;
  }
  if (sources === null) {
    return <p className="no-results">Loading sources…</p>;
  }
  if (sources.length === 0) {
    return <p className="no-results">No sources in this range.</p>;
  }

  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Source IP</th>
            <th>PTR</th>
            <th style={{ textAlign: "right" }}>Total</th>
            <th style={{ textAlign: "right" }}>Pass</th>
            <th style={{ textAlign: "right" }}>Fail</th>
            <th style={{ width: "30%" }}>Pass Rate</th>
          </tr>
        </thead>
        <tbody>
          {sources.map((s, i) => (
            <tr key={`${s.source_ip}-${i}`}>
              <td style={{ fontFamily: MONO }}>{s.source_ip}</td>
              <td style={{ color: "#64748b" }}>{s.ptr || "—"}</td>
              <td style={{ textAlign: "right", fontFamily: MONO }}>
                {s.total.toLocaleString()}
              </td>
              <td style={{ textAlign: "right", color: "#166534", fontFamily: MONO }}>
                {s.pass.toLocaleString()}
              </td>
              <td style={{ textAlign: "right", color: "#991b1b", fontFamily: MONO }}>
                {s.fail.toLocaleString()}
              </td>
              <td>
                <div
                  style={{
                    display: "flex",
                    alignItems: "center",
                    gap: "0.5rem",
                  }}
                >
                  <div
                    style={{
                      flex: 1,
                      height: "0.5rem",
                      borderRadius: "9999px",
                      background: "#ef4444",
                      overflow: "hidden",
                    }}
                  >
                    <div
                      style={{
                        height: "100%",
                        width: `${s.pass_rate * 100}%`,
                        background: "#22c55e",
                      }}
                    />
                  </div>
                  <span
                    style={{
                      fontFamily: MONO,
                      fontSize: "0.75rem",
                      fontWeight: 600,
                      color:
                        s.pass_rate >= 0.9
                          ? "#166534"
                          : s.pass_rate >= 0.7
                            ? "#854d0e"
                            : "#991b1b",
                      whiteSpace: "nowrap",
                    }}
                  >
                    {pct(s.pass_rate)}
                  </span>
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

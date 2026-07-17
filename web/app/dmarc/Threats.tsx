"use client";

import { useEffect, useState } from "react";
import { apiGet } from "@/lib/api";

interface Threat {
  source_ip: string;
  ptr: string;
  domain: string;
  fail_count: number;
  last_seen: string;
  top_disposition: string;
}

interface ThreatsResponse {
  threats: Threat[];
}

const dispositionBadge: Record<string, string> = {
  reject: "badge badge-critical",
  quarantine: "badge badge-warning",
  none: "badge badge-unknown",
};

function fmtDate(ts: string): string {
  if (!ts) return "—";
  try {
    return new Date(ts).toLocaleDateString();
  } catch {
    return ts;
  }
}

export default function Threats(props: {
  since?: string;
  until?: string;
  domain?: string;
}) {
  const { since, until, domain } = props;
  const [threats, setThreats] = useState<Threat[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    setThreats(null);
    setError(null);

    const params = new URLSearchParams();
    if (since) params.set("since", since);
    if (until) params.set("until", until);
    if (domain) params.set("domain", domain);
    const qs = params.toString();

    apiGet<ThreatsResponse>(`/v1/dmarc/threats${qs ? `?${qs}` : ""}`)
      .then((d) => {
        if (!cancelled) setThreats(d.threats ?? []);
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e));
      });

    return () => {
      cancelled = true;
    };
  }, [since, until, domain]);

  if (error)
    return <p className="no-results">Failed to load threats: {error}</p>;
  if (threats === null)
    return <p className="no-results">Loading threats…</p>;
  if (threats.length === 0)
    return <p className="no-results">No spoofing threats detected.</p>;

  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Source IP</th>
            <th>PTR</th>
            <th>Spoofed Domain</th>
            <th style={{ textAlign: "right" }}>Fail Count</th>
            <th>Last Seen</th>
            <th style={{ textAlign: "center" }}>Disposition</th>
          </tr>
        </thead>
        <tbody>
          {threats.map((t, i) => (
            <tr key={`${t.source_ip}-${t.domain}-${i}`}>
              <td style={{ fontFamily: "var(--font-mono, monospace)" }}>
                {t.source_ip}
              </td>
              <td>{t.ptr || "—"}</td>
              <td>{t.domain || "—"}</td>
              <td
                style={{
                  textAlign: "right",
                  color: "#991b1b",
                  fontFamily: "var(--font-mono, monospace)",
                }}
              >
                {t.fail_count.toLocaleString()}
              </td>
              <td style={{ whiteSpace: "nowrap" }}>{fmtDate(t.last_seen)}</td>
              <td style={{ textAlign: "center" }}>
                <span
                  className={
                    dispositionBadge[t.top_disposition] ?? "badge badge-unknown"
                  }
                >
                  {t.top_disposition || "—"}
                </span>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

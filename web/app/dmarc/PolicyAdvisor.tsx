"use client";

import { useEffect, useState } from "react";
import { apiGet } from "@/lib/api";

interface PolicyAdvice {
  domain: string;
  total_messages: number;
  pass_rate: number;
  published_policy: string;
  recommended_policy: string;
  rationale: string;
}

interface PolicyAdviceResponse {
  domains: PolicyAdvice[];
}

function pct(n: number): string {
  return `${(n * 100).toFixed(1)}%`;
}

function passRateColor(rate: number): string {
  if (rate >= 0.95) return "#166534";
  if (rate >= 0.8) return "#854d0e";
  return "#991b1b";
}

const recommendedBadge: Record<string, string> = {
  reject: "badge badge-critical",
  quarantine: "badge badge-warning",
  none: "badge badge-ok",
};

export default function PolicyAdvisor(props: { since?: string; until?: string }) {
  const { since, until } = props;
  const [domains, setDomains] = useState<PolicyAdvice[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    setDomains(null);
    setError(null);

    const params = new URLSearchParams();
    if (since) params.set("since", since);
    if (until) params.set("until", until);
    const qs = params.toString();

    apiGet<PolicyAdviceResponse>(`/v1/dmarc/policy-advice${qs ? `?${qs}` : ""}`)
      .then((d) => {
        if (!cancelled) setDomains(d.domains ?? []);
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e));
      });

    return () => {
      cancelled = true;
    };
  }, [since, until]);

  if (error) {
    return <p className="no-results">Failed to load policy advice: {error}</p>;
  }
  if (domains === null) {
    return <p className="no-results">Loading policy advice…</p>;
  }
  if (domains.length === 0) {
    return <p className="no-results">No domains observed.</p>;
  }

  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Domain</th>
            <th style={{ textAlign: "right" }}>Messages</th>
            <th style={{ textAlign: "right" }}>Pass Rate</th>
            <th>Published</th>
            <th style={{ textAlign: "center" }}>Recommended</th>
            <th>Rationale</th>
          </tr>
        </thead>
        <tbody>
          {domains.map((d) => (
            <tr key={d.domain}>
              <td>{d.domain}</td>
              <td style={{ textAlign: "right", fontFamily: "var(--font-mono, monospace)" }}>
                {d.total_messages.toLocaleString()}
              </td>
              <td
                style={{
                  textAlign: "right",
                  fontFamily: "var(--font-mono, monospace)",
                  fontWeight: 600,
                  color: passRateColor(d.pass_rate),
                }}
              >
                {pct(d.pass_rate)}
              </td>
              <td style={{ fontFamily: "var(--font-mono, monospace)" }}>
                {d.published_policy || "—"}
              </td>
              <td style={{ textAlign: "center" }}>
                <span className={recommendedBadge[d.recommended_policy] ?? "badge badge-unknown"}>
                  {d.recommended_policy}
                </span>
              </td>
              <td style={{ color: "#64748b", fontSize: "0.8rem" }}>{d.rationale}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

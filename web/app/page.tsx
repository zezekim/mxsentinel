"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { apiGet, type DomainsResponse, type DomainSummary, type StatusColor, type OverallStatus } from "@/lib/api";
import Badge from "@/components/Badge";
import LoadingError from "@/components/LoadingError";

function fmt(ts: string | null): string {
  if (!ts) return "—";
  return new Date(ts).toLocaleString();
}

export default function DomainsPage() {
  const [domains, setDomains] = useState<DomainSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    apiGet<DomainsResponse>("/v1/domains")
      .then((d) => setDomains(d.domains))
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, []);

  return (
    <>
      <h1>Domains</h1>
      <LoadingError loading={loading} error={error} />
      {!loading && !error && (
        <>
          {domains.length === 0 ? (
            <p className="no-results">No domains found.</p>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Domain</th>
                    <th>Overall</th>
                    <th>SPF</th>
                    <th>DKIM</th>
                    <th>DMARC</th>
                    <th>MX</th>
                    <th>Findings</th>
                    <th>Last Checked</th>
                  </tr>
                </thead>
                <tbody>
                  {domains.map((d) => (
                    <tr key={d.id}>
                      <td>
                        <Link href={`/domains/${d.id}`}>{d.name}</Link>
                      </td>
                      <td>
                        <Badge kind="status" value={d.overall as OverallStatus} />
                      </td>
                      <td>
                        <Badge kind="status" value={d.categories.spf as StatusColor} />
                      </td>
                      <td>
                        <Badge kind="status" value={d.categories.dkim as StatusColor} />
                      </td>
                      <td>
                        <Badge kind="status" value={d.categories.dmarc as StatusColor} />
                      </td>
                      <td>
                        <Badge kind="status" value={d.categories.mx as StatusColor} />
                      </td>
                      <td>{d.finding_count}</td>
                      <td style={{ whiteSpace: "nowrap" }}>{fmt(d.last_checked_at)}</td>
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

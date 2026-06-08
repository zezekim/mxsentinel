"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import {
  apiGet,
  createDomain,
  type DomainsResponse,
  type DomainSummary,
  type StatusColor,
  type OverallStatus,
} from "@/lib/api";
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

  const [showAdd, setShowAdd] = useState(false);
  const [addName, setAddName] = useState("");
  const [adding, setAdding] = useState(false);
  const [addError, setAddError] = useState<string | null>(null);

  function loadDomains() {
    return apiGet<DomainsResponse>("/v1/domains").then((d) => setDomains(d.domains));
  }

  useEffect(() => {
    loadDomains()
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  function handleAdd(e: React.FormEvent) {
    e.preventDefault();
    const name = addName.trim().toLowerCase();
    if (!name) return;
    setAdding(true);
    setAddError(null);
    createDomain(name)
      .then(() => {
        setShowAdd(false);
        setAddName("");
        return loadDomains();
      })
      .catch((e: unknown) => setAddError(e instanceof Error ? e.message : String(e)))
      .finally(() => setAdding(false));
  }

  return (
    <>
      <div style={{ display: "flex", alignItems: "center", gap: "1rem", marginBottom: "0.5rem" }}>
        <h1 style={{ margin: 0 }}>Domains</h1>
        <button
          className="btn btn-primary"
          onClick={() => { setShowAdd((v) => !v); setAddError(null); setAddName(""); }}
        >
          {showAdd ? "Cancel" : "+ Add Domain"}
        </button>
      </div>

      {showAdd && (
        <form onSubmit={handleAdd} style={{ margin: "0.75rem 0 1.25rem", display: "flex", gap: "0.5rem", alignItems: "center" }}>
          <input
            type="text"
            value={addName}
            onChange={(e) => setAddName(e.target.value)}
            placeholder="example.com"
            style={{ padding: "0.4rem 0.6rem", minWidth: "220px" }}
            autoFocus
            disabled={adding}
          />
          <button className="btn btn-primary" type="submit" disabled={adding || !addName.trim()}>
            {adding ? "Adding…" : "Add"}
          </button>
          {addError && <span style={{ color: "#e55" }}>{addError}</span>}
        </form>
      )}

      <LoadingError loading={loading} error={error} />
      {!loading && !error && (
        <>
          {domains.length === 0 ? (
            <p className="no-results">No domains found. Add one above.</p>
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

"use client";

import React, { useCallback, useEffect, useState } from "react";
import LoadingError from "@/components/LoadingError";
import {
  listBimi,
  getBimiDetail,
  recheckBimi,
  type BimiSummaryItem,
  type BimiDetail,
  type BimiReadiness,
  type ChecklistStatus,
} from "@/lib/api-bimi";

// ─── Presentation helpers ─────────────────────────────────────────────────────

const READINESS_META: Record<
  BimiReadiness,
  { label: string; bg: string; fg: string }
> = {
  ready: { label: "Ready", bg: "#dcfce7", fg: "#15803d" },
  partial: { label: "Partial (no VMC)", bg: "#dbeafe", fg: "#1d4ed8" },
  vmc_expired: { label: "VMC expired", bg: "#fee2e2", fg: "#dc2626" },
  blocked: { label: "Blocked", bg: "#fef3c7", fg: "#b45309" },
  not_configured: { label: "Not configured", bg: "#f3f4f6", fg: "#6b7280" },
  unknown: { label: "Not checked", bg: "#f3f4f6", fg: "#9ca3af" },
};

function ReadinessBadge({ state }: { state: BimiReadiness }) {
  const m = READINESS_META[state] ?? READINESS_META.unknown;
  return (
    <span
      style={{
        padding: "2px 10px",
        borderRadius: 9999,
        fontSize: "0.75rem",
        fontWeight: 600,
        background: m.bg,
        color: m.fg,
        whiteSpace: "nowrap",
      }}
    >
      {m.label}
    </span>
  );
}

const CHECK_META: Record<ChecklistStatus, { icon: string; color: string }> = {
  ok: { icon: "✓", color: "#15803d" },
  warn: { icon: "!", color: "#b45309" },
  fail: { icon: "✗", color: "#dc2626" },
};

function fmtDate(ts: string | null): string {
  if (!ts) return "—";
  try {
    return new Date(ts).toLocaleDateString();
  } catch {
    return ts;
  }
}

// ─── Detail (expanded row) ─────────────────────────────────────────────────────

function DomainDetail({ domainId }: { domainId: string }) {
  const [detail, setDetail] = useState<BimiDetail | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [rechecking, setRechecking] = useState(false);

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    getBimiDetail(domainId)
      .then(setDetail)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, [domainId]);

  useEffect(() => {
    load();
  }, [load]);

  function handleRecheck() {
    setRechecking(true);
    setError(null);
    recheckBimi(domainId)
      .then(setDetail)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setRechecking(false));
  }

  return (
    <div
      style={{
        padding: "16px 24px",
        background: "var(--surface-2, #f9fafb)",
        borderTop: "1px solid var(--border, #e5e7eb)",
      }}
    >
      <div
        style={{
          display: "flex",
          justifyContent: "space-between",
          alignItems: "center",
          marginBottom: 12,
          gap: 12,
          flexWrap: "wrap",
        }}
      >
        <span style={{ fontWeight: 700 }}>What&rsquo;s blocking BIMI</span>
        <button
          className="btn btn-sm"
          onClick={handleRecheck}
          disabled={rechecking}
        >
          {rechecking ? "Checking…" : "Check now"}
        </button>
      </div>

      <LoadingError loading={loading} error={error} />

      {detail && !loading && (
        <>
          <ul style={{ listStyle: "none", margin: 0, padding: 0, display: "flex", flexDirection: "column", gap: 8 }}>
            {detail.checklist.length === 0 && (
              <li className="no-results" style={{ margin: 0 }}>
                No assessment yet — run “Check now”.
              </li>
            )}
            {detail.checklist.map((item) => {
              const meta = CHECK_META[item.status];
              return (
                <li
                  key={item.code}
                  style={{ display: "flex", gap: 10, alignItems: "flex-start" }}
                >
                  <span
                    style={{
                      color: meta.color,
                      fontWeight: 800,
                      width: 16,
                      textAlign: "center",
                    }}
                  >
                    {meta.icon}
                  </span>
                  <span>
                    <strong>{item.label}</strong>
                    <div style={{ fontSize: "0.85rem", color: "#6b7280" }}>
                      {item.detail}
                    </div>
                  </span>
                </li>
              );
            })}
          </ul>

          {detail.record && (
            <div style={{ marginTop: 14 }}>
              <div style={{ fontSize: "0.75rem", fontWeight: 600, color: "#6b7280" }}>
                BIMI record
              </div>
              <code style={{ fontSize: "0.8rem", wordBreak: "break-all" }}>
                {detail.record}
              </code>
            </div>
          )}
        </>
      )}
    </div>
  );
}

// ─── Main page ────────────────────────────────────────────────────────────────

export default function BimiPage() {
  const [rows, setRows] = useState<BimiSummaryItem[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [expanded, setExpanded] = useState<Set<string>>(new Set());

  const fetchRows = useCallback(() => {
    setLoading(true);
    setError(null);
    listBimi()
      .then((d) => setRows(d.domains ?? []))
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    fetchRows();
  }, [fetchRows]);

  function toggle(id: string) {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  return (
    <>
      <div style={{ marginBottom: 20 }}>
        <h1 style={{ margin: 0 }}>BIMI &amp; VMC Readiness</h1>
        <p style={{ color: "#6b7280", marginTop: 6 }}>
          BIMI displays your brand logo beside authenticated mail — the visible
          payoff of reaching DMARC enforcement. Expand a domain to see what&rsquo;s
          blocking it.
        </p>
      </div>

      <LoadingError loading={loading} error={error} />

      {!loading && !error && (
        <>
          {rows.length === 0 ? (
            <p className="no-results">No domains found.</p>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Domain</th>
                    <th>Readiness</th>
                    <th>DMARC</th>
                    <th>Logo</th>
                    <th>VMC</th>
                    <th>VMC Expiry</th>
                    <th>Checked</th>
                    <th></th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((row) => {
                    const open = expanded.has(row.domain_id);
                    return (
                      <React.Fragment key={row.domain_id}>
                        <tr
                          onClick={() => toggle(row.domain_id)}
                          aria-expanded={open}
                          style={{ cursor: "pointer" }}
                        >
                          <td style={{ fontWeight: 600 }}>{row.domain}</td>
                          <td>
                            <ReadinessBadge state={row.readiness_state} />
                          </td>
                          <td>{row.dmarc_enforced ? "Enforced" : "—"}</td>
                          <td>{row.logo_url ? "Yes" : "—"}</td>
                          <td>{row.vmc_url ? "Yes" : "—"}</td>
                          <td style={{ whiteSpace: "nowrap" }}>
                            {fmtDate(row.vmc_expiry)}
                          </td>
                          <td style={{ whiteSpace: "nowrap" }}>
                            {fmtDate(row.checked_at)}
                          </td>
                          <td style={{ fontSize: "0.75rem", color: "#9ca3af" }}>
                            {open ? "▲" : "▼"}
                          </td>
                        </tr>
                        {open && (
                          <tr>
                            <td colSpan={8} style={{ padding: 0 }}>
                              <DomainDetail domainId={row.domain_id} />
                            </td>
                          </tr>
                        )}
                      </React.Fragment>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
    </>
  );
}

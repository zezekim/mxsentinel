"use client";

import React, { useCallback, useEffect, useState } from "react";
import LoadingError from "@/components/LoadingError";
import {
  listSeedLists,
  createSeedList,
  deleteSeedList,
  listSeedRuns,
  startSeedRun,
  getSeedRun,
  type SeedList,
  type SeedRun,
  type SeedRunDetail,
  type SeedResult,
  type ProviderSummary,
  type Placement,
  type RunStatus,
} from "@/lib/api-inboxplacement";

// ─── Helpers ────────────────────────────────────────────────────────────────

function fmtDate(ts: string | null): string {
  if (!ts) return "—";
  try {
    return new Date(ts).toLocaleString();
  } catch {
    return ts;
  }
}

function pct(x: number): string {
  return `${(x * 100).toFixed(0)}%`;
}

const PLACEMENT_STYLE: Record<Placement, React.CSSProperties> = {
  inbox: { background: "#dcfce7", color: "#15803d" },
  spam: { background: "#fee2e2", color: "#dc2626" },
  missing: { background: "#f3f4f6", color: "#6b7280" },
  unknown: { background: "#fef9c3", color: "#a16207" },
};

const RUN_STATUS_STYLE: Record<RunStatus, React.CSSProperties> = {
  pending: { background: "#fef9c3", color: "#a16207" },
  sending: { background: "#dbeafe", color: "#1d4ed8" },
  collecting: { background: "#dbeafe", color: "#1d4ed8" },
  completed: { background: "#dcfce7", color: "#15803d" },
  failed: { background: "#fee2e2", color: "#dc2626" },
};

function Pill({ style, children }: { style: React.CSSProperties; children: React.ReactNode }) {
  return (
    <span
      style={{
        padding: "2px 10px",
        borderRadius: 9999,
        fontSize: "0.75rem",
        fontWeight: 600,
        textTransform: "capitalize",
        ...style,
      }}
    >
      {children}
    </span>
  );
}

function AuthCell({ v }: { v: boolean | null }) {
  if (v === null || v === undefined) return <span style={{ color: "#9ca3af" }}>—</span>;
  return v ? (
    <span style={{ color: "#15803d", fontWeight: 700 }}>pass</span>
  ) : (
    <span style={{ color: "#dc2626", fontWeight: 700 }}>fail</span>
  );
}

// ─── Placement bar (inbox / spam / missing) ─────────────────────────────────

function PlacementBar({ s }: { s: ProviderSummary }) {
  const resolved = s.inbox + s.spam + s.missing;
  if (resolved === 0) {
    return <span style={{ color: "#9ca3af", fontSize: "0.8rem" }}>pending</span>;
  }
  const seg = (n: number, color: string) =>
    n > 0 ? <div style={{ width: `${(n / resolved) * 100}%`, background: color }} /> : null;
  return (
    <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
      <div
        style={{
          display: "flex",
          height: 10,
          width: 160,
          borderRadius: 5,
          overflow: "hidden",
          background: "#e5e7eb",
        }}
      >
        {seg(s.inbox, "#22c55e")}
        {seg(s.spam, "#ef4444")}
        {seg(s.missing, "#9ca3af")}
      </div>
      <span style={{ fontSize: "0.75rem", color: "#6b7280", whiteSpace: "nowrap" }}>
        {pct(s.inbox_rate)} inbox
      </span>
    </div>
  );
}

// ─── Run detail (expanded) ──────────────────────────────────────────────────

function RunDetail({ runId }: { runId: string }) {
  const [detail, setDetail] = useState<SeedRunDetail | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    getSeedRun(runId)
      .then(setDetail)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, [runId]);

  useEffect(() => {
    load();
  }, [load]);

  return (
    <div
      style={{
        padding: "16px 24px",
        background: "var(--surface-2, #f9fafb)",
        borderTop: "1px solid var(--border, #e5e7eb)",
      }}
    >
      <div style={{ display: "flex", justifyContent: "flex-end", marginBottom: 8 }}>
        <button className="btn btn-sm" onClick={load}>
          Refresh
        </button>
      </div>
      <LoadingError loading={loading} error={error} />
      {detail && !loading && (
        <>
          <h4 style={{ margin: "0 0 8px" }}>Placement by provider</h4>
          <div className="table-wrap" style={{ marginBottom: 20 }}>
            <table>
              <thead>
                <tr>
                  <th>Provider</th>
                  <th>Placement</th>
                  <th style={{ textAlign: "right" }}>Inbox</th>
                  <th style={{ textAlign: "right" }}>Spam</th>
                  <th style={{ textAlign: "right" }}>Missing</th>
                  <th style={{ textAlign: "right" }}>Pending</th>
                  <th style={{ textAlign: "right" }}>SPF</th>
                  <th style={{ textAlign: "right" }}>DKIM</th>
                  <th style={{ textAlign: "right" }}>DMARC</th>
                </tr>
              </thead>
              <tbody>
                {detail.summary.providers.map((p) => (
                  <tr key={p.provider}>
                    <td style={{ fontWeight: 600, textTransform: "capitalize" }}>{p.provider}</td>
                    <td>
                      <PlacementBar s={p} />
                    </td>
                    <td style={{ textAlign: "right" }}>{p.inbox}</td>
                    <td style={{ textAlign: "right" }}>{p.spam}</td>
                    <td style={{ textAlign: "right" }}>{p.missing}</td>
                    <td style={{ textAlign: "right" }}>{p.pending}</td>
                    <td style={{ textAlign: "right" }}>{pct(p.spf_pass_rate)}</td>
                    <td style={{ textAlign: "right" }}>{pct(p.dkim_pass_rate)}</td>
                    <td style={{ textAlign: "right" }}>{pct(p.dmarc_pass_rate)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          <h4 style={{ margin: "0 0 8px" }}>Per-seed results</h4>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Seed</th>
                  <th>Provider</th>
                  <th>Placement</th>
                  <th>Folder</th>
                  <th>SPF</th>
                  <th>DKIM</th>
                  <th>DMARC</th>
                  <th>Observed</th>
                </tr>
              </thead>
              <tbody>
                {detail.results.map((r: SeedResult) => (
                  <tr key={r.address}>
                    <td style={{ fontFamily: "monospace", fontSize: "0.8rem" }}>{r.address}</td>
                    <td style={{ textTransform: "capitalize" }}>{r.provider}</td>
                    <td>
                      <Pill style={PLACEMENT_STYLE[r.placement]}>
                        {r.status === "sent" ? "pending" : r.placement}
                      </Pill>
                    </td>
                    <td>{r.mailbox || "—"}</td>
                    <td>
                      <AuthCell v={r.spf_pass} />
                    </td>
                    <td>
                      <AuthCell v={r.dkim_pass} />
                    </td>
                    <td>
                      <AuthCell v={r.dmarc_pass} />
                    </td>
                    <td style={{ whiteSpace: "nowrap" }}>{fmtDate(r.observed_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
    </div>
  );
}

// ─── New seed list form ─────────────────────────────────────────────────────

function NewListForm({ onCreated, onCancel }: { onCreated: () => void; onCancel: () => void }) {
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [addressesText, setAddressesText] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim()) return;
    const addresses = addressesText
      .split(/[\n,]+/)
      .map((a) => a.trim())
      .filter(Boolean)
      .map((address) => ({ address }));
    setSubmitting(true);
    setError(null);
    createSeedList({ name: name.trim(), description: description.trim(), addresses })
      .then(() => onCreated())
      .catch((err: unknown) => {
        setError(err instanceof Error ? err.message : String(err));
        setSubmitting(false);
      });
  }

  return (
    <form
      onSubmit={handleSubmit}
      style={{
        background: "var(--surface-2, #f9fafb)",
        border: "1px solid var(--border, #e5e7eb)",
        borderRadius: 8,
        padding: "20px 24px",
        marginBottom: 24,
        display: "flex",
        flexDirection: "column",
        gap: 14,
        maxWidth: 560,
      }}
    >
      <h3 style={{ margin: 0, fontSize: "1rem", fontWeight: 700 }}>New Seed List</h3>
      {error && <p className="state-msg error" style={{ margin: 0 }}>Error: {error}</p>}

      <label style={{ display: "flex", flexDirection: "column", gap: 4 }}>
        <span style={{ fontSize: "0.8rem", fontWeight: 600, color: "#6b7280" }}>List Name</span>
        <input type="text" required value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. Q3 Deliverability Panel" />
      </label>

      <label style={{ display: "flex", flexDirection: "column", gap: 4 }}>
        <span style={{ fontSize: "0.8rem", fontWeight: 600, color: "#6b7280" }}>Description</span>
        <input type="text" value={description} onChange={(e) => setDescription(e.target.value)} />
      </label>

      <label style={{ display: "flex", flexDirection: "column", gap: 4 }}>
        <span style={{ fontSize: "0.8rem", fontWeight: 600, color: "#6b7280" }}>
          Seed Addresses (one per line or comma-separated)
        </span>
        <textarea
          rows={4}
          value={addressesText}
          onChange={(e) => setAddressesText(e.target.value)}
          placeholder={"seed1@gmail.com\nseed2@outlook.com\nseed3@yahoo.com"}
          style={{ fontFamily: "monospace", fontSize: "0.85rem" }}
        />
        <span style={{ fontSize: "0.72rem", color: "#9ca3af" }}>
          Provider is inferred from each address domain.
        </span>
      </label>

      <div style={{ display: "flex", gap: 10 }}>
        <button type="submit" className="btn btn-primary btn-sm" disabled={submitting}>
          {submitting ? "Creating…" : "Create List"}
        </button>
        <button type="button" className="btn btn-sm" onClick={onCancel} disabled={submitting}>
          Cancel
        </button>
      </div>
    </form>
  );
}

// ─── Main page ──────────────────────────────────────────────────────────────

export default function InboxPlacementPage() {
  const [lists, setLists] = useState<SeedList[]>([]);
  const [runs, setRuns] = useState<SeedRun[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [showNewList, setShowNewList] = useState(false);
  const [expandedRun, setExpandedRun] = useState<string | null>(null);

  const fetchAll = useCallback(() => {
    setLoading(true);
    setError(null);
    Promise.all([listSeedLists(), listSeedRuns()])
      .then(([l, r]) => {
        setLists(l.lists ?? []);
        setRuns(r.runs ?? []);
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    fetchAll();
  }, [fetchAll]);

  function handleStart(listId: string) {
    setActionError(null);
    startSeedRun({ list_id: listId })
      .then(() => fetchAll())
      .catch((e: unknown) => setActionError(e instanceof Error ? e.message : String(e)));
  }

  function handleDeleteList(id: string) {
    if (!confirm("Delete this seed list? Existing run history is kept.")) return;
    setActionError(null);
    deleteSeedList(id)
      .then(() => fetchAll())
      .catch((e: unknown) => setActionError(e instanceof Error ? e.message : String(e)));
  }

  return (
    <>
      <div
        style={{
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          marginBottom: 8,
          flexWrap: "wrap",
          gap: 12,
        }}
      >
        <h1 style={{ margin: 0 }}>Inbox Placement</h1>
        {!showNewList && <button onClick={() => setShowNewList(true)}>+ New Seed List</button>}
      </div>
      <p style={{ color: "#6b7280", marginTop: 0, maxWidth: 720 }}>
        Send uniquely-tagged probe messages to a seed list across providers, then measure inbox
        vs. spam vs. missing placement per provider and IP. Probes are synthetic test content —
        no real mail is read.
      </p>

      {showNewList && <NewListForm onCreated={() => { setShowNewList(false); fetchAll(); }} onCancel={() => setShowNewList(false)} />}

      {actionError && <p className="state-msg error">Error: {actionError}</p>}

      <LoadingError loading={loading} error={error} />

      {!loading && !error && (
        <>
          <h2 style={{ fontSize: "1.1rem", marginBottom: 12 }}>Seed Lists</h2>
          {lists.length === 0 ? (
            <p className="no-results">No seed lists yet. Create one to begin.</p>
          ) : (
            <div className="table-wrap" style={{ marginBottom: 32 }}>
              <table>
                <thead>
                  <tr>
                    <th>Name</th>
                    <th>Description</th>
                    <th style={{ textAlign: "right" }}>Seeds</th>
                    <th>Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {lists.map((l) => (
                    <tr key={l.id}>
                      <td style={{ fontWeight: 600 }}>{l.name}</td>
                      <td style={{ color: "#6b7280" }}>{l.description || "—"}</td>
                      <td style={{ textAlign: "right" }}>{l.address_count}</td>
                      <td style={{ whiteSpace: "nowrap" }}>
                        <button
                          className="btn btn-primary btn-sm"
                          style={{ marginRight: 6 }}
                          disabled={l.address_count === 0}
                          onClick={() => handleStart(l.id)}
                        >
                          Run Test
                        </button>
                        <button
                          className="btn btn-sm"
                          style={{ background: "#fee2e2", color: "#dc2626", border: "1px solid #fca5a5" }}
                          onClick={() => handleDeleteList(l.id)}
                        >
                          Delete
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}

          <h2 style={{ fontSize: "1.1rem", marginBottom: 12 }}>Seed Test Runs</h2>
          {runs.length === 0 ? (
            <p className="no-results">No runs yet. Run a test from a seed list above.</p>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Started</th>
                    <th>Status</th>
                    <th style={{ textAlign: "right" }}>Seeds</th>
                    <th style={{ textAlign: "right" }}>Sent</th>
                    <th>Run Tag</th>
                    <th></th>
                  </tr>
                </thead>
                <tbody>
                  {runs.map((run) => {
                    const expanded = expandedRun === run.id;
                    return (
                      <React.Fragment key={run.id}>
                        <tr
                          onClick={() => setExpandedRun(expanded ? null : run.id)}
                          aria-expanded={expanded}
                          style={{ cursor: "pointer" }}
                        >
                          <td style={{ whiteSpace: "nowrap" }}>{fmtDate(run.created_at)}</td>
                          <td>
                            <Pill style={RUN_STATUS_STYLE[run.status]}>{run.status}</Pill>
                          </td>
                          <td style={{ textAlign: "right" }}>{run.seed_count}</td>
                          <td style={{ textAlign: "right" }}>{run.sent_count}</td>
                          <td style={{ fontFamily: "monospace", fontSize: "0.8rem" }}>{run.run_tag}</td>
                          <td style={{ fontSize: "0.75rem", color: "#9ca3af" }}>{expanded ? "▲" : "▼"}</td>
                        </tr>
                        {expanded && (
                          <tr>
                            <td colSpan={6} style={{ padding: 0 }}>
                              <RunDetail runId={run.id} />
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

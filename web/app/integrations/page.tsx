"use client";

import React, { useEffect, useState, useCallback } from "react";
import { apiGet, apiPost, apiPatch, apiDelete, type CpanelServer, type CpanelAccount, type WhmcsConnection, type WhmcsPushLogEntry } from "@/lib/api";
import LoadingError from "@/components/LoadingError";

function fmtDate(ts: string | null): string {
  if (!ts) return "—";
  try {
    return new Date(ts).toLocaleString();
  } catch {
    return ts;
  }
}

function truncate(s: string, n = 40): string {
  return s.length > n ? s.slice(0, n) + "…" : s;
}

// ─── cPanel inline form ───────────────────────────────────────────────────────

interface CpanelFormData {
  label: string;
  hostname: string;
  port: number;
  username: string;
  api_token: string;
  verify_ssl: boolean;
  sync_interval_hours: number;
}

const emptyCpanelForm = (): CpanelFormData => ({
  label: "",
  hostname: "",
  port: 2087,
  username: "",
  api_token: "",
  verify_ssl: true,
  sync_interval_hours: 4,
});

function CpanelForm({
  initial,
  onSave,
  onCancel,
}: {
  initial?: CpanelFormData;
  onSave: (data: CpanelFormData) => Promise<void>;
  onCancel: () => void;
}) {
  const [form, setForm] = useState<CpanelFormData>(initial ?? emptyCpanelForm());
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  function set<K extends keyof CpanelFormData>(k: K, v: CpanelFormData[K]) {
    setForm((f) => ({ ...f, [k]: v }));
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setSaving(true);
    setErr(null);
    try {
      await onSave(form);
    } catch (ex) {
      setErr(ex instanceof Error ? ex.message : String(ex));
      setSaving(false);
    }
  }

  return (
    <form className="inline-form" onSubmit={handleSubmit}>
      <div className="form-row">
        <label>Label</label>
        <input value={form.label} onChange={(e) => set("label", e.target.value)} required />
      </div>
      <div className="form-row">
        <label>Hostname</label>
        <input value={form.hostname} onChange={(e) => set("hostname", e.target.value)} required />
      </div>
      <div className="form-row">
        <label>Port</label>
        <input type="number" value={form.port} onChange={(e) => set("port", Number(e.target.value))} required />
      </div>
      <div className="form-row">
        <label>Username</label>
        <input value={form.username} onChange={(e) => set("username", e.target.value)} required />
      </div>
      <div className="form-row">
        <label>API Token</label>
        <input type="password" value={form.api_token} onChange={(e) => set("api_token", e.target.value)} placeholder={initial ? "(unchanged)" : ""} />
      </div>
      <div className="form-row">
        <label>Sync Interval (hours)</label>
        <input type="number" min={1} value={form.sync_interval_hours} onChange={(e) => set("sync_interval_hours", Number(e.target.value))} required />
      </div>
      <div className="form-row">
        <label>
          <input type="checkbox" checked={form.verify_ssl} onChange={(e) => set("verify_ssl", e.target.checked)} />
          {" "}Verify SSL
        </label>
      </div>
      {err && <div className="inline-error">{err}</div>}
      <div className="form-actions">
        <button type="submit" className="btn btn-sm" disabled={saving}>
          {saving ? "Saving…" : "Save"}
        </button>
        <button type="button" className="btn btn-sm" onClick={onCancel} disabled={saving}>
          Cancel
        </button>
      </div>
    </form>
  );
}

// ─── WHMCS inline form ────────────────────────────────────────────────────────

interface WhmcsFormData {
  label: string;
  api_url: string;
  api_identifier: string;
  api_secret: string;
  push_frequency: "daily" | "weekly";
}

const emptyWhmcsForm = (): WhmcsFormData => ({
  label: "",
  api_url: "",
  api_identifier: "",
  api_secret: "",
  push_frequency: "daily",
});

function WhmcsForm({
  initial,
  onSave,
  onCancel,
}: {
  initial?: WhmcsFormData;
  onSave: (data: WhmcsFormData) => Promise<void>;
  onCancel: () => void;
}) {
  const [form, setForm] = useState<WhmcsFormData>(initial ?? emptyWhmcsForm());
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  function set<K extends keyof WhmcsFormData>(k: K, v: WhmcsFormData[K]) {
    setForm((f) => ({ ...f, [k]: v }));
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setSaving(true);
    setErr(null);
    try {
      await onSave(form);
    } catch (ex) {
      setErr(ex instanceof Error ? ex.message : String(ex));
      setSaving(false);
    }
  }

  return (
    <form className="inline-form" onSubmit={handleSubmit}>
      <div className="form-row">
        <label>Label</label>
        <input value={form.label} onChange={(e) => set("label", e.target.value)} required />
      </div>
      <div className="form-row">
        <label>API URL</label>
        <input type="url" value={form.api_url} onChange={(e) => set("api_url", e.target.value)} required />
      </div>
      <div className="form-row">
        <label>API Identifier</label>
        <input value={form.api_identifier} onChange={(e) => set("api_identifier", e.target.value)} required />
      </div>
      <div className="form-row">
        <label>API Secret</label>
        <input type="password" value={form.api_secret} onChange={(e) => set("api_secret", e.target.value)} placeholder={initial ? "(unchanged)" : ""} />
      </div>
      <div className="form-row">
        <label>Push Frequency</label>
        <select value={form.push_frequency} onChange={(e) => set("push_frequency", e.target.value as "daily" | "weekly")}>
          <option value="daily">Daily</option>
          <option value="weekly">Weekly</option>
        </select>
      </div>
      {err && <div className="inline-error">{err}</div>}
      <div className="form-actions">
        <button type="submit" className="btn btn-sm" disabled={saving}>
          {saving ? "Saving…" : "Save"}
        </button>
        <button type="button" className="btn btn-sm" onClick={onCancel} disabled={saving}>
          Cancel
        </button>
      </div>
    </form>
  );
}

// ─── cPanel status badge ──────────────────────────────────────────────────────

function SyncStatusBadge({ status }: { status: CpanelServer["sync_status"] }) {
  const colors: Record<string, string> = {
    ok: "badge-ok",
    error: "badge-error",
    syncing: "badge-syncing",
    pending: "badge-pending",
  };
  return <span className={`badge ${colors[status] ?? "badge-pending"}`}>{status}</span>;
}

// ─── cPanel tab ───────────────────────────────────────────────────────────────

function CpanelTab() {
  const [servers, setServers] = useState<CpanelServer[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [showAdd, setShowAdd] = useState(false);
  const [editId, setEditId] = useState<string | null>(null);
  const [expandedIds, setExpandedIds] = useState<Set<string>>(new Set());
  const [accounts, setAccounts] = useState<Record<string, CpanelAccount[]>>({});
  const [syncing, setSyncing] = useState<Record<string, boolean>>({});
  const [syncMsg, setSyncMsg] = useState<Record<string, string>>({});

  const fetchServers = useCallback(() => {
    setLoading(true);
    setError(null);
    apiGet<{ servers: CpanelServer[] }>("/v1/integrations/cpanel")
      .then((d) => setServers(d.servers ?? []))
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => { fetchServers(); }, [fetchServers]);

  function toggleRow(id: string) {
    setExpandedIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) {
        next.delete(id);
      } else {
        next.add(id);
        if (!accounts[id]) {
          apiGet<{ accounts: CpanelAccount[] }>(`/v1/integrations/cpanel/${id}/accounts`)
            .then((d) => setAccounts((a) => ({ ...a, [id]: d.accounts ?? [] })))
            .catch(() => {});
        }
      }
      return next;
    });
  }

  function flashMsg(id: string, msg: string) {
    setSyncMsg((m) => ({ ...m, [id]: msg }));
    setTimeout(() => setSyncMsg((m) => { const n = { ...m }; delete n[id]; return n; }), 3000);
  }

  async function handleSync(id: string) {
    setSyncing((s) => ({ ...s, [id]: true }));
    try {
      await apiPost(`/v1/integrations/cpanel/${id}/sync`);
      flashMsg(id, "Sync triggered");
      fetchServers();
    } catch (e) {
      flashMsg(id, e instanceof Error ? e.message : "Error");
    } finally {
      setSyncing((s) => ({ ...s, [id]: false }));
    }
  }

  async function handleDelete(id: string) {
    if (!confirm("Delete this cPanel server?")) return;
    try {
      await apiDelete(`/v1/integrations/cpanel/${id}`);
      fetchServers();
    } catch (e) {
      alert(e instanceof Error ? e.message : "Error");
    }
  }

  async function handleAdd(data: CpanelFormData) {
    await apiPost("/v1/integrations/cpanel", data);
    setShowAdd(false);
    fetchServers();
  }

  async function handleEdit(id: string, data: CpanelFormData) {
    await apiPatch(`/v1/integrations/cpanel/${id}`, data);
    setEditId(null);
    fetchServers();
  }

  const editServer = servers.find((s) => s.id === editId);
  const editInitial: CpanelFormData | undefined = editServer
    ? {
        label: editServer.label,
        hostname: editServer.hostname,
        port: editServer.port,
        username: editServer.username,
        api_token: "",
        verify_ssl: editServer.verify_ssl,
        sync_interval_hours: editServer.sync_interval,
      }
    : undefined;

  return (
    <div>
      <div className="page-header" style={{ marginBottom: "1rem" }}>
        <button className="btn btn-sm" onClick={() => { setShowAdd(true); setEditId(null); }}>
          + Add cPanel Server
        </button>
      </div>

      {showAdd && (
        <div style={{ marginBottom: "1.5rem" }}>
          <CpanelForm onSave={handleAdd} onCancel={() => setShowAdd(false)} />
        </div>
      )}

      <LoadingError loading={loading} error={error} />

      {!loading && !error && (
        <>
          {servers.length === 0 ? (
            <p className="no-results">No cPanel servers configured.</p>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Label</th>
                    <th>Hostname</th>
                    <th>Username</th>
                    <th>Status</th>
                    <th>Accounts</th>
                    <th>Last Synced</th>
                    <th>Actions</th>
                    <th></th>
                  </tr>
                </thead>
                <tbody>
                  {servers.map((srv) => {
                    const expanded = expandedIds.has(srv.id);
                    const isEditing = editId === srv.id;
                    return (
                      <React.Fragment key={srv.id}>
                        <tr
                          style={{ cursor: "pointer" }}
                          onClick={() => !isEditing && toggleRow(srv.id)}
                        >
                          <td>{srv.label}</td>
                          <td>{srv.hostname}:{srv.port}</td>
                          <td>{srv.username}</td>
                          <td><SyncStatusBadge status={srv.sync_status} /></td>
                          <td>{srv.account_count}</td>
                          <td style={{ whiteSpace: "nowrap" }}>{fmtDate(srv.last_synced_at)}</td>
                          <td onClick={(e) => e.stopPropagation()}>
                            <div style={{ display: "flex", gap: "0.4rem", alignItems: "center" }}>
                              <button
                                className="btn btn-sm"
                                disabled={syncing[srv.id]}
                                onClick={() => handleSync(srv.id)}
                              >
                                {syncing[srv.id] ? "…" : "Sync Now"}
                              </button>
                              <button
                                className="btn btn-sm"
                                onClick={() => { setEditId(srv.id); setShowAdd(false); }}
                              >
                                Edit
                              </button>
                              <button
                                className="btn btn-sm btn-danger"
                                onClick={() => handleDelete(srv.id)}
                              >
                                Delete
                              </button>
                              {syncMsg[srv.id] && (
                                <span style={{ fontSize: "0.75rem", color: "#6b7280" }}>
                                  {syncMsg[srv.id]}
                                </span>
                              )}
                            </div>
                          </td>
                          <td style={{ fontSize: "0.75rem", color: "#9ca3af" }}>
                            {expanded ? "▲" : "▼"}
                          </td>
                        </tr>

                        {isEditing && (
                          <tr>
                            <td colSpan={8} style={{ padding: "1rem", background: "var(--color-surface, #f9fafb)" }}>
                              <CpanelForm
                                initial={editInitial}
                                onSave={(data) => handleEdit(srv.id, data)}
                                onCancel={() => setEditId(null)}
                              />
                            </td>
                          </tr>
                        )}

                        {expanded && !isEditing && (
                          <tr>
                            <td colSpan={8} style={{ padding: "1rem", background: "var(--color-surface, #f9fafb)" }}>
                              <div style={{ fontWeight: 600, marginBottom: "0.5rem", fontSize: "0.875rem" }}>
                                Accounts — {srv.label}
                              </div>
                              {srv.sync_error && (
                                <div className="inline-error" style={{ marginBottom: "0.5rem" }}>
                                  Last error: {srv.sync_error}
                                </div>
                              )}
                              {!accounts[srv.id] ? (
                                <span style={{ color: "#9ca3af", fontSize: "0.875rem" }}>Loading…</span>
                              ) : accounts[srv.id].length === 0 ? (
                                <span style={{ color: "#9ca3af", fontSize: "0.875rem" }}>No accounts found.</span>
                              ) : (
                                <div className="table-wrap">
                                  <table>
                                    <thead>
                                      <tr>
                                        <th>Username</th>
                                        <th>Primary Domain</th>
                                        <th>Owner Email</th>
                                        <th>Plan</th>
                                        <th>Suspended</th>
                                        <th>Disk (MB)</th>
                                        <th>Synced At</th>
                                      </tr>
                                    </thead>
                                    <tbody>
                                      {accounts[srv.id].map((acc) => (
                                        <tr key={acc.id}>
                                          <td>{acc.username}</td>
                                          <td>{acc.primary_domain}</td>
                                          <td>{acc.owner_email || "—"}</td>
                                          <td>{acc.plan || "—"}</td>
                                          <td>
                                            <span className={`badge ${acc.suspended ? "badge-error" : "badge-ok"}`}>
                                              {acc.suspended ? "Yes" : "No"}
                                            </span>
                                          </td>
                                          <td>{acc.disk_used_mb}</td>
                                          <td style={{ whiteSpace: "nowrap" }}>{fmtDate(acc.synced_at)}</td>
                                        </tr>
                                      ))}
                                    </tbody>
                                  </table>
                                </div>
                              )}
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
    </div>
  );
}

// ─── WHMCS tab ────────────────────────────────────────────────────────────────

function WhmcsTab() {
  const [connections, setConnections] = useState<WhmcsConnection[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [showAdd, setShowAdd] = useState(false);
  const [editId, setEditId] = useState<string | null>(null);
  const [expandedIds, setExpandedIds] = useState<Set<string>>(new Set());
  const [logs, setLogs] = useState<Record<string, WhmcsPushLogEntry[]>>({});
  const [pushing, setPushing] = useState<Record<string, boolean>>({});
  const [pushMsg, setPushMsg] = useState<Record<string, string>>({});

  const fetchConnections = useCallback(() => {
    setLoading(true);
    setError(null);
    apiGet<{ connections: WhmcsConnection[] }>("/v1/integrations/whmcs")
      .then((d) => setConnections(d.connections ?? []))
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => { fetchConnections(); }, [fetchConnections]);

  function toggleRow(id: string) {
    setExpandedIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) {
        next.delete(id);
      } else {
        next.add(id);
        if (!logs[id]) {
          apiGet<{ log: WhmcsPushLogEntry[] }>(`/v1/integrations/whmcs/${id}/log`)
            .then((d) => setLogs((l) => ({ ...l, [id]: d.log ?? [] })))
            .catch(() => {});
        }
      }
      return next;
    });
  }

  function flashMsg(id: string, msg: string) {
    setPushMsg((m) => ({ ...m, [id]: msg }));
    setTimeout(() => setPushMsg((m) => { const n = { ...m }; delete n[id]; return n; }), 3000);
  }

  async function handlePush(id: string) {
    setPushing((s) => ({ ...s, [id]: true }));
    try {
      await apiPost(`/v1/integrations/whmcs/${id}/push`);
      flashMsg(id, "Push triggered");
      fetchConnections();
    } catch (e) {
      flashMsg(id, e instanceof Error ? e.message : "Error");
    } finally {
      setPushing((s) => ({ ...s, [id]: false }));
    }
  }

  async function handleDelete(id: string) {
    if (!confirm("Delete this WHMCS connection?")) return;
    try {
      await apiDelete(`/v1/integrations/whmcs/${id}`);
      fetchConnections();
    } catch (e) {
      alert(e instanceof Error ? e.message : "Error");
    }
  }

  async function handleAdd(data: WhmcsFormData) {
    await apiPost("/v1/integrations/whmcs", data);
    setShowAdd(false);
    fetchConnections();
  }

  async function handleEdit(id: string, data: WhmcsFormData) {
    await apiPatch(`/v1/integrations/whmcs/${id}`, data);
    setEditId(null);
    fetchConnections();
  }

  const editConn = connections.find((c) => c.id === editId);
  const editInitial: WhmcsFormData | undefined = editConn
    ? {
        label: editConn.label,
        api_url: editConn.api_url,
        api_identifier: editConn.api_identifier,
        api_secret: "",
        push_frequency: editConn.push_frequency,
      }
    : undefined;

  return (
    <div>
      <div className="page-header" style={{ marginBottom: "1rem" }}>
        <button className="btn btn-sm" onClick={() => { setShowAdd(true); setEditId(null); }}>
          + Add WHMCS Connection
        </button>
      </div>

      {showAdd && (
        <div style={{ marginBottom: "1.5rem" }}>
          <WhmcsForm onSave={handleAdd} onCancel={() => setShowAdd(false)} />
        </div>
      )}

      <LoadingError loading={loading} error={error} />

      {!loading && !error && (
        <>
          {connections.length === 0 ? (
            <p className="no-results">No WHMCS connections configured.</p>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Label</th>
                    <th>API URL</th>
                    <th>Frequency</th>
                    <th>Enabled</th>
                    <th>Last Pushed</th>
                    <th>Actions</th>
                    <th></th>
                  </tr>
                </thead>
                <tbody>
                  {connections.map((conn) => {
                    const expanded = expandedIds.has(conn.id);
                    const isEditing = editId === conn.id;
                    return (
                      <React.Fragment key={conn.id}>
                        <tr
                          style={{ cursor: "pointer" }}
                          onClick={() => !isEditing && toggleRow(conn.id)}
                        >
                          <td>{conn.label}</td>
                          <td title={conn.api_url} style={{ maxWidth: "200px", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                            {truncate(conn.api_url)}
                          </td>
                          <td>{conn.push_frequency}</td>
                          <td>
                            <span className={`badge ${conn.enabled ? "badge-ok" : "badge-pending"}`}>
                              {conn.enabled ? "Yes" : "No"}
                            </span>
                          </td>
                          <td style={{ whiteSpace: "nowrap" }}>{fmtDate(conn.last_pushed_at)}</td>
                          <td onClick={(e) => e.stopPropagation()}>
                            <div style={{ display: "flex", gap: "0.4rem", alignItems: "center" }}>
                              <button
                                className="btn btn-sm"
                                disabled={pushing[conn.id]}
                                onClick={() => handlePush(conn.id)}
                              >
                                {pushing[conn.id] ? "…" : "Push Now"}
                              </button>
                              <button
                                className="btn btn-sm"
                                onClick={() => { setEditId(conn.id); setShowAdd(false); }}
                              >
                                Edit
                              </button>
                              <button
                                className="btn btn-sm btn-danger"
                                onClick={() => handleDelete(conn.id)}
                              >
                                Delete
                              </button>
                              {pushMsg[conn.id] && (
                                <span style={{ fontSize: "0.75rem", color: "#6b7280" }}>
                                  {pushMsg[conn.id]}
                                </span>
                              )}
                            </div>
                          </td>
                          <td style={{ fontSize: "0.75rem", color: "#9ca3af" }}>
                            {expanded ? "▲" : "▼"}
                          </td>
                        </tr>

                        {isEditing && (
                          <tr>
                            <td colSpan={7} style={{ padding: "1rem", background: "var(--color-surface, #f9fafb)" }}>
                              <WhmcsForm
                                initial={editInitial}
                                onSave={(data) => handleEdit(conn.id, data)}
                                onCancel={() => setEditId(null)}
                              />
                            </td>
                          </tr>
                        )}

                        {expanded && !isEditing && (
                          <tr>
                            <td colSpan={7} style={{ padding: "1rem", background: "var(--color-surface, #f9fafb)" }}>
                              <div style={{ fontWeight: 600, marginBottom: "0.5rem", fontSize: "0.875rem" }}>
                                Push Log — {conn.label}
                              </div>
                              {!logs[conn.id] ? (
                                <span style={{ color: "#9ca3af", fontSize: "0.875rem" }}>Loading…</span>
                              ) : logs[conn.id].length === 0 ? (
                                <span style={{ color: "#9ca3af", fontSize: "0.875rem" }}>No push history.</span>
                              ) : (
                                <div className="table-wrap">
                                  <table>
                                    <thead>
                                      <tr>
                                        <th>Pushed At</th>
                                        <th>Accounts</th>
                                        <th>Status</th>
                                        <th>Period</th>
                                        <th>Detail</th>
                                      </tr>
                                    </thead>
                                    <tbody>
                                      {logs[conn.id].map((entry) => (
                                        <tr key={entry.id}>
                                          <td style={{ whiteSpace: "nowrap" }}>{fmtDate(entry.pushed_at)}</td>
                                          <td>{entry.accounts_pushed}</td>
                                          <td>
                                            <span className={`badge ${entry.status === "ok" ? "badge-ok" : entry.status === "partial" ? "badge-syncing" : "badge-error"}`}>
                                              {entry.status}
                                            </span>
                                          </td>
                                          <td style={{ whiteSpace: "nowrap" }}>
                                            {fmtDate(entry.period_start)} – {fmtDate(entry.period_end)}
                                          </td>
                                          <td style={{ color: "#6b7280", fontSize: "0.8125rem" }}>
                                            {entry.error_detail || "—"}
                                          </td>
                                        </tr>
                                      ))}
                                    </tbody>
                                  </table>
                                </div>
                              )}
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
    </div>
  );
}

// ─── Main page ────────────────────────────────────────────────────────────────

type Tab = "cpanel" | "whmcs";

export default function IntegrationsPage() {
  const [activeTab, setActiveTab] = useState<Tab>("cpanel");

  return (
    <>
      <h1 className="page-title">Integrations</h1>

      <div className="tab-bar" style={{ display: "flex", gap: "0", marginBottom: "1.5rem", borderBottom: "1px solid var(--color-border, #e5e7eb)" }}>
        {(["cpanel", "whmcs"] as Tab[]).map((tab) => (
          <button
            key={tab}
            type="button"
            onClick={() => setActiveTab(tab)}
            style={{
              padding: "0.5rem 1.25rem",
              fontWeight: activeTab === tab ? 600 : 400,
              background: "none",
              border: "none",
              borderBottom: activeTab === tab ? "2px solid var(--color-primary, #2563eb)" : "2px solid transparent",
              cursor: "pointer",
              color: activeTab === tab ? "var(--color-primary, #2563eb)" : "inherit",
              fontSize: "0.9375rem",
            }}
          >
            {tab === "cpanel" ? "cPanel Servers" : "WHMCS Connections"}
          </button>
        ))}
      </div>

      {activeTab === "cpanel" ? <CpanelTab /> : <WhmcsTab />}
    </>
  );
}

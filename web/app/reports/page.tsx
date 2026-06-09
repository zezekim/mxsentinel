"use client";

import React, { useState, useEffect, useCallback } from "react";
import { apiGet, apiPost, apiPut, apiDelete } from "@/lib/api";
import LoadingError from "@/components/LoadingError";

interface ReportSchedule {
  id: string;
  name: string;
  frequency: "daily" | "weekly" | "monthly";
  recipients: string[];
  include_dns: boolean;
  include_dmarc: boolean;
  include_incidents: boolean;
  include_reputation: boolean;
  enabled: boolean;
  last_sent_at: string;
  next_run_at: string;
}

interface ReportSchedulesResponse {
  schedules: ReportSchedule[];
}

function fmt(ts: string): string {
  if (!ts) return "—";
  const d = new Date(ts);
  return isNaN(d.getTime()) ? ts : d.toLocaleString();
}

function FrequencyBadge({ frequency }: { frequency: string }) {
  const colorMap: Record<string, string> = {
    daily: "badge-info",
    weekly: "badge-healthy",
    monthly: "badge-purple",
  };
  return (
    <span className={`badge ${colorMap[frequency] ?? "badge-unknown"}`}>
      {frequency}
    </span>
  );
}

function ContentIcons({
  dns,
  dmarc,
  incidents,
  reputation,
}: {
  dns: boolean;
  dmarc: boolean;
  incidents: boolean;
  reputation: boolean;
}) {
  const items = [
    { label: "DNS", active: dns },
    { label: "DMARC", active: dmarc },
    { label: "Incidents", active: incidents },
    { label: "Reputation", active: reputation },
  ];
  return (
    <div style={{ display: "flex", gap: 4, flexWrap: "wrap" }}>
      {items.map(({ label, active }) =>
        active ? (
          <span key={label} className="badge badge-unknown" style={{ fontSize: 11 }}>
            {label}
          </span>
        ) : null
      )}
    </div>
  );
}

export default function ReportsPage() {
  const [schedules, setSchedules] = useState<ReportSchedule[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // Expanded rows
  const [expandedRows, setExpandedRows] = useState<Set<string>>(new Set());

  // Create form visibility
  const [showForm, setShowForm] = useState(false);

  // Create form fields
  const [formName, setFormName] = useState("");
  const [formFrequency, setFormFrequency] = useState<"daily" | "weekly" | "monthly">("weekly");
  const [formRecipients, setFormRecipients] = useState("");
  const [formIncludeDns, setFormIncludeDns] = useState(true);
  const [formIncludeDmarc, setFormIncludeDmarc] = useState(true);
  const [formIncludeIncidents, setFormIncludeIncidents] = useState(true);
  const [formIncludeReputation, setFormIncludeReputation] = useState(true);
  const [creating, setCreating] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  const refresh = useCallback(() => {
    setLoading(true);
    setError(null);
    apiGet<ReportSchedulesResponse>("/v1/reports")
      .then((d) => setSchedules(d.schedules ?? []))
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh]);

  function toggleRow(id: string) {
    setExpandedRows((prev) => {
      const next = new Set(prev);
      if (next.has(id)) {
        next.delete(id);
      } else {
        next.add(id);
      }
      return next;
    });
  }

  function resetForm() {
    setFormName("");
    setFormFrequency("weekly");
    setFormRecipients("");
    setFormIncludeDns(true);
    setFormIncludeDmarc(true);
    setFormIncludeIncidents(true);
    setFormIncludeReputation(true);
    setFormError(null);
  }

  async function handleCreate(e: React.FormEvent) {
    e.preventDefault();
    setFormError(null);
    const recipients = formRecipients
      .split("\n")
      .map((r) => r.trim())
      .filter(Boolean);
    if (recipients.length === 0) {
      setFormError("At least one recipient email is required.");
      return;
    }
    setCreating(true);
    try {
      await apiPost("/v1/reports", {
        name: formName.trim(),
        frequency: formFrequency,
        recipients,
        include_dns: formIncludeDns,
        include_dmarc: formIncludeDmarc,
        include_incidents: formIncludeIncidents,
        include_reputation: formIncludeReputation,
      });
      resetForm();
      setShowForm(false);
      refresh();
    } catch (err: unknown) {
      setFormError(err instanceof Error ? err.message : "Failed to create schedule");
    } finally {
      setCreating(false);
    }
  }

  async function handleToggleEnabled(s: ReportSchedule) {
    try {
      await apiPut(`/v1/reports/${s.id}`, {
        name: s.name,
        frequency: s.frequency,
        recipients: s.recipients,
        include_dns: s.include_dns,
        include_dmarc: s.include_dmarc,
        include_incidents: s.include_incidents,
        include_reputation: s.include_reputation,
        enabled: !s.enabled,
      });
      refresh();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  async function handleSendNow(s: ReportSchedule) {
    if (
      !window.confirm(
        `Send "${s.name}" report to ${s.recipients.length} recipient(s) now?`
      )
    ) {
      return;
    }
    try {
      await apiPost(`/v1/reports/${s.id}/send-now`);
      refresh();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  async function handleDelete(s: ReportSchedule) {
    if (
      !window.confirm(
        `Delete report schedule "${s.name}"? This cannot be undone.`
      )
    ) {
      return;
    }
    try {
      await apiDelete(`/v1/reports/${s.id}`);
      refresh();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  return (
    <>
      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", marginBottom: 16 }}>
        <h1 style={{ margin: 0 }}>Scheduled Reports</h1>
        <button
          type="button"
          onClick={() => {
            resetForm();
            setShowForm((v) => !v);
          }}
        >
          {showForm ? "Cancel" : "New Report Schedule"}
        </button>
      </div>

      <p className="section-desc">
        Automatically email DNS health, DMARC summaries, incidents, and reputation
        snapshots to your team on a recurring schedule.
      </p>

      {showForm && (
        <form className="filters" onSubmit={handleCreate} style={{ flexDirection: "column", alignItems: "flex-start", gap: 12, marginBottom: 24 }}>
          <h3 style={{ margin: 0 }}>New Schedule</h3>

          <div style={{ display: "flex", gap: 12, flexWrap: "wrap", width: "100%" }}>
            <input
              type="text"
              placeholder="Report name (e.g. Weekly DNS Digest)"
              value={formName}
              onChange={(e) => setFormName(e.target.value)}
              style={{ minWidth: 260, flex: 1 }}
              required
            />
            <select
              value={formFrequency}
              onChange={(e) => setFormFrequency(e.target.value as "daily" | "weekly" | "monthly")}
              style={{ minWidth: 140 }}
            >
              <option value="daily">Daily</option>
              <option value="weekly">Weekly</option>
              <option value="monthly">Monthly</option>
            </select>
          </div>

          <textarea
            placeholder={"Recipients (one email per line)\nops@example.com\nteam@example.com"}
            value={formRecipients}
            onChange={(e) => setFormRecipients(e.target.value)}
            rows={3}
            style={{ width: "100%", maxWidth: 480, fontFamily: "monospace", fontSize: 13 }}
            required
          />

          <div style={{ display: "flex", gap: 20, flexWrap: "wrap" }}>
            <label style={{ display: "flex", alignItems: "center", gap: 6, cursor: "pointer" }}>
              <input
                type="checkbox"
                checked={formIncludeDns}
                onChange={(e) => setFormIncludeDns(e.target.checked)}
              />
              Include DNS Health
            </label>
            <label style={{ display: "flex", alignItems: "center", gap: 6, cursor: "pointer" }}>
              <input
                type="checkbox"
                checked={formIncludeDmarc}
                onChange={(e) => setFormIncludeDmarc(e.target.checked)}
              />
              Include DMARC Summary
            </label>
            <label style={{ display: "flex", alignItems: "center", gap: 6, cursor: "pointer" }}>
              <input
                type="checkbox"
                checked={formIncludeIncidents}
                onChange={(e) => setFormIncludeIncidents(e.target.checked)}
              />
              Include Incidents
            </label>
            <label style={{ display: "flex", alignItems: "center", gap: 6, cursor: "pointer" }}>
              <input
                type="checkbox"
                checked={formIncludeReputation}
                onChange={(e) => setFormIncludeReputation(e.target.checked)}
              />
              Include Reputation
            </label>
          </div>

          {formError && <p className="state-msg error" style={{ margin: 0 }}>{formError}</p>}

          <button type="submit" disabled={creating}>
            {creating ? "Creating…" : "Create Schedule"}
          </button>
        </form>
      )}

      <LoadingError loading={loading} error={error} />

      {!loading && !error && (
        <>
          {schedules.length === 0 ? (
            <p className="no-results">No report schedules configured. Click "New Report Schedule" to add one.</p>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th style={{ width: 28 }} />
                    <th>Name</th>
                    <th>Frequency</th>
                    <th>Recipients</th>
                    <th>Content</th>
                    <th>Enabled</th>
                    <th>Last Sent</th>
                    <th>Next Run</th>
                    <th style={{ textAlign: "right" }}>Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {schedules.map((s) => {
                    const expanded = expandedRows.has(s.id);
                    return (
                      <React.Fragment key={s.id}>
                        <tr>
                          <td>
                            <button
                              type="button"
                              className="btn-sm"
                              onClick={() => toggleRow(s.id)}
                              title={expanded ? "Collapse" : "Expand"}
                              style={{ padding: "0 6px", minWidth: 24 }}
                            >
                              {expanded ? "▲" : "▼"}
                            </button>
                          </td>
                          <td style={{ fontWeight: 500 }}>{s.name}</td>
                          <td>
                            <FrequencyBadge frequency={s.frequency} />
                          </td>
                          <td>
                            <span className="badge badge-unknown">{s.recipients.length}</span>
                          </td>
                          <td>
                            <ContentIcons
                              dns={s.include_dns}
                              dmarc={s.include_dmarc}
                              incidents={s.include_incidents}
                              reputation={s.include_reputation}
                            />
                          </td>
                          <td>
                            <span className={`badge ${s.enabled ? "badge-healthy" : "badge-unknown"}`}>
                              {s.enabled ? "enabled" : "disabled"}
                            </span>
                          </td>
                          <td style={{ whiteSpace: "nowrap" }}>{fmt(s.last_sent_at)}</td>
                          <td style={{ whiteSpace: "nowrap" }}>{fmt(s.next_run_at)}</td>
                          <td>
                            <div className="row-actions">
                              <button
                                type="button"
                                className="btn-sm"
                                onClick={() => handleToggleEnabled(s)}
                              >
                                {s.enabled ? "Disable" : "Enable"}
                              </button>
                              <button
                                type="button"
                                className="btn-sm"
                                onClick={() => handleSendNow(s)}
                              >
                                Send Now
                              </button>
                              <button
                                type="button"
                                className="btn-sm btn-sm-danger"
                                onClick={() => handleDelete(s)}
                              >
                                Delete
                              </button>
                            </div>
                          </td>
                        </tr>

                        {expanded && (
                          <tr>
                            <td />
                            <td colSpan={8}>
                              <div style={{ padding: "10px 0 10px 4px", display: "flex", gap: 32, flexWrap: "wrap" }}>
                                <div>
                                  <div style={{ fontWeight: 600, marginBottom: 6, fontSize: 13 }}>
                                    Recipients ({s.recipients.length})
                                  </div>
                                  <ul style={{ margin: 0, paddingLeft: 18, fontSize: 13 }}>
                                    {s.recipients.map((r) => (
                                      <li key={r}>{r}</li>
                                    ))}
                                  </ul>
                                </div>
                                <div>
                                  <div style={{ fontWeight: 600, marginBottom: 6, fontSize: 13 }}>
                                    Content Sections
                                  </div>
                                  <ul style={{ margin: 0, paddingLeft: 18, fontSize: 13 }}>
                                    <li style={{ color: s.include_dns ? "inherit" : "var(--text-muted, #888)" }}>
                                      {s.include_dns ? "✓" : "✗"} DNS Health
                                    </li>
                                    <li style={{ color: s.include_dmarc ? "inherit" : "var(--text-muted, #888)" }}>
                                      {s.include_dmarc ? "✓" : "✗"} DMARC Summary
                                    </li>
                                    <li style={{ color: s.include_incidents ? "inherit" : "var(--text-muted, #888)" }}>
                                      {s.include_incidents ? "✓" : "✗"} Incidents
                                    </li>
                                    <li style={{ color: s.include_reputation ? "inherit" : "var(--text-muted, #888)" }}>
                                      {s.include_reputation ? "✓" : "✗"} Reputation
                                    </li>
                                  </ul>
                                </div>
                              </div>
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

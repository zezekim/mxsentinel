"use client";

import React, { useEffect, useState, useCallback } from "react";
import { getToken } from "@/lib/api";
import LoadingError from "@/components/LoadingError";
import { API_BASE } from "@/lib/api-base";


// ─── Types ────────────────────────────────────────────────────────────────────

type Stage = "ramping" | "established" | "paused";

interface WarmupPlan {
  id: string;
  name: string;
  ip_pool: string;
  target_daily_volume: number;
  current_day: number;
  stage: Stage;
  started_at: string | null;
  completed_at: string | null;
}

interface DayStat {
  day_number: number;
  stat_date: string;
  sent: number;
  delivered: number;
  bounced: number;
  acceptance_rate: number | null;
}

interface PlanDetail {
  plan: WarmupPlan;
  day_stats: DayStat[];
  recommended_today: number;
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

function fmtDate(ts: string | null): string {
  if (!ts) return "—";
  try {
    return new Date(ts).toLocaleDateString();
  } catch {
    return ts;
  }
}

function authHeaders(): HeadersInit {
  const token = getToken();
  return token ? { Authorization: `Bearer ${token}` } : {};
}

async function apiFetch<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await fetch(`${API_BASE}${path}`, {
    ...options,
    headers: {
      "Content-Type": "application/json",
      ...authHeaders(),
      ...(options?.headers ?? {}),
    },
  });
  if (!res.ok) {
    let msg = `HTTP ${res.status}`;
    try {
      const body = await res.json();
      if (body?.error?.message) msg = body.error.message;
    } catch {
      // ignore
    }
    throw new Error(msg);
  }
  return res.json() as Promise<T>;
}

/**
 * Estimate total warm-up days needed.
 * Starts at 50/day, doubles each day until target is reached.
 * Returns the number of days to reach target_daily_volume.
 */
function estimateTotalDays(targetVolume: number): number {
  let vol = 50;
  let days = 1;
  while (vol < targetVolume && days < 365) {
    vol = Math.min(vol * 2, targetVolume);
    days++;
  }
  return days;
}

// ─── Stage badge ──────────────────────────────────────────────────────────────

function StageBadge({ stage }: { stage: Stage }) {
  const styles: Record<Stage, React.CSSProperties> = {
    ramping: { background: "#dbeafe", color: "#1d4ed8" },
    established: { background: "#dcfce7", color: "#15803d" },
    paused: { background: "#f3f4f6", color: "#6b7280" },
  };
  return (
    <span
      style={{
        padding: "2px 10px",
        borderRadius: 9999,
        fontSize: "0.75rem",
        fontWeight: 600,
        ...styles[stage],
      }}
    >
      {stage}
    </span>
  );
}

// ─── Acceptance rate status ───────────────────────────────────────────────────

function AcceptanceBadge({ rate }: { rate: number | null }) {
  if (rate === null || rate === undefined) {
    return <span style={{ color: "#9ca3af" }}>—</span>;
  }
  if (rate >= 0.95) {
    return (
      <span style={{ color: "#15803d", fontWeight: 600 }}>Good</span>
    );
  }
  if (rate >= 0.8) {
    return (
      <span style={{ color: "#b45309", fontWeight: 600 }}>Caution</span>
    );
  }
  return (
    <span style={{ color: "#dc2626", fontWeight: 600 }}>Slow Down</span>
  );
}

// ─── Progress bar ─────────────────────────────────────────────────────────────

function ProgressBar({
  current,
  total,
}: {
  current: number;
  total: number;
}) {
  const pct = total > 0 ? Math.min((current / total) * 100, 100) : 0;
  return (
    <div style={{ display: "flex", alignItems: "center", gap: 10 }}>
      <div
        style={{
          flex: 1,
          height: 8,
          background: "#e5e7eb",
          borderRadius: 4,
          overflow: "hidden",
        }}
      >
        <div
          style={{
            width: `${pct.toFixed(1)}%`,
            height: "100%",
            background: "#3b82f6",
            borderRadius: 4,
            transition: "width 0.3s ease",
          }}
        />
      </div>
      <span style={{ fontSize: "0.75rem", color: "#6b7280", whiteSpace: "nowrap" }}>
        Day {current} / {total}
      </span>
    </div>
  );
}

// ─── Plan detail (expanded row) ───────────────────────────────────────────────

function PlanDetail({
  planId,
  targetVolume,
  currentDay,
}: {
  planId: string;
  targetVolume: number;
  currentDay: number;
}) {
  const [detail, setDetail] = useState<PlanDetail | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    setLoading(true);
    setError(null);
    apiFetch<PlanDetail>(`/v1/warmup/${planId}`)
      .then(setDetail)
      .catch((e: unknown) =>
        setError(e instanceof Error ? e.message : String(e))
      )
      .finally(() => setLoading(false));
  }, [planId]);

  const totalDays = estimateTotalDays(targetVolume);

  return (
    <div
      style={{
        padding: "16px 24px",
        background: "var(--surface-2, #f9fafb)",
        borderTop: "1px solid var(--border, #e5e7eb)",
      }}
    >
      <LoadingError loading={loading} error={error} />

      {detail && !loading && (
        <>
          <div
            style={{
              display: "flex",
              alignItems: "center",
              gap: 24,
              marginBottom: 16,
              flexWrap: "wrap",
            }}
          >
            <div>
              <span style={{ fontWeight: 700, fontSize: "1.1rem" }}>
                Recommended today:
              </span>{" "}
              <span
                style={{
                  fontSize: "1.25rem",
                  fontWeight: 800,
                  color: "#2563eb",
                }}
              >
                {detail.recommended_today.toLocaleString()} emails
              </span>
            </div>
            <div style={{ flex: 1, minWidth: 200 }}>
              <ProgressBar current={currentDay} total={totalDays} />
            </div>
          </div>

          {detail.day_stats && detail.day_stats.length > 0 ? (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Day #</th>
                    <th>Date</th>
                    <th>Sent</th>
                    <th>Delivered</th>
                    <th>Bounced</th>
                    <th>Acceptance Rate</th>
                    <th>Status</th>
                  </tr>
                </thead>
                <tbody>
                  {detail.day_stats.map((ds) => (
                    <tr key={ds.day_number}>
                      <td style={{ textAlign: "center" }}>{ds.day_number}</td>
                      <td>{fmtDate(ds.stat_date)}</td>
                      <td style={{ textAlign: "right" }}>
                        {ds.sent.toLocaleString()}
                      </td>
                      <td style={{ textAlign: "right" }}>
                        {ds.delivered.toLocaleString()}
                      </td>
                      <td style={{ textAlign: "right" }}>
                        {ds.bounced.toLocaleString()}
                      </td>
                      <td style={{ textAlign: "right" }}>
                        {ds.acceptance_rate !== null &&
                        ds.acceptance_rate !== undefined
                          ? `${(ds.acceptance_rate * 100).toFixed(1)}%`
                          : "—"}
                      </td>
                      <td>
                        <AcceptanceBadge rate={ds.acceptance_rate} />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <p className="no-results" style={{ margin: 0 }}>
              No day stats yet.
            </p>
          )}
        </>
      )}
    </div>
  );
}

// ─── New plan form ────────────────────────────────────────────────────────────

interface NewPlanFormProps {
  onCreated: () => void;
  onCancel: () => void;
}

function NewPlanForm({ onCreated, onCancel }: NewPlanFormProps) {
  const [name, setName] = useState("");
  const [ipPool, setIpPool] = useState("");
  const [targetVolume, setTargetVolume] = useState(10000);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim() || !ipPool.trim()) return;
    setSubmitting(true);
    setError(null);
    apiFetch<{ plan: WarmupPlan }>("/v1/warmup", {
      method: "POST",
      body: JSON.stringify({
        name: name.trim(),
        ip_pool: ipPool.trim(),
        target_daily_volume: targetVolume,
      }),
    })
      .then(() => onCreated())
      .catch((e: unknown) => {
        setError(e instanceof Error ? e.message : String(e));
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
        maxWidth: 480,
      }}
    >
      <h3 style={{ margin: 0, fontSize: "1rem", fontWeight: 700 }}>
        New Warm-up Plan
      </h3>

      {error && (
        <p className="state-msg error" style={{ margin: 0 }}>
          Error: {error}
        </p>
      )}

      <label style={{ display: "flex", flexDirection: "column", gap: 4 }}>
        <span style={{ fontSize: "0.8rem", fontWeight: 600, color: "#6b7280" }}>
          Plan Name
        </span>
        <input
          type="text"
          required
          placeholder="e.g. Transactional Jan 2026"
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
      </label>

      <label style={{ display: "flex", flexDirection: "column", gap: 4 }}>
        <span style={{ fontSize: "0.8rem", fontWeight: 600, color: "#6b7280" }}>
          IP Pool
        </span>
        <input
          type="text"
          required
          placeholder="e.g. pool-A"
          value={ipPool}
          onChange={(e) => setIpPool(e.target.value)}
        />
      </label>

      <label style={{ display: "flex", flexDirection: "column", gap: 4 }}>
        <span style={{ fontSize: "0.8rem", fontWeight: 600, color: "#6b7280" }}>
          Target Daily Volume
        </span>
        <input
          type="number"
          required
          min={1}
          value={targetVolume}
          onChange={(e) => setTargetVolume(Number(e.target.value))}
        />
      </label>

      <div style={{ display: "flex", gap: 10 }}>
        <button type="submit" className="btn btn-primary btn-sm" disabled={submitting}>
          {submitting ? "Creating…" : "Create Plan"}
        </button>
        <button type="button" className="btn btn-sm" onClick={onCancel} disabled={submitting}>
          Cancel
        </button>
      </div>
    </form>
  );
}

// ─── Main page ────────────────────────────────────────────────────────────────

export default function WarmupPage() {
  const [plans, setPlans] = useState<WarmupPlan[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [expandedIds, setExpandedIds] = useState<Set<string>>(new Set());
  const [showNewForm, setShowNewForm] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);

  const fetchPlans = useCallback(() => {
    setLoading(true);
    setError(null);
    apiFetch<{ plans: WarmupPlan[] }>("/v1/warmup")
      .then((d) => setPlans(d.plans ?? []))
      .catch((e: unknown) =>
        setError(e instanceof Error ? e.message : String(e))
      )
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    fetchPlans();
  }, [fetchPlans]);

  function toggleRow(id: string) {
    setExpandedIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) {
        next.delete(id);
      } else {
        next.add(id);
      }
      return next;
    });
  }

  function handleStageChange(
    e: React.MouseEvent,
    plan: WarmupPlan,
    newStage: Stage
  ) {
    e.stopPropagation();
    setActionError(null);
    apiFetch(`/v1/warmup/${plan.id}`, {
      method: "PATCH",
      body: JSON.stringify({ stage: newStage }),
    })
      .then(() => fetchPlans())
      .catch((err: unknown) =>
        setActionError(err instanceof Error ? err.message : String(err))
      );
  }

  function handleDelete(e: React.MouseEvent, id: string) {
    e.stopPropagation();
    if (!confirm("Delete this warm-up plan? This cannot be undone.")) return;
    setActionError(null);
    apiFetch(`/v1/warmup/${id}`, { method: "DELETE" })
      .then(() => {
        setExpandedIds((prev) => {
          const next = new Set(prev);
          next.delete(id);
          return next;
        });
        fetchPlans();
      })
      .catch((err: unknown) =>
        setActionError(err instanceof Error ? err.message : String(err))
      );
  }

  function handleCreated() {
    setShowNewForm(false);
    fetchPlans();
  }

  return (
    <>
      <div
        style={{
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          marginBottom: 20,
          flexWrap: "wrap",
          gap: 12,
        }}
      >
        <h1 style={{ margin: 0 }}>IP Warm-up Advisor</h1>
        {!showNewForm && (
          <button onClick={() => setShowNewForm(true)}>
            + New Warm-up Plan
          </button>
        )}
      </div>

      {showNewForm && (
        <NewPlanForm
          onCreated={handleCreated}
          onCancel={() => setShowNewForm(false)}
        />
      )}

      {actionError && (
        <p className="state-msg error">Error: {actionError}</p>
      )}

      <LoadingError loading={loading} error={error} />

      {!loading && !error && (
        <>
          {plans.length === 0 ? (
            <p className="no-results">No warm-up plans found.</p>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Name</th>
                    <th>IP Pool</th>
                    <th>Stage</th>
                    <th>Current Day</th>
                    <th>Target Vol/Day</th>
                    <th>Started At</th>
                    <th>Actions</th>
                    <th></th>
                  </tr>
                </thead>
                <tbody>
                  {plans.map((plan) => {
                    const expanded = expandedIds.has(plan.id);
                    return (
                      <React.Fragment key={plan.id}>
                        <tr
                          className="incident-row-main"
                          onClick={() => toggleRow(plan.id)}
                          aria-expanded={expanded}
                          style={{ cursor: "pointer" }}
                        >
                          <td style={{ fontWeight: 600 }}>{plan.name}</td>
                          <td>
                            <code style={{ fontSize: "0.8rem" }}>
                              {plan.ip_pool}
                            </code>
                          </td>
                          <td>
                            <StageBadge stage={plan.stage} />
                          </td>
                          <td style={{ textAlign: "center" }}>
                            {plan.current_day}
                          </td>
                          <td style={{ textAlign: "right" }}>
                            {plan.target_daily_volume.toLocaleString()}
                          </td>
                          <td style={{ whiteSpace: "nowrap" }}>
                            {fmtDate(plan.started_at)}
                          </td>
                          <td
                            onClick={(e) => e.stopPropagation()}
                            style={{ whiteSpace: "nowrap" }}
                          >
                            {plan.stage === "paused" ? (
                              <button
                                style={{ marginRight: 6 }}
                                onClick={(e) =>
                                  handleStageChange(e, plan, "ramping")
                                }
                              >
                                Resume
                              </button>
                            ) : (
                              <button
                                style={{ marginRight: 6 }}
                                onClick={(e) =>
                                  handleStageChange(e, plan, "paused")
                                }
                              >
                                Pause
                              </button>
                            )}
                            <button
                              style={{
                                background: "#fee2e2",
                                color: "#dc2626",
                                border: "1px solid #fca5a5",
                              }}
                              onClick={(e) => handleDelete(e, plan.id)}
                            >
                              Delete
                            </button>
                          </td>
                          <td
                            style={{ fontSize: "0.75rem", color: "#9ca3af" }}
                          >
                            {expanded ? "▲" : "▼"}
                          </td>
                        </tr>
                        {expanded && (
                          <tr className="incident-detail-row">
                            <td colSpan={8} style={{ padding: 0 }}>
                              <PlanDetail
                                planId={plan.id}
                                targetVolume={plan.target_daily_volume}
                                currentDay={plan.current_day}
                              />
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

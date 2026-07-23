"use client";

import React, { useState, useEffect, useCallback } from "react";
import LoadingError from "@/components/LoadingError";
import {
  AlertChannel,
  ChannelType,
  listAlertChannels,
  createAlertChannel,
  updateAlertChannel,
  deleteAlertChannel,
  testAlertChannel,
  buildChannelConfig,
  channelTypeLabel,
  primaryFieldLabel,
} from "@/lib/api-alertchannels";

// ─── Helpers ────────────────────────────────────────────────────────────────

function fmtDate(ts: string | null): string {
  if (!ts) return "—";
  try {
    return new Date(ts).toLocaleString();
  } catch {
    return ts;
  }
}

function typeDotColor(type: ChannelType): string {
  switch (type) {
    case "email":
      return "#60a5fa";
    case "slack":
      return "#a78bfa";
    case "webhook":
      return "#34d399";
    case "pagerduty":
      return "#f87171";
    default:
      return "#9ca3af";
  }
}

function TypeBadge({ type }: { type: ChannelType }) {
  return (
    <span style={{ display: "inline-flex", alignItems: "center", gap: "0.35rem", fontSize: "0.8rem" }}>
      <span
        style={{
          width: "8px",
          height: "8px",
          borderRadius: "50%",
          background: typeDotColor(type),
          display: "inline-block",
          flexShrink: 0,
        }}
      />
      {channelTypeLabel(type)}
    </span>
  );
}

// ─── New Channel Form ─────────────────────────────────────────────────────────

function NewChannelForm({
  onCreated,
  onCancel,
}: {
  onCreated: () => void;
  onCancel: () => void;
}) {
  const [name, setName] = useState("");
  const [type, setType] = useState<ChannelType>("slack");
  const [primary, setPrimary] = useState("");
  const [signingSecret, setSigningSecret] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim()) return;
    setSubmitting(true);
    setError(null);
    createAlertChannel({
      type,
      name: name.trim(),
      config: buildChannelConfig(type, primary.trim(), { signingSecret: signingSecret.trim() }),
    })
      .then(() => onCreated())
      .catch((err: unknown) => {
        setError(err instanceof Error ? err.message : String(err));
        setSubmitting(false);
      });
  }

  return (
    <form className="inline-form" onSubmit={handleSubmit}>
      <div className="inline-form-row">
        <label>Name</label>
        <input
          type="text"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="e.g. Ops Slack"
          required
        />
      </div>
      <div className="inline-form-row">
        <label>Type</label>
        <select value={type} onChange={(e) => setType(e.target.value as ChannelType)}>
          <option value="slack">Slack</option>
          <option value="webhook">Webhook</option>
          <option value="pagerduty">PagerDuty</option>
          <option value="email">Email</option>
        </select>
      </div>
      <div className="inline-form-row">
        <label>{primaryFieldLabel(type)}</label>
        <input
          type="text"
          value={primary}
          onChange={(e) => setPrimary(e.target.value)}
          placeholder={primaryFieldLabel(type)}
        />
      </div>
      {type === "webhook" && (
        <div className="inline-form-row">
          <label>Signing Secret (optional)</label>
          <input
            type="text"
            value={signingSecret}
            onChange={(e) => setSigningSecret(e.target.value)}
            placeholder="HMAC-SHA256 secret for X-MXS-Signature"
          />
        </div>
      )}
      {error && (
        <p className="state-msg error" style={{ margin: "0.25rem 0" }}>
          Error: {error}
        </p>
      )}
      <div className="inline-form-actions">
        <button type="submit" className="btn btn-sm" disabled={submitting}>
          {submitting ? "Creating…" : "Create Channel"}
        </button>
        <button type="button" className="btn btn-sm" onClick={onCancel}>
          Cancel
        </button>
      </div>
    </form>
  );
}

// ─── Main Page ────────────────────────────────────────────────────────────────

export default function AlertChannelsPage() {
  const [channels, setChannels] = useState<AlertChannel[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [showForm, setShowForm] = useState(false);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [testResult, setTestResult] = useState<{ id: string; ok: boolean; msg: string } | null>(null);

  const fetchChannels = useCallback(() => {
    setLoading(true);
    setError(null);
    listAlertChannels()
      .then((d) => setChannels(d.alert_channels ?? []))
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    fetchChannels();
  }, [fetchChannels]);

  function handleToggle(ch: AlertChannel) {
    setBusyId(ch.id);
    updateAlertChannel(ch.id, { enabled: !ch.enabled })
      .then(() => fetchChannels())
      .catch(() => {})
      .finally(() => setBusyId(null));
  }

  function handleDelete(id: string) {
    if (!confirm("Delete this alert channel?")) return;
    setBusyId(id);
    deleteAlertChannel(id)
      .then(() => fetchChannels())
      .catch(() => {})
      .finally(() => setBusyId(null));
  }

  function handleTest(ch: AlertChannel) {
    setBusyId(ch.id);
    setTestResult(null);
    testAlertChannel(ch.id)
      .then((r) => setTestResult({ id: ch.id, ok: r.ok, msg: "Test notification sent" }))
      .catch((e: unknown) =>
        setTestResult({ id: ch.id, ok: false, msg: e instanceof Error ? e.message : String(e) }),
      )
      .finally(() => setBusyId(null));
  }

  return (
    <>
      <h1>Alert Channels</h1>
      <p style={{ color: "#9ca3af", fontSize: "0.9rem", marginTop: "-0.5rem" }}>
        Delivery destinations for firing alerts and incidents. Secrets are encrypted at rest
        and shown redacted. Per-channel throttling stops a flapping alert from spamming a
        destination.
      </p>

      <div className="section-block">
        <div className="section-header">
          <h2>Channels</h2>
          {!showForm && (
            <button className="btn btn-sm" onClick={() => setShowForm(true)}>
              + New Channel
            </button>
          )}
        </div>

        {showForm && (
          <NewChannelForm
            onCreated={() => {
              setShowForm(false);
              fetchChannels();
            }}
            onCancel={() => setShowForm(false)}
          />
        )}

        <LoadingError loading={loading} error={error} />

        {!loading && !error && (
          <>
            {channels.length === 0 ? (
              <p className="no-results">No alert channels configured.</p>
            ) : (
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>Name</th>
                      <th>Type</th>
                      <th>Status</th>
                      <th>Created</th>
                      <th></th>
                    </tr>
                  </thead>
                  <tbody>
                    {channels.map((ch) => (
                      <tr key={ch.id}>
                        <td style={{ fontWeight: 500 }}>
                          {ch.name}
                          {testResult && testResult.id === ch.id && (
                            <div
                              style={{
                                fontSize: "0.75rem",
                                marginTop: "0.25rem",
                                color: testResult.ok ? "#34d399" : "#f87171",
                              }}
                            >
                              {testResult.msg}
                            </div>
                          )}
                        </td>
                        <td>
                          <TypeBadge type={ch.type} />
                        </td>
                        <td>
                          <span className={`badge ${ch.enabled ? "badge-ok" : "badge-unknown"}`}>
                            {ch.enabled ? "enabled" : "disabled"}
                          </span>
                        </td>
                        <td style={{ whiteSpace: "nowrap", fontSize: "0.85rem" }}>
                          {fmtDate(ch.created_at)}
                        </td>
                        <td style={{ whiteSpace: "nowrap" }}>
                          <button
                            className="btn btn-sm"
                            onClick={() => handleTest(ch)}
                            disabled={busyId === ch.id}
                            style={{ marginRight: "0.4rem" }}
                          >
                            Test
                          </button>
                          <button
                            className={`toggle-btn ${ch.enabled ? "toggle-on" : "toggle-off"}`}
                            onClick={() => handleToggle(ch)}
                            disabled={busyId === ch.id}
                            title={ch.enabled ? "Disable" : "Enable"}
                            style={{ marginRight: "0.4rem" }}
                          >
                            {ch.enabled ? "On" : "Off"}
                          </button>
                          <button
                            className="btn-danger-sm"
                            onClick={() => handleDelete(ch.id)}
                            disabled={busyId === ch.id}
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
          </>
        )}
      </div>
    </>
  );
}

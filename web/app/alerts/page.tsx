"use client";

import React, { useState, useEffect, useCallback } from "react";
import { apiGet, apiPost, apiPatch, apiDelete } from "@/lib/api";
import Badge from "@/components/Badge";
import LoadingError from "@/components/LoadingError";

// ─── Types ────────────────────────────────────────────────────────────────────

type SignalType =
  | "dns_breakage"
  | "rejection_spike"
  | "blacklist_hit"
  | "tls_failure"
  | "complaint_spike"
  | "bounce_rate";

type ChannelKind = "email" | "slack" | "webhook" | "pagerduty";

interface AlertRule {
  id: string;
  name: string;
  signal: SignalType;
  enabled: boolean;
  threshold: number | null;
  channel_ids: string[];
  created_at: string;
}

interface AlertRulesResponse {
  alert_rules: AlertRule[];
}

interface NotificationChannel {
  id: string;
  name: string;
  kind: ChannelKind;
  enabled: boolean;
  created_at: string;
}

interface ChannelsResponse {
  channels: NotificationChannel[];
}

interface AlertEvent {
  id: string;
  rule_id: string;
  rule_name: string;
  state: string;
  triggered_at: string;
  payload: Record<string, unknown>;
}

interface AlertEventsResponse {
  alert_events: AlertEvent[];
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

function fmtDate(ts: string | null): string {
  if (!ts) return "—";
  try {
    return new Date(ts).toLocaleString();
  } catch {
    return ts;
  }
}

function signalLabel(signal: SignalType): string {
  switch (signal) {
    case "dns_breakage":
      return "DNS Breakage";
    case "rejection_spike":
      return "Rejection Spike";
    case "blacklist_hit":
      return "Blacklist Hit";
    case "tls_failure":
      return "TLS Failure";
    case "complaint_spike":
      return "Complaint Spike";
    case "bounce_rate":
      return "Bounce Rate";
    default:
      return signal;
  }
}

function signalColor(signal: SignalType): string {
  switch (signal) {
    case "dns_breakage":
      return "signal-red";
    case "rejection_spike":
      return "signal-orange";
    case "blacklist_hit":
      return "signal-red";
    case "tls_failure":
      return "signal-orange";
    case "complaint_spike":
      return "signal-yellow";
    case "bounce_rate":
      return "signal-yellow";
    default:
      return "signal-gray";
  }
}

function kindDotColor(kind: ChannelKind): string {
  switch (kind) {
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

function buildChannelConfig(
  kind: ChannelKind,
  configUrl: string,
): Record<string, string> {
  switch (kind) {
    case "slack":
      return { webhook_url: configUrl };
    case "webhook":
      return { url: configUrl };
    case "email":
      return { address: configUrl };
    case "pagerduty":
      return { integration_key: configUrl };
    default:
      return { url: configUrl };
  }
}

// ─── Signal Badge ─────────────────────────────────────────────────────────────

function SignalBadge({ signal }: { signal: SignalType }) {
  return (
    <span className={`badge signal-badge ${signalColor(signal)}`}>
      {signalLabel(signal)}
    </span>
  );
}

// ─── Kind Badge ───────────────────────────────────────────────────────────────

function KindBadge({ kind }: { kind: ChannelKind }) {
  return (
    <span style={{ display: "inline-flex", alignItems: "center", gap: "0.35rem", fontSize: "0.8rem" }}>
      <span
        style={{
          width: "8px",
          height: "8px",
          borderRadius: "50%",
          background: kindDotColor(kind),
          display: "inline-block",
          flexShrink: 0,
        }}
      />
      {kind}
    </span>
  );
}

// ─── New Rule Form ────────────────────────────────────────────────────────────

function NewRuleForm({
  onCreated,
  onCancel,
}: {
  onCreated: () => void;
  onCancel: () => void;
}) {
  const [name, setName] = useState("");
  const [signal, setSignal] = useState<SignalType>("rejection_spike");
  const [threshold, setThreshold] = useState("");
  const [channelIds, setChannelIds] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const showThreshold =
    signal === "bounce_rate" || signal === "rejection_spike";

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim()) return;
    setSubmitting(true);
    setError(null);
    const body: Record<string, unknown> = {
      name: name.trim(),
      signal,
      channel_ids: channelIds
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean),
    };
    if (showThreshold && threshold !== "") {
      body.threshold = parseFloat(threshold);
    }
    apiPost("/v1/alert-rules", body)
      .then(() => onCreated())
      .catch((e: unknown) => {
        setError(e instanceof Error ? e.message : String(e));
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
          placeholder="Rule name"
          required
        />
      </div>
      <div className="inline-form-row">
        <label>Signal</label>
        <select
          value={signal}
          onChange={(e) => setSignal(e.target.value as SignalType)}
        >
          <option value="dns_breakage">DNS Breakage</option>
          <option value="rejection_spike">Rejection Spike</option>
          <option value="blacklist_hit">Blacklist Hit</option>
          <option value="tls_failure">TLS Failure</option>
          <option value="complaint_spike">Complaint Spike</option>
          <option value="bounce_rate">Bounce Rate</option>
        </select>
      </div>
      {showThreshold && (
        <div className="inline-form-row">
          <label>Threshold %</label>
          <input
            type="number"
            value={threshold}
            onChange={(e) => setThreshold(e.target.value)}
            placeholder="e.g. 10"
            min={0}
            max={100}
            step={0.1}
          />
        </div>
      )}
      <div className="inline-form-row">
        <label>Channel IDs</label>
        <input
          type="text"
          value={channelIds}
          onChange={(e) => setChannelIds(e.target.value)}
          placeholder="Comma-separated channel IDs"
        />
      </div>
      {error && <p className="state-msg error" style={{ margin: "0.25rem 0" }}>Error: {error}</p>}
      <div className="inline-form-actions">
        <button type="submit" disabled={submitting}>
          {submitting ? "Creating…" : "Create Rule"}
        </button>
        <button type="button" className="btn-secondary" onClick={onCancel}>
          Cancel
        </button>
      </div>
    </form>
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
  const [kind, setKind] = useState<ChannelKind>("email");
  const [configUrl, setConfigUrl] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function configUrlLabel(k: ChannelKind): string {
    switch (k) {
      case "email":
        return "Email Address";
      case "slack":
        return "Webhook URL";
      case "webhook":
        return "Webhook URL";
      case "pagerduty":
        return "Integration Key";
      default:
        return "Webhook URL / Email Address";
    }
  }

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim()) return;
    setSubmitting(true);
    setError(null);
    apiPost("/v1/notification-channels", {
      name: name.trim(),
      kind,
      config: buildChannelConfig(kind, configUrl.trim()),
    })
      .then(() => onCreated())
      .catch((e: unknown) => {
        setError(e instanceof Error ? e.message : String(e));
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
          placeholder="Channel name"
          required
        />
      </div>
      <div className="inline-form-row">
        <label>Kind</label>
        <select
          value={kind}
          onChange={(e) => setKind(e.target.value as ChannelKind)}
        >
          <option value="email">Email</option>
          <option value="slack">Slack</option>
          <option value="webhook">Webhook</option>
          <option value="pagerduty">PagerDuty</option>
        </select>
      </div>
      <div className="inline-form-row">
        <label>{configUrlLabel(kind)}</label>
        <input
          type="text"
          value={configUrl}
          onChange={(e) => setConfigUrl(e.target.value)}
          placeholder={configUrlLabel(kind)}
        />
      </div>
      {error && <p className="state-msg error" style={{ margin: "0.25rem 0" }}>Error: {error}</p>}
      <div className="inline-form-actions">
        <button type="submit" disabled={submitting}>
          {submitting ? "Creating…" : "Create Channel"}
        </button>
        <button type="button" className="btn-secondary" onClick={onCancel}>
          Cancel
        </button>
      </div>
    </form>
  );
}

// ─── Alert Rules Section ──────────────────────────────────────────────────────

function AlertRulesSection() {
  const [rules, setRules] = useState<AlertRule[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [showForm, setShowForm] = useState(false);
  const [togglingId, setTogglingId] = useState<string | null>(null);
  const [deletingId, setDeletingId] = useState<string | null>(null);

  const fetchRules = useCallback(() => {
    setLoading(true);
    setError(null);
    apiGet<AlertRulesResponse>("/v1/alert-rules")
      .then((d) => setRules(d.alert_rules ?? []))
      .catch((e: unknown) =>
        setError(e instanceof Error ? e.message : String(e))
      )
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    fetchRules();
  }, [fetchRules]);

  function handleToggle(rule: AlertRule) {
    setTogglingId(rule.id);
    apiPatch(`/v1/alert-rules/${rule.id}`, { enabled: !rule.enabled })
      .then(() => fetchRules())
      .catch(() => {/* silently ignore; list stays as-is */})
      .finally(() => setTogglingId(null));
  }

  function handleDelete(id: string) {
    if (!confirm("Delete this alert rule?")) return;
    setDeletingId(id);
    apiDelete(`/v1/alert-rules/${id}`)
      .then(() => fetchRules())
      .catch(() => {/* silently ignore */})
      .finally(() => setDeletingId(null));
  }

  return (
    <div className="section-block">
      <div className="section-header">
        <h2>Alert Rules</h2>
        {!showForm && (
          <button onClick={() => setShowForm(true)}>+ New Rule</button>
        )}
      </div>

      {showForm && (
        <NewRuleForm
          onCreated={() => {
            setShowForm(false);
            fetchRules();
          }}
          onCancel={() => setShowForm(false)}
        />
      )}

      <LoadingError loading={loading} error={error} />

      {!loading && !error && (
        <>
          {rules.length === 0 ? (
            <p className="no-results">No alert rules configured.</p>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Name</th>
                    <th>Signal</th>
                    <th>Condition</th>
                    <th>Channels</th>
                    <th>Enabled</th>
                    <th></th>
                  </tr>
                </thead>
                <tbody>
                  {rules.map((rule) => (
                    <tr key={rule.id}>
                      <td style={{ fontWeight: 500 }}>{rule.name}</td>
                      <td>
                        <SignalBadge signal={rule.signal} />
                      </td>
                      <td style={{ fontSize: "0.85rem", color: "#9ca3af" }}>
                        {rule.threshold != null
                          ? `≥ ${rule.threshold}%`
                          : "Any"}
                      </td>
                      <td style={{ fontSize: "0.85rem" }}>
                        {rule.channel_ids?.length
                          ? `${rule.channel_ids.length} channel${rule.channel_ids.length !== 1 ? "s" : ""}`
                          : "—"}
                      </td>
                      <td>
                        <button
                          className={`toggle-btn ${rule.enabled ? "toggle-on" : "toggle-off"}`}
                          onClick={() => handleToggle(rule)}
                          disabled={togglingId === rule.id}
                          title={rule.enabled ? "Disable" : "Enable"}
                        >
                          {rule.enabled ? "On" : "Off"}
                        </button>
                      </td>
                      <td>
                        <button
                          className="btn-danger-sm"
                          onClick={() => handleDelete(rule.id)}
                          disabled={deletingId === rule.id}
                        >
                          {deletingId === rule.id ? "…" : "Delete"}
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
  );
}

// ─── Notification Channels Section ───────────────────────────────────────────

function NotificationChannelsSection() {
  const [channels, setChannels] = useState<NotificationChannel[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [showForm, setShowForm] = useState(false);
  const [deletingId, setDeletingId] = useState<string | null>(null);

  const fetchChannels = useCallback(() => {
    setLoading(true);
    setError(null);
    apiGet<ChannelsResponse>("/v1/notification-channels")
      .then((d) => setChannels(d.channels ?? []))
      .catch((e: unknown) =>
        setError(e instanceof Error ? e.message : String(e))
      )
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    fetchChannels();
  }, [fetchChannels]);

  function handleDelete(id: string) {
    if (!confirm("Delete this notification channel?")) return;
    setDeletingId(id);
    apiDelete(`/v1/notification-channels/${id}`)
      .then(() => fetchChannels())
      .catch(() => {/* silently ignore */})
      .finally(() => setDeletingId(null));
  }

  return (
    <div className="section-block">
      <div className="section-header">
        <h2>Notification Channels</h2>
        {!showForm && (
          <button onClick={() => setShowForm(true)}>+ New Channel</button>
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
            <p className="no-results">No notification channels configured.</p>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Name</th>
                    <th>Kind</th>
                    <th>Status</th>
                    <th>Created</th>
                    <th></th>
                  </tr>
                </thead>
                <tbody>
                  {channels.map((ch) => (
                    <tr key={ch.id}>
                      <td style={{ fontWeight: 500 }}>{ch.name}</td>
                      <td>
                        <KindBadge kind={ch.kind} />
                      </td>
                      <td>
                        <span
                          className={`badge ${ch.enabled ? "badge-ok" : "badge-unknown"}`}
                        >
                          {ch.enabled ? "enabled" : "disabled"}
                        </span>
                      </td>
                      <td style={{ whiteSpace: "nowrap", fontSize: "0.85rem" }}>
                        {fmtDate(ch.created_at)}
                      </td>
                      <td>
                        <button
                          className="btn-danger-sm"
                          onClick={() => handleDelete(ch.id)}
                          disabled={deletingId === ch.id}
                        >
                          {deletingId === ch.id ? "…" : "Delete"}
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
  );
}

// ─── Alert Events Section ─────────────────────────────────────────────────────

function AlertEventsSection() {
  const [events, setEvents] = useState<AlertEvent[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const fetchEvents = useCallback(() => {
    setLoading(true);
    setError(null);
    apiGet<AlertEventsResponse>("/v1/alert-events")
      .then((d) => setEvents(d.alert_events ?? []))
      .catch((e: unknown) =>
        setError(e instanceof Error ? e.message : String(e))
      )
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    fetchEvents();
    const interval = setInterval(fetchEvents, 30_000);
    return () => clearInterval(interval);
  }, [fetchEvents]);

  function payloadSummary(payload: Record<string, unknown>): string {
    const keys = Object.keys(payload);
    if (keys.length === 0) return "—";
    return keys
      .slice(0, 3)
      .map((k) => `${k}: ${String(payload[k])}`)
      .join(", ");
  }

  return (
    <div className="section-block">
      <div className="section-header">
        <h2>Alert Events</h2>
        <span style={{ fontSize: "0.8rem", color: "#6b7280" }}>
          Auto-refreshes every 30s
        </span>
      </div>

      <LoadingError loading={loading} error={error} />

      {!loading && !error && (
        <>
          {events.length === 0 ? (
            <p className="no-results">No alert events recorded.</p>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>State</th>
                    <th>Rule</th>
                    <th>Triggered At</th>
                    <th>Payload</th>
                  </tr>
                </thead>
                <tbody>
                  {events.map((ev) => (
                    <tr key={ev.id}>
                      <td>
                        <span className={`badge badge-${ev.state === "firing" ? "critical" : ev.state === "resolved" ? "ok" : "unknown"}`}>
                          {ev.state}
                        </span>
                      </td>
                      <td style={{ fontWeight: 500 }}>{ev.rule_name || "—"}</td>
                      <td style={{ whiteSpace: "nowrap", fontSize: "0.85rem" }}>
                        {fmtDate(ev.triggered_at)}
                      </td>
                      <td
                        style={{
                          maxWidth: "320px",
                          overflow: "hidden",
                          textOverflow: "ellipsis",
                          whiteSpace: "nowrap",
                          fontSize: "0.8rem",
                          color: "#9ca3af",
                        }}
                        title={JSON.stringify(ev.payload)}
                      >
                        {payloadSummary(ev.payload)}
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
  );
}

// ─── Main Page ────────────────────────────────────────────────────────────────

type Tab = "rules" | "channels";

export default function AlertsPage() {
  const [activeTab, setActiveTab] = useState<Tab>("rules");

  return (
    <>
      <h1>Alerts</h1>

      <div className="tabs" style={{ marginBottom: "1.5rem" }}>
        <button
          className={`tab-btn ${activeTab === "rules" ? "tab-active" : ""}`}
          onClick={() => setActiveTab("rules")}
        >
          Alert Rules
        </button>
        <button
          className={`tab-btn ${activeTab === "channels" ? "tab-active" : ""}`}
          onClick={() => setActiveTab("channels")}
        >
          Notification Channels
        </button>
      </div>

      {activeTab === "rules" && <AlertRulesSection />}
      {activeTab === "channels" && <NotificationChannelsSection />}

      <AlertEventsSection />
    </>
  );
}

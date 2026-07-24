"use client";

import { useEffect, useState } from "react";
import {
  getIntegrationSettings,
  updateIntegrationSettings,
  type IntegrationSettings,
} from "@/lib/api";

const EMPTY: IntegrationSettings = {
  ai: { endpoint: "", model: "", api_key: "", api_key_set: false, timeout_secs: 60 },
  microsoft: { snds_key: "", snds_key_set: false },
  google: { postmaster_token: "", postmaster_token_set: false },
  notify_email: { host: "", port: 587, username: "", password: "", password_set: false, from: "" },
};

/** Secret input whose placeholder reflects whether a value is already stored. */
function SecretField(props: {
  id: string;
  label: string;
  isSet: boolean;
  value: string;
  onChange: (v: string) => void;
  hint?: string;
}) {
  return (
    <div className="field">
      <label htmlFor={props.id}>{props.label}</label>
      <input
        id={props.id}
        type="password"
        autoComplete="new-password"
        placeholder={props.isSet ? "•••••••• (unchanged)" : "not set"}
        value={props.value}
        onChange={(e) => props.onChange(e.target.value)}
      />
      <span className="field-hint">
        {props.isSet ? "Stored. Leave blank to keep it; type to replace." : props.hint || "Stored encrypted."}
      </span>
    </div>
  );
}

/**
 * Dashboard-managed provider credentials/endpoints (previously env-only). Secrets are sealed
 * at rest and write-only. Dashboard values win over env; changes take effect when the owning
 * daemon restarts. See docs/settings-inventory.md.
 */
export default function ProvidersSection() {
  const [form, setForm] = useState<IntegrationSettings>(EMPTY);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);

  useEffect(() => {
    getIntegrationSettings()
      .then((d) => setForm(clearSecrets(d.integrations)))
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, []);

  function clearSecrets(v: IntegrationSettings): IntegrationSettings {
    return {
      ...v,
      ai: { ...v.ai, api_key: "" },
      microsoft: { ...v.microsoft, snds_key: "" },
      google: { ...v.google, postmaster_token: "" },
      notify_email: { ...v.notify_email, password: "" },
    };
  }

  function patch(section: keyof IntegrationSettings, key: string, value: unknown) {
    setForm((f) => ({ ...f, [section]: { ...f[section], [key]: value } }));
    setSaved(false);
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setSaving(true);
    setSaveError(null);
    setSaved(false);
    // Strip empty secrets so the stored ones are preserved.
    const payload: IntegrationSettings = JSON.parse(JSON.stringify(form));
    if (!payload.ai.api_key) delete payload.ai.api_key;
    if (!payload.microsoft.snds_key) delete payload.microsoft.snds_key;
    if (!payload.google.postmaster_token) delete payload.google.postmaster_token;
    if (!payload.notify_email.password) delete payload.notify_email.password;
    try {
      const d = await updateIntegrationSettings(payload);
      setForm(clearSecrets(d.integrations));
      setSaved(true);
    } catch (err: unknown) {
      setSaveError(err instanceof Error ? err.message : "Failed to save provider settings");
    } finally {
      setSaving(false);
    }
  }

  if (loading) return <p className="section-desc">Loading providers…</p>;
  if (error) return <p className="section-desc" style={{ color: "var(--danger, #c0392b)" }}>{error}</p>;

  return (
    <form onSubmit={handleSubmit} className="settings-form">
      <fieldset className="settings-group">
        <legend>Providers &amp; keys</legend>
        <p className="section-desc">
          API keys and endpoints for the diagnostics LLM and deliverability data sources. Secrets are
          encrypted at rest; changes take effect when the relevant daemon restarts.
        </p>

        <h3 className="subhead">AI diagnostics (aid)</h3>
        <div className="field">
          <label htmlFor="ai_endpoint">Endpoint</label>
          <input id="ai_endpoint" placeholder="http://host.docker.internal:11434/v1"
            value={form.ai.endpoint} onChange={(e) => patch("ai", "endpoint", e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor="ai_model">Model</label>
          <input id="ai_model" placeholder="llama3.2:3b"
            value={form.ai.model} onChange={(e) => patch("ai", "model", e.target.value)} />
        </div>
        <SecretField id="ai_key" label="API key" isSet={form.ai.api_key_set}
          value={form.ai.api_key || ""} onChange={(v) => patch("ai", "api_key", v)}
          hint="Optional; many local LLM servers ignore it." />
        <div className="field">
          <label htmlFor="ai_timeout">Timeout (secs)</label>
          <input id="ai_timeout" type="number" value={form.ai.timeout_secs}
            onChange={(e) => patch("ai", "timeout_secs", Number(e.target.value))} />
        </div>

        <h3 className="subhead">Microsoft SNDS (sndsd)</h3>
        <SecretField id="snds_key" label="SNDS access key" isSet={form.microsoft.snds_key_set}
          value={form.microsoft.snds_key || ""} onChange={(v) => patch("microsoft", "snds_key", v)}
          hint="From your SNDS automated-data URL. Blank disables the SNDS poll." />

        <h3 className="subhead">Google Postmaster (fbld)</h3>
        <SecretField id="pm_token" label="Postmaster token" isSet={form.google.postmaster_token_set}
          value={form.google.postmaster_token || ""} onChange={(v) => patch("google", "postmaster_token", v)}
          hint="Blank skips Gmail Postmaster reputation (FBL complaints still ingest)." />

        <h3 className="subhead">Notification email sender</h3>
        <div className="field">
          <label htmlFor="ne_host">SMTP host</label>
          <input id="ne_host" value={form.notify_email.host}
            onChange={(e) => patch("notify_email", "host", e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor="ne_port">Port</label>
          <input id="ne_port" type="number" value={form.notify_email.port}
            onChange={(e) => patch("notify_email", "port", Number(e.target.value))} />
        </div>
        <div className="field">
          <label htmlFor="ne_user">Username</label>
          <input id="ne_user" value={form.notify_email.username}
            onChange={(e) => patch("notify_email", "username", e.target.value)} />
        </div>
        <SecretField id="ne_pass" label="Password" isSet={form.notify_email.password_set}
          value={form.notify_email.password || ""} onChange={(v) => patch("notify_email", "password", v)} />
        <div className="field">
          <label htmlFor="ne_from">From address</label>
          <input id="ne_from" placeholder="alerts@yourdomain.com" value={form.notify_email.from}
            onChange={(e) => patch("notify_email", "from", e.target.value)} />
        </div>

        <div style={{ marginTop: "1rem", display: "flex", gap: "0.75rem", alignItems: "center" }}>
          <button type="submit" disabled={saving}>{saving ? "Saving…" : "Save providers"}</button>
          {saved && <span className="field-hint" style={{ color: "var(--ok, #2e7d32)" }}>Saved ✓</span>}
          {saveError && <span className="field-hint" style={{ color: "var(--danger, #c0392b)" }}>{saveError}</span>}
        </div>
      </fieldset>
    </form>
  );
}

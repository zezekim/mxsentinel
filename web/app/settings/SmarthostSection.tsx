"use client";

import { useEffect, useState } from "react";
import { getSmarthost, updateSmarthost, type Smarthost, type SmarthostMode } from "@/lib/api";

const EMPTY: Smarthost = {
  enabled: false,
  host: "",
  port: 587,
  username: "",
  password: "",
  password_set: false,
  mode: "always",
  domains: [],
};

const OUTLOOK_DEFAULTS = "outlook.com, hotmail.com, live.com, msn.com, outlook.co.uk, hotmail.co.uk, live.co.uk";

/**
 * Outbound fallback smarthost (e.g. mail.baby). Replaces hand-editing /etc/postfix — the
 * password is sealed at rest and write-only in the API. relayfailoverd renders it onto the
 * relay via the host hook. See docs/relay-failover.md and docs/settings-inventory.md.
 */
export default function SmarthostSection() {
  const [form, setForm] = useState<Smarthost>(EMPTY);
  const [domainsText, setDomainsText] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [showAdvanced, setShowAdvanced] = useState(false);

  useEffect(() => {
    getSmarthost()
      .then((d) => {
        setForm({ ...d.smarthost, password: "" });
        setDomainsText((d.smarthost.domains || []).join(", "));
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, []);

  function set<K extends keyof Smarthost>(key: K, value: Smarthost[K]) {
    setForm((f) => ({ ...f, [key]: value }));
    setSaved(false);
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setSaving(true);
    setSaveError(null);
    setSaved(false);
    const domains = domainsText
      .split(/[\s,]+/)
      .map((d) => d.trim().toLowerCase())
      .filter(Boolean);
    try {
      // Omit an empty password so the stored one is preserved.
      const payload: Smarthost = { ...form, domains };
      if (!payload.password) delete payload.password;
      const d = await updateSmarthost(payload);
      setForm({ ...d.smarthost, password: "" });
      setDomainsText((d.smarthost.domains || []).join(", "));
      setSaved(true);
    } catch (err: unknown) {
      setSaveError(err instanceof Error ? err.message : "Failed to save smarthost");
    } finally {
      setSaving(false);
    }
  }

  if (loading) return <p className="section-desc">Loading fallback smarthost…</p>;
  if (error) return <p className="section-desc" style={{ color: "var(--danger, #c0392b)" }}>{error}</p>;

  return (
    <form onSubmit={handleSubmit} className="settings-form">
      <fieldset className="settings-group">
        <legend>Outbound fallback smarthost</legend>
        <p className="section-desc">
          Route mail for specific recipient domains through a managed relay (e.g. mail.baby) when your
          own IP can&apos;t deliver to them — for example when Outlook blocks your sending IP&apos;s
          reputation. Credentials are encrypted at rest and applied to the relay automatically.
        </p>

        <label className="check-row">
          <input
            type="checkbox"
            checked={form.enabled}
            onChange={(e) => set("enabled", e.target.checked)}
          />{" "}
          Enable fallback smarthost
        </label>

        <div className="field">
          <label htmlFor="sh_host">Smarthost host</label>
          <input
            id="sh_host"
            placeholder="relay.mailbaby.net"
            value={form.host}
            onChange={(e) => set("host", e.target.value)}
          />
          <span className="field-hint">Use the exact host your provider gives you.</span>
        </div>

        <div className="field">
          <label htmlFor="sh_port">Port</label>
          <input
            id="sh_port"
            type="number"
            value={form.port}
            onChange={(e) => set("port", Number(e.target.value))}
          />
          <span className="field-hint">Usually 587 (submission, STARTTLS).</span>
        </div>

        <div className="field">
          <label htmlFor="sh_user">Username</label>
          <input
            id="sh_user"
            value={form.username}
            onChange={(e) => set("username", e.target.value)}
          />
        </div>

        <div className="field">
          <label htmlFor="sh_pass">Password</label>
          <input
            id="sh_pass"
            type="password"
            placeholder={form.password_set ? "•••••••• (unchanged)" : "set a password"}
            value={form.password || ""}
            onChange={(e) => set("password", e.target.value)}
            autoComplete="new-password"
          />
          <span className="field-hint">
            {form.password_set
              ? "A password is stored. Leave blank to keep it; type to replace."
              : "Stored encrypted; never shown again."}
          </span>
        </div>

        <div className="field">
          <label htmlFor="sh_mode">Routing mode</label>
          <select
            id="sh_mode"
            value={form.mode}
            onChange={(e) => set("mode", e.target.value as SmarthostMode)}
          >
            <option value="always">Always route these domains via the smarthost</option>
            <option value="on_throttle">Only when the direct path is being throttled (4xx)</option>
          </select>
          <span className="field-hint">
            {form.mode === "always"
              ? "Best for a persistent block (e.g. Outlook S3140/S3150 on your IP)."
              : "Automatic failover on sustained transient 4xx defers; auto-reverts when they clear."}
          </span>
        </div>

        <div className="field">
          <label htmlFor="sh_domains">Recipient domains</label>
          <textarea
            id="sh_domains"
            rows={3}
            placeholder={OUTLOOK_DEFAULTS}
            value={domainsText}
            onChange={(e) => {
              setDomainsText(e.target.value);
              setSaved(false);
            }}
          />
          <span className="field-hint">
            Comma- or space-separated. <button type="button" className="linklike" onClick={() => setDomainsText(OUTLOOK_DEFAULTS)}>Use Outlook set</button>
          </span>
        </div>

        <button type="button" className="linklike" onClick={() => setShowAdvanced((v) => !v)}>
          {showAdvanced ? "Hide" : "Show"} failover tuning (advanced)
        </button>
        {showAdvanced && (
          <div className="advanced-grid" style={{ marginTop: "0.75rem" }}>
            <div className="field">
              <label htmlFor="sh_trip">Trip rate (0–1)</label>
              <input id="sh_trip" type="number" step="0.05" min="0" max="1"
                value={form.trip_rate ?? ""} onChange={(e) => set("trip_rate", Number(e.target.value))} />
            </div>
            <div className="field">
              <label htmlFor="sh_window">Window (secs)</label>
              <input id="sh_window" type="number" value={form.window_secs ?? ""}
                onChange={(e) => set("window_secs", Number(e.target.value))} />
            </div>
            <div className="field">
              <label htmlFor="sh_hold">Hold (secs)</label>
              <input id="sh_hold" type="number" value={form.hold_secs ?? ""}
                onChange={(e) => set("hold_secs", Number(e.target.value))} />
            </div>
            <div className="field">
              <label htmlFor="sh_minatt">Min attempts</label>
              <input id="sh_minatt" type="number" value={form.min_attempts ?? ""}
                onChange={(e) => set("min_attempts", Number(e.target.value))} />
            </div>
            <div className="field">
              <label htmlFor="sh_mindef">Min defers</label>
              <input id="sh_mindef" type="number" value={form.min_defers ?? ""}
                onChange={(e) => set("min_defers", Number(e.target.value))} />
            </div>
            <span className="field-hint">Only used in &quot;on throttling&quot; mode. Blank = daemon defaults.</span>
          </div>
        )}

        <div style={{ marginTop: "1rem", display: "flex", gap: "0.75rem", alignItems: "center" }}>
          <button type="submit" disabled={saving}>
            {saving ? "Saving…" : "Save smarthost"}
          </button>
          {saved && <span className="field-hint" style={{ color: "var(--ok, #2e7d32)" }}>Saved ✓</span>}
          {saveError && <span className="field-hint" style={{ color: "var(--danger, #c0392b)" }}>{saveError}</span>}
        </div>
      </fieldset>
    </form>
  );
}

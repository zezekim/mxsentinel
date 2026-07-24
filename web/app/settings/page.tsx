"use client";

import { useEffect, useState } from "react";
import { getSettings, updateSettings, type MailSettings, type DMARCPolicy } from "@/lib/api";
import LoadingError from "@/components/LoadingError";
import SmarthostSection from "./SmarthostSection";
import ProvidersSection from "./ProvidersSection";
import AbuseTuningSection from "./AbuseTuningSection";
import MonitoringTuningSection from "./MonitoringTuningSection";
import DeliveryTuningSection from "./DeliveryTuningSection";

const EMPTY: MailSettings = {
  spf_include: "",
  dkim_selector: "mxs",
  dmarc_policy: "none",
  dmarc_rua: "",
  dmarc_ruf: "",
  relay_host: "",
  relay_port: 587,
  resolver_address: "",
  resolver_timeout_secs: 5,
};

const DMARC_POLICIES: DMARCPolicy[] = ["none", "quarantine", "reject"];

export default function SettingsPage() {
  const [form, setForm] = useState<MailSettings>(EMPTY);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);

  useEffect(() => {
    getSettings()
      .then((d) => setForm(d.settings))
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, []);

  function set<K extends keyof MailSettings>(key: K, value: MailSettings[K]) {
    setForm((f) => ({ ...f, [key]: value }));
    setSaved(false);
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setSaving(true);
    setSaveError(null);
    setSaved(false);
    try {
      const d = await updateSettings(form);
      setForm(d.settings);
      setSaved(true);
    } catch (err: unknown) {
      setSaveError(err instanceof Error ? err.message : "Failed to save settings");
    } finally {
      setSaving(false);
    }
  }

  // Derived guidance previews.
  const spfRecord = form.spf_include ? `v=spf1 include:${form.spf_include} ~all` : null;
  const dmarcParts = [`v=DMARC1`, `p=${form.dmarc_policy || "none"}`];
  if (form.dmarc_rua) dmarcParts.push(`rua=mailto:${form.dmarc_rua}`);
  if (form.dmarc_ruf) dmarcParts.push(`ruf=mailto:${form.dmarc_ruf}`);
  const dmarcRecord = dmarcParts.join("; ");
  const submission =
    form.relay_host ? `${form.relay_host}:${form.relay_port || 587} (STARTTLS, AUTH PLAIN/LOGIN)` : null;

  return (
    <>
      <h1>Settings</h1>
      <p className="section-desc">
        Mail parameters for this tenant. These drive the recommended DNS records and the smarthost
        connection instructions shown to senders.
      </p>

      <LoadingError loading={loading} error={error} />

      {!loading && !error && (
        <form className="settings-form" onSubmit={handleSubmit}>
          <fieldset className="settings-group">
            <legend>SPF</legend>
            <div className="field">
              <label htmlFor="spf_include">SPF include endpoint</label>
              <input
                id="spf_include"
                type="text"
                placeholder="spf.example.net"
                value={form.spf_include}
                onChange={(e) => set("spf_include", e.target.value)}
              />
              <span className="field-hint">
                The host senders add via <code>include:</code> in their SPF record.
              </span>
            </div>
          </fieldset>

          <fieldset className="settings-group">
            <legend>DKIM</legend>
            <div className="field">
              <label htmlFor="dkim_selector">Default DKIM selector</label>
              <input
                id="dkim_selector"
                type="text"
                placeholder="mxs"
                value={form.dkim_selector}
                onChange={(e) => set("dkim_selector", e.target.value)}
              />
              <span className="field-hint">
                Used in setup guidance, e.g. <code>{form.dkim_selector || "mxs"}._domainkey.&lt;domain&gt;</code>.
              </span>
            </div>
          </fieldset>

          <fieldset className="settings-group">
            <legend>DMARC defaults</legend>
            <div className="field">
              <label htmlFor="dmarc_policy">Policy</label>
              <select
                id="dmarc_policy"
                value={form.dmarc_policy}
                onChange={(e) => set("dmarc_policy", e.target.value as DMARCPolicy)}
              >
                {DMARC_POLICIES.map((p) => (
                  <option key={p} value={p}>
                    {p}
                  </option>
                ))}
              </select>
            </div>
            <div className="field">
              <label htmlFor="dmarc_rua">Aggregate report address (rua)</label>
              <input
                id="dmarc_rua"
                type="text"
                placeholder="dmarc@example.com"
                value={form.dmarc_rua}
                onChange={(e) => set("dmarc_rua", e.target.value)}
              />
            </div>
            <div className="field">
              <label htmlFor="dmarc_ruf">Forensic report address (ruf)</label>
              <input
                id="dmarc_ruf"
                type="text"
                placeholder="dmarc-forensics@example.com"
                value={form.dmarc_ruf}
                onChange={(e) => set("dmarc_ruf", e.target.value)}
              />
            </div>
          </fieldset>

          <fieldset className="settings-group">
            <legend>Relay / submission host</legend>
            <div className="field">
              <label htmlFor="relay_host">Submission host</label>
              <input
                id="relay_host"
                type="text"
                placeholder="relay.example.com"
                value={form.relay_host}
                onChange={(e) => set("relay_host", e.target.value)}
              />
              <span className="field-hint">Hostname smarthost clients connect to.</span>
            </div>
            <div className="field">
              <label htmlFor="relay_port">Submission port</label>
              <input
                id="relay_port"
                type="number"
                min={1}
                max={65535}
                value={form.relay_port}
                onChange={(e) => set("relay_port", Number(e.target.value))}
              />
            </div>
          </fieldset>

          <fieldset className="settings-group">
            <legend>DNS resolver</legend>
            <div className="field">
              <label htmlFor="resolver_address">Resolver address</label>
              <input
                id="resolver_address"
                type="text"
                placeholder="system default (e.g. 1.1.1.1 or 9.9.9.9:53)"
                value={form.resolver_address}
                onChange={(e) => set("resolver_address", e.target.value)}
              />
              <span className="field-hint">
                DNS server used to validate SPF/DKIM/DMARC. Leave blank to use the host&apos;s
                system resolver. Bare IP/host gets port 53.
              </span>
            </div>
            <div className="field">
              <label htmlFor="resolver_timeout_secs">Lookup timeout (seconds)</label>
              <input
                id="resolver_timeout_secs"
                type="number"
                min={1}
                max={60}
                value={form.resolver_timeout_secs}
                onChange={(e) => set("resolver_timeout_secs", Number(e.target.value))}
              />
            </div>
          </fieldset>

          <div className="settings-actions">
            <button type="submit" className="btn btn-primary" disabled={saving}>
              {saving ? "Saving…" : "Save settings"}
            </button>
            {saved && <span className="save-ok">Saved ✓</span>}
            {saveError && <span className="state-msg error" style={{ padding: 0 }}>{saveError}</span>}
          </div>
        </form>
      )}

      {!loading && !error && (spfRecord || submission) && (
        <div className="settings-preview">
          <h2>Generated guidance</h2>
          {spfRecord && (
            <div className="preview-item">
              <span className="preview-label">SPF (TXT @)</span>
              <code>{spfRecord}</code>
            </div>
          )}
          <div className="preview-item">
            <span className="preview-label">DMARC (TXT _dmarc)</span>
            <code>{dmarcRecord}</code>
          </div>
          {submission && (
            <div className="preview-item">
              <span className="preview-label">Submission endpoint</span>
              <code>{submission}</code>
            </div>
          )}
        </div>
      )}

      <SmarthostSection />
      <ProvidersSection />
      <AbuseTuningSection />
      <MonitoringTuningSection />
      <DeliveryTuningSection />
    </>
  );
}

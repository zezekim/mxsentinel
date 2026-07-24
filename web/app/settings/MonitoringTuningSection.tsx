"use client";

import { useEffect, useState } from "react";
import { getMonitoringTuning, updateMonitoringTuning, type MonitoringTuning } from "@/lib/api";
import LoadingError from "@/components/LoadingError";

const EMPTY: MonitoringTuning = {
  tlsrpt: { interval_secs: 0 },
  mtasts: { interval_secs: 0, cert_warn_days: 0, cert_timeout_secs: 0, http_timeout_secs: 0 },
  probe: {
    interval_secs: 0,
    connect_timeout_secs: 0,
    command_timeout_secs: 0,
    cert_warn_days: 0,
    ehlo_name: "",
    tls_insecure: false,
    check_response: false,
  },
  bimi: { interval_secs: 0, fetch_timeout_secs: 0 },
};

// NumField renders a numeric knob. Blank/0 means "use the daemon default".
function NumField(props: {
  id: string;
  label: string;
  value: number;
  hint?: string;
  onChange: (v: number) => void;
}) {
  return (
    <div className="field">
      <label htmlFor={props.id}>{props.label}</label>
      <input
        id={props.id}
        type="number"
        min={0}
        placeholder="default"
        value={props.value === 0 ? "" : props.value}
        onChange={(e) => props.onChange(e.target.value === "" ? 0 : Number(e.target.value))}
      />
      {props.hint && <span className="field-hint">{props.hint}</span>}
    </div>
  );
}

export default function MonitoringTuningSection() {
  const [form, setForm] = useState<MonitoringTuning>(EMPTY);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);

  useEffect(() => {
    getMonitoringTuning()
      .then((d) => setForm(d.tuning))
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, []);

  function setGroup<G extends keyof MonitoringTuning, K extends keyof MonitoringTuning[G]>(
    group: G,
    key: K,
    value: MonitoringTuning[G][K],
  ) {
    setForm((f) => ({ ...f, [group]: { ...f[group], [key]: value } }));
    setSaved(false);
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setSaving(true);
    setSaveError(null);
    setSaved(false);
    try {
      const d = await updateMonitoringTuning(form);
      setForm(d.tuning);
      setSaved(true);
    } catch (err: unknown) {
      setSaveError(err instanceof Error ? err.message : "Failed to save monitoring tuning");
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="settings-preview" style={{ marginTop: "2rem" }}>
      <h2>Monitoring tuning (advanced)</h2>
      <p className="section-desc">
        Cadence and timeout knobs for the monitoring daemons (tlsrptd, probed, bimid). Leave a
        field blank or 0 to use the daemon default. Dashboard values take precedence over
        environment variables and apply on the next daemon restart.
      </p>

      <LoadingError loading={loading} error={error} />

      {!loading && !error && (
        <form className="settings-form" onSubmit={handleSubmit}>
          <fieldset className="settings-group">
            <legend>TLS-RPT (tlsrptd)</legend>
            <NumField
              id="tlsrpt_interval"
              label="Drop-dir scan interval (seconds)"
              value={form.tlsrpt.interval_secs}
              hint="How often the TLS-RPT report drop directory is scanned. Default 30s."
              onChange={(v) => setGroup("tlsrpt", "interval_secs", v)}
            />
          </fieldset>

          <fieldset className="settings-group">
            <legend>MTA-STS (tlsrptd)</legend>
            <NumField
              id="mtasts_interval"
              label="Re-check interval (seconds)"
              value={form.mtasts.interval_secs}
              hint="MTA-STS / MX-cert re-check cadence. Default 1h (3600s)."
              onChange={(v) => setGroup("mtasts", "interval_secs", v)}
            />
            <NumField
              id="mtasts_cert_warn"
              label="Cert expiry warning (days)"
              value={form.mtasts.cert_warn_days}
              hint="Raise a warning this many days before an MX cert expires. Default 14."
              onChange={(v) => setGroup("mtasts", "cert_warn_days", v)}
            />
            <NumField
              id="mtasts_cert_timeout"
              label="Cert check timeout (seconds)"
              value={form.mtasts.cert_timeout_secs}
              hint="Per-host TLS cert check timeout. Default 10s."
              onChange={(v) => setGroup("mtasts", "cert_timeout_secs", v)}
            />
            <NumField
              id="mtasts_http_timeout"
              label="Policy fetch timeout (seconds)"
              value={form.mtasts.http_timeout_secs}
              hint="MTA-STS policy HTTP GET timeout. Default 10s."
              onChange={(v) => setGroup("mtasts", "http_timeout_secs", v)}
            />
          </fieldset>

          <fieldset className="settings-group">
            <legend>SMTP prober (probed)</legend>
            <NumField
              id="probe_interval"
              label="Probe interval (seconds)"
              value={form.probe.interval_secs}
              hint="How often each endpoint is re-probed. Default 60s."
              onChange={(v) => setGroup("probe", "interval_secs", v)}
            />
            <NumField
              id="probe_connect_timeout"
              label="Connect timeout (seconds)"
              value={form.probe.connect_timeout_secs}
              hint="TCP connect timeout. Default 10s."
              onChange={(v) => setGroup("probe", "connect_timeout_secs", v)}
            />
            <NumField
              id="probe_command_timeout"
              label="Command timeout (seconds)"
              value={form.probe.command_timeout_secs}
              hint="Per-SMTP-command timeout. Default 10s."
              onChange={(v) => setGroup("probe", "command_timeout_secs", v)}
            />
            <NumField
              id="probe_cert_warn"
              label="Cert expiry warning (days)"
              value={form.probe.cert_warn_days}
              hint="Warn this many days before a probed endpoint's cert expires. Default 14."
              onChange={(v) => setGroup("probe", "cert_warn_days", v)}
            />
            <div className="field">
              <label htmlFor="probe_ehlo">EHLO name</label>
              <input
                id="probe_ehlo"
                type="text"
                placeholder="mxsentinel.probe"
                value={form.probe.ehlo_name}
                onChange={(e) => setGroup("probe", "ehlo_name", e.target.value)}
              />
              <span className="field-hint">Name sent in EHLO. Blank uses the default.</span>
            </div>
            <div className="field">
              <label htmlFor="probe_tls_insecure">
                <input
                  id="probe_tls_insecure"
                  type="checkbox"
                  checked={form.probe.tls_insecure}
                  onChange={(e) => setGroup("probe", "tls_insecure", e.target.checked)}
                />{" "}
                Skip TLS chain verification reporting
              </label>
            </div>
            <div className="field">
              <label htmlFor="probe_check_response">
                <input
                  id="probe_check_response"
                  type="checkbox"
                  checked={form.probe.check_response}
                  onChange={(e) => setGroup("probe", "check_response", e.target.checked)}
                />{" "}
                Sample MAIL/RCPT for greylisting behaviour
              </label>
            </div>
          </fieldset>

          <fieldset className="settings-group">
            <legend>BIMI / VMC (bimid)</legend>
            <NumField
              id="bimi_interval"
              label="Poll interval (seconds)"
              value={form.bimi.interval_secs}
              hint="BIMI readiness poll cadence. Default 1h (3600s)."
              onChange={(v) => setGroup("bimi", "interval_secs", v)}
            />
            <NumField
              id="bimi_fetch_timeout"
              label="Logo/VMC fetch timeout (seconds)"
              value={form.bimi.fetch_timeout_secs}
              hint="Per logo/VMC HTTP GET timeout. Default 10s."
              onChange={(v) => setGroup("bimi", "fetch_timeout_secs", v)}
            />
          </fieldset>

          <div className="settings-actions">
            <button type="submit" className="btn btn-primary" disabled={saving}>
              {saving ? "Saving…" : "Save monitoring tuning"}
            </button>
            {saved && <span className="save-ok">Saved ✓</span>}
            {saveError && (
              <span className="state-msg error" style={{ padding: 0 }}>
                {saveError}
              </span>
            )}
          </div>
        </form>
      )}
    </div>
  );
}

"use client";

import { useEffect, useState } from "react";
import { getDeliveryTuning, updateDeliveryTuning, type DeliveryTuning } from "@/lib/api";

const EMPTY: DeliveryTuning = {
  notify: {},
  snds: {},
  seed: {},
  dmarc_pull: {},
  nl: {},
};

/** Numeric knob whose blank value means "use the daemon default" (sent as unset). */
function NumField(props: {
  id: string;
  label: string;
  value: number | undefined;
  onChange: (v: number | undefined) => void;
  hint?: string;
}) {
  return (
    <div className="field">
      <label htmlFor={props.id}>{props.label}</label>
      <input
        id={props.id}
        type="number"
        min={1}
        placeholder="default"
        value={props.value ?? ""}
        onChange={(e) => props.onChange(e.target.value === "" ? undefined : Number(e.target.value))}
      />
      {props.hint && <span className="field-hint">{props.hint}</span>}
    </div>
  );
}

/**
 * Dashboard-managed tuning for the notification / data-pull daemons (previously env-only). All
 * fields are non-secret; a blank field (or 0) means "use the daemon default". Dashboard values
 * win over env; changes take effect when the relevant daemon restarts. See docs/settings-inventory.md.
 */
export default function DeliveryTuningSection() {
  const [form, setForm] = useState<DeliveryTuning>(EMPTY);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);

  useEffect(() => {
    getDeliveryTuning()
      .then((d) => setForm({ ...EMPTY, ...d.tuning }))
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, []);

  function patch<S extends keyof DeliveryTuning>(section: S, key: keyof DeliveryTuning[S], value: unknown) {
    setForm((f) => ({ ...f, [section]: { ...f[section], [key]: value } }));
    setSaved(false);
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setSaving(true);
    setSaveError(null);
    setSaved(false);
    try {
      const d = await updateDeliveryTuning(form);
      setForm({ ...EMPTY, ...d.tuning });
      setSaved(true);
    } catch (err: unknown) {
      setSaveError(err instanceof Error ? err.message : "Failed to save delivery tuning");
    } finally {
      setSaving(false);
    }
  }

  if (loading) return <p className="section-desc">Loading tuning…</p>;
  if (error) return <p className="section-desc" style={{ color: "var(--danger, #c0392b)" }}>{error}</p>;

  return (
    <form onSubmit={handleSubmit} className="settings-form" style={{ marginTop: "2.5rem" }}>
      <fieldset className="settings-group">
        <legend>Delivery &amp; data tuning (advanced)</legend>
        <p className="section-desc">
          Poll/scan intervals, throttle/dedup windows and thresholds for the notification and
          data-pull daemons. Leave a field blank (or 0) to use the daemon default. Values apply
          when the relevant daemon restarts.
        </p>

        <h3 className="subhead">Alert delivery (notifyd)</h3>
        <NumField id="nt_poll" label="Poll interval (secs)" value={form.notify.poll_interval_secs}
          onChange={(v) => patch("notify", "poll_interval_secs", v)}
          hint="How often notifyd scans for firing incidents." />
        <NumField id="nt_throttle" label="Throttle (secs)" value={form.notify.throttle_secs}
          onChange={(v) => patch("notify", "throttle_secs", v)}
          hint="Minimum gap between two sends to the same channel." />
        <NumField id="nt_dedup" label="Dedup window (secs)" value={form.notify.dedup_secs}
          onChange={(v) => patch("notify", "dedup_secs", v)}
          hint="Suppresses a repeated alert to a channel within this window." />
        <NumField id="nt_lookback" label="Lookback (secs)" value={form.notify.lookback_secs}
          onChange={(v) => patch("notify", "lookback_secs", v)}
          hint="How far back each scan looks (auto-raised above the poll interval)." />
        <NumField id="nt_http" label="HTTP timeout (secs)" value={form.notify.http_timeout_secs}
          onChange={(v) => patch("notify", "http_timeout_secs", v)}
          hint="Per-request timeout for webhook/Slack/PagerDuty sends." />
        <div className="field">
          <label htmlFor="nt_dash">Dashboard URL</label>
          <input id="nt_dash" type="text" placeholder="https://sentinel.example.com"
            value={form.notify.dashboard_url ?? ""}
            onChange={(e) => patch("notify", "dashboard_url", e.target.value)} />
          <span className="field-hint">Base URL used to build deep links in notifications.</span>
        </div>

        <h3 className="subhead">Microsoft SNDS / JMRP (sndsd)</h3>
        <NumField id="sn_interval" label="SNDS poll interval (secs)" value={form.snds.interval_secs}
          onChange={(v) => patch("snds", "interval_secs", v)}
          hint="How often the SNDS CSV is fetched (default 6h)." />
        <NumField id="sn_scan" label="JMRP scan interval (secs)" value={form.snds.jmrp_scan_interval_secs}
          onChange={(v) => patch("snds", "jmrp_scan_interval_secs", v)}
          hint="How often the JMRP drop directory is scanned." />
        <NumField id="sn_thresh" label="JMRP complaint threshold" value={form.snds.jmrp_complaint_threshold}
          onChange={(v) => patch("snds", "jmrp_complaint_threshold", v)}
          hint="24h complaint count per sending domain that trips a critical incident." />

        <h3 className="subhead">Inbox-placement seed tests (seedd)</h3>
        <NumField id="se_interval" label="Advance interval (secs)" value={form.seed.interval_secs}
          onChange={(v) => patch("seed", "interval_secs", v)}
          hint="How often seedd advances runs (send probes / poll collectors)." />
        <NumField id="se_window" label="Collect window (secs)" value={form.seed.collect_window_secs}
          onChange={(v) => patch("seed", "collect_window_secs", v)}
          hint="How long a sent probe is polled before it is declared missing." />

        <h3 className="subhead">DMARC pull (dmarcpulld)</h3>
        <NumField id="dp_interval" label="Poll interval (secs)" value={form.dmarc_pull.interval_secs}
          onChange={(v) => patch("dmarc_pull", "interval_secs", v)}
          hint="Seconds between pulls from the DMARC receiver API (default 1h)." />
        <NumField id="dp_lookback" label="Lookback (days)" value={form.dmarc_pull.lookback_days}
          onChange={(v) => patch("dmarc_pull", "lookback_days", v)}
          hint="On first run, how far back to fetch (default 30)." />

        <h3 className="subhead">Natural-language analytics (apid)</h3>
        <NumField id="nl_max" label="Max tools per question" value={form.nl.max_tools}
          onChange={(v) => patch("nl", "max_tools", v)}
          hint="Cap on whitelisted aggregate queries the planner may run per question (default 3)." />

        <div style={{ marginTop: "1rem", display: "flex", gap: "0.75rem", alignItems: "center" }}>
          <button type="submit" disabled={saving}>{saving ? "Saving…" : "Save tuning"}</button>
          {saved && <span className="field-hint" style={{ color: "var(--ok, #2e7d32)" }}>Saved ✓</span>}
          {saveError && <span className="field-hint" style={{ color: "var(--danger, #c0392b)" }}>{saveError}</span>}
        </div>
      </fieldset>
    </form>
  );
}

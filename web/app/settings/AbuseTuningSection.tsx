"use client";

import { useEffect, useState } from "react";
import { getAbuseTuning, updateAbuseTuning, type AbuseTuning } from "@/lib/api";
import LoadingError from "@/components/LoadingError";

// All-zero tuning = "everything on defaults". A blank/0 field tells the daemon to keep its
// env var / built-in default, so the whole group is optional.
const EMPTY: AbuseTuning = {
  authwatch: {
    threshold: 0,
    window_secs: 0,
    cooldown_secs: 0,
    min_volume: 0,
    distinct_rcpt: 0,
    bounce_rate: 0,
    volume_factor: 0,
    volume_floor: 0,
    offhours_start: 0,
    offhours_end: 0,
    offhours_rate: 0,
    offhours_weight: 0,
    autolock: false,
  },
  bounce: {
    interval_secs: 0,
    lookback_secs: 0,
    max_rows: 0,
  },
};

// numVal renders 0 as an empty field so the "leave blank to use default" contract is visible.
function numVal(n: number): number | string {
  return n === 0 ? "" : n;
}

export default function AbuseTuningSection() {
  const [form, setForm] = useState<AbuseTuning>(EMPTY);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);

  useEffect(() => {
    getAbuseTuning()
      .then((d) => setForm(d.tuning_abuse))
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, []);

  function setAuth<K extends keyof AbuseTuning["authwatch"]>(
    key: K,
    value: AbuseTuning["authwatch"][K],
  ) {
    setForm((f) => ({ ...f, authwatch: { ...f.authwatch, [key]: value } }));
    setSaved(false);
  }

  function setBounce<K extends keyof AbuseTuning["bounce"]>(
    key: K,
    value: AbuseTuning["bounce"][K],
  ) {
    setForm((f) => ({ ...f, bounce: { ...f.bounce, [key]: value } }));
    setSaved(false);
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setSaving(true);
    setSaveError(null);
    setSaved(false);
    try {
      const d = await updateAbuseTuning(form);
      setForm(d.tuning_abuse);
      setSaved(true);
    } catch (err: unknown) {
      setSaveError(err instanceof Error ? err.message : "Failed to save tuning");
    } finally {
      setSaving(false);
    }
  }

  return (
    <section style={{ marginTop: "2.5rem" }}>
      <h2>Abuse &amp; bounce tuning (advanced)</h2>
      <p className="section-desc">
        Runtime knobs for the credential-compromise detector (<code>authwatchd</code>) and the
        bounce/suppression worker (<code>bounced</code>). Leave a field blank (0) to keep the
        daemon&apos;s built-in default. These are read once at daemon <strong>startup</strong>, so
        changes apply after the affected daemon restarts.
      </p>

      <LoadingError loading={loading} error={error} />

      {!loading && !error && (
        <form className="settings-form" onSubmit={handleSubmit}>
          <fieldset className="settings-group">
            <legend>authwatchd — credential compromise</legend>
            <div className="field">
              <label htmlFor="aw_threshold">Score threshold</label>
              <input
                id="aw_threshold"
                type="number"
                min={0}
                step="0.1"
                placeholder="default 1.0"
                value={numVal(form.authwatch.threshold)}
                onChange={(e) => setAuth("threshold", Number(e.target.value))}
              />
              <span className="field-hint">Summed-score trip line. Higher = less sensitive.</span>
            </div>
            <div className="field">
              <label htmlFor="aw_window">Window (seconds)</label>
              <input
                id="aw_window"
                type="number"
                min={0}
                placeholder="default 3600"
                value={numVal(form.authwatch.window_secs)}
                onChange={(e) => setAuth("window_secs", Number(e.target.value))}
              />
              <span className="field-hint">Rolling per-credential accounting window.</span>
            </div>
            <div className="field">
              <label htmlFor="aw_cooldown">Cooldown (seconds)</label>
              <input
                id="aw_cooldown"
                type="number"
                min={0}
                placeholder="default 21600"
                value={numVal(form.authwatch.cooldown_secs)}
                onChange={(e) => setAuth("cooldown_secs", Number(e.target.value))}
              />
              <span className="field-hint">Minimum time between trips for one credential.</span>
            </div>
            <div className="field">
              <label htmlFor="aw_min_volume">Min volume</label>
              <input
                id="aw_min_volume"
                type="number"
                min={0}
                placeholder="default 50"
                value={numVal(form.authwatch.min_volume)}
                onChange={(e) => setAuth("min_volume", Number(e.target.value))}
              />
              <span className="field-hint">Messages needed before bounce-rate scoring applies.</span>
            </div>
            <div className="field">
              <label htmlFor="aw_distinct_rcpt">Distinct recipient domains</label>
              <input
                id="aw_distinct_rcpt"
                type="number"
                min={0}
                placeholder="default 50"
                value={numVal(form.authwatch.distinct_rcpt)}
                onChange={(e) => setAuth("distinct_rcpt", Number(e.target.value))}
              />
              <span className="field-hint">List-blasting burst threshold.</span>
            </div>
            <div className="field">
              <label htmlFor="aw_bounce_rate">Bounce rate (0–1)</label>
              <input
                id="aw_bounce_rate"
                type="number"
                min={0}
                max={1}
                step="0.01"
                placeholder="default 0.30"
                value={numVal(form.authwatch.bounce_rate)}
                onChange={(e) => setAuth("bounce_rate", Number(e.target.value))}
              />
              <span className="field-hint">Spam/block bounce fraction that contributes.</span>
            </div>
            <div className="field">
              <label htmlFor="aw_volume_factor">Volume factor</label>
              <input
                id="aw_volume_factor"
                type="number"
                min={0}
                step="0.1"
                placeholder="default 5.0"
                value={numVal(form.authwatch.volume_factor)}
                onChange={(e) => setAuth("volume_factor", Number(e.target.value))}
              />
              <span className="field-hint">Multiple of the credential&apos;s baseline treated as a spike.</span>
            </div>
            <div className="field">
              <label htmlFor="aw_volume_floor">Volume floor</label>
              <input
                id="aw_volume_floor"
                type="number"
                min={0}
                placeholder="default 200"
                value={numVal(form.authwatch.volume_floor)}
                onChange={(e) => setAuth("volume_floor", Number(e.target.value))}
              />
              <span className="field-hint">Minimum volume before spike scoring applies.</span>
            </div>
            <div className="field">
              <label htmlFor="aw_offhours_start">Off-hours start (UTC hour 0–23)</label>
              <input
                id="aw_offhours_start"
                type="number"
                min={0}
                max={23}
                placeholder="default 1"
                value={numVal(form.authwatch.offhours_start)}
                onChange={(e) => setAuth("offhours_start", Number(e.target.value))}
              />
            </div>
            <div className="field">
              <label htmlFor="aw_offhours_end">Off-hours end (UTC hour 0–23)</label>
              <input
                id="aw_offhours_end"
                type="number"
                min={0}
                max={23}
                placeholder="default 5"
                value={numVal(form.authwatch.offhours_end)}
                onChange={(e) => setAuth("offhours_end", Number(e.target.value))}
              />
            </div>
            <div className="field">
              <label htmlFor="aw_offhours_rate">Off-hours rate (0–1)</label>
              <input
                id="aw_offhours_rate"
                type="number"
                min={0}
                max={1}
                step="0.01"
                placeholder="default 0.80"
                value={numVal(form.authwatch.offhours_rate)}
                onChange={(e) => setAuth("offhours_rate", Number(e.target.value))}
              />
              <span className="field-hint">Off-hours concentration that contributes to the score.</span>
            </div>
            <div className="field">
              <label htmlFor="aw_offhours_weight">Off-hours weight</label>
              <input
                id="aw_offhours_weight"
                type="number"
                min={0}
                step="0.1"
                placeholder="default 0.5"
                value={numVal(form.authwatch.offhours_weight)}
                onChange={(e) => setAuth("offhours_weight", Number(e.target.value))}
              />
              <span className="field-hint">Set 0 to disable the off-hours signal.</span>
            </div>
            <div className="field">
              <label htmlFor="aw_autolock" style={{ display: "inline-flex", alignItems: "center", gap: "0.5rem" }}>
                <input
                  id="aw_autolock"
                  type="checkbox"
                  checked={form.authwatch.autolock}
                  onChange={(e) => setAuth("autolock", e.target.checked)}
                />
                Auto-lock credential on trip
              </label>
              <span className="field-hint">
                Opt-in. On a shared relay one credential serves every client, so auto-locking is
                drastic — leave off unless submissions are per-client.
              </span>
            </div>
          </fieldset>

          <fieldset className="settings-group">
            <legend>bounced — bounce classification &amp; suppression</legend>
            <div className="field">
              <label htmlFor="bo_interval">Scan interval (seconds)</label>
              <input
                id="bo_interval"
                type="number"
                min={0}
                placeholder="default 300"
                value={numVal(form.bounce.interval_secs)}
                onChange={(e) => setBounce("interval_secs", Number(e.target.value))}
              />
              <span className="field-hint">How often recent bounces are re-scanned.</span>
            </div>
            <div className="field">
              <label htmlFor="bo_lookback">Lookback window (seconds)</label>
              <input
                id="bo_lookback"
                type="number"
                min={0}
                placeholder="default 172800"
                value={numVal(form.bounce.lookback_secs)}
                onChange={(e) => setBounce("lookback_secs", Number(e.target.value))}
              />
              <span className="field-hint">
                Window of recent bounces re-read each tick; must comfortably exceed the interval.
              </span>
            </div>
            <div className="field">
              <label htmlFor="bo_maxrows">Max rows per tick</label>
              <input
                id="bo_maxrows"
                type="number"
                min={0}
                placeholder="default 100000"
                value={numVal(form.bounce.max_rows)}
                onChange={(e) => setBounce("max_rows", Number(e.target.value))}
              />
              <span className="field-hint">Safety cap on rows pulled per scan.</span>
            </div>
          </fieldset>

          <div className="settings-actions">
            <button type="submit" className="btn btn-primary" disabled={saving}>
              {saving ? "Saving…" : "Save tuning"}
            </button>
            {saved && <span className="save-ok">Saved ✓</span>}
            {saveError && <span className="state-msg error" style={{ padding: 0 }}>{saveError}</span>}
          </div>
        </form>
      )}
    </section>
  );
}

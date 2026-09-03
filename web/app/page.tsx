"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import {
  apiGet,
  getDeliveryOutcomes,
  getSummary,
  getTimeseries,
  listIncidents,
  type DeliveryOutcomes,
  type DomainsResponse,
  type DomainSummary,
  type Granularity,
  type Incident,
  type OverallStatus,
  type ReportProviderRow,
  type SeriesPoint,
  type TenantSummary,
  getRole,
} from "@/lib/api";
import { routeHiddenFor } from "@/components/nav-config";
import Badge from "@/components/Badge";
import LoadingError from "@/components/LoadingError";
import {
  BarList,
  Legend,
  OUTCOME_KEYS,
  OUTCOME_LABEL,
  ProviderBars,
  RateChart,
  TrendChart,
  fmtInt,
  fmtPct,
} from "@/components/charts";

const RANGES = [
  { key: "7d", label: "7 days", days: 7 },
  { key: "30d", label: "30 days", days: 30 },
  { key: "90d", label: "90 days", days: 90 },
] as const;

type RangeKey = (typeof RANGES)[number]["key"];

export default function OverviewPage() {
  const [range, setRange] = useState<RangeKey>("30d");

  const [summary, setSummary] = useState<TenantSummary | null>(null);
  const [outcomes, setOutcomes] = useState<DeliveryOutcomes | null>(null);
  const [points, setPoints] = useState<SeriesPoint[]>([]);
  const [granularity, setGranularity] = useState<Granularity>("day");
  const [domains, setDomains] = useState<DomainSummary[]>([]);
  const [incidents, setIncidents] = useState<Incident[]>([]);

  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [showTable, setShowTable] = useState(false);
  // Viewers cannot open /domains, so the health card links nowhere for them. Starts
  // false so a viewer never sees a live link flash in before the role is read.
  const [canOpenDomains, setCanOpenDomains] = useState(false);

  useEffect(() => {
    const cached = getRole();
    if (cached) setCanOpenDomains(!routeHiddenFor(cached, "/domains"));
  }, []);

  const period = useMemo(() => {
    const days = RANGES.find((r) => r.key === range)?.days ?? 30;
    const until = new Date();
    const since = new Date(until.getTime() - days * 86400000);
    return { since: since.toISOString(), until: until.toISOString() };
  }, [range]);

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    // The four panels are independent; fetch together so one slow call doesn't
    // stagger the page in.
    return Promise.all([
      getSummary(period.since, period.until, 8),
      getTimeseries(period.since, period.until),
      getDeliveryOutcomes(period.since, period.until),
      apiGet<DomainsResponse>("/v1/domains"),
      listIncidents({ status: "open", limit: 6 }),
    ])
      .then(([s, ts, oc, d, inc]) => {
        setSummary(s.summary);
        setPoints(ts.points);
        setGranularity(ts.granularity);
        setOutcomes(oc);
        setDomains(d.domains);
        setIncidents(inc.incidents);
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, [period]);

  useEffect(() => {
    load();
  }, [load]);

  const overall = summary?.overall;
  const rate = overall && overall.total > 0 ? overall.delivered / overall.total : 0;

  const health = useMemo(() => {
    const counts: Record<string, number> = { healthy: 0, warning: 0, critical: 0, unknown: 0 };
    for (const d of domains) counts[d.overall] = (counts[d.overall] ?? 0) + 1;
    return counts;
  }, [domains]);

  // The summary can return the same provider name more than once: the report builder
  // relabels a blank provider as "other", which collides with the literal "other" the
  // classifier already emits. Merge by name so the bar chart shows one row per provider
  // (and so React doesn't get duplicate keys).
  const providers: ReportProviderRow[] = useMemo(() => {
    const merged = new Map<string, ReportProviderRow>();
    for (const p of summary?.providers ?? []) {
      const prev = merged.get(p.provider);
      if (prev) {
        prev.delivered += p.delivered;
        prev.deferred += p.deferred;
        prev.bounced += p.bounced;
        prev.rejected += p.rejected;
        prev.total += p.total;
      } else {
        merged.set(p.provider, { ...p });
      }
    }
    return [...merged.values()]
      .filter((p) => p.total > 0)
      .sort((a, b) => b.total - a.total)
      .slice(0, 8);
  }, [summary]);

  return (
    <>
      <div className="page-header">
        <h1 className="page-title">Overview</h1>
        <div className="range-tabs" role="group" aria-label="Time range">
          {RANGES.map((r) => (
            <button
              key={r.key}
              type="button"
              className={`range-tab${range === r.key ? " is-active" : ""}`}
              aria-pressed={range === r.key}
              onClick={() => setRange(r.key)}
            >
              {r.label}
            </button>
          ))}
        </div>
      </div>

      <LoadingError loading={loading} error={error} />

      {!loading && !error && summary && (
        <>
          {/* Headline figures are per MESSAGE. The event-level numbers (further down)
              count delivery attempts, and retries make those look far worse than the
              share of mail that actually arrives — so the two are never mixed in one
              row, and each says which it is. */}
          <div className="stat-cards">
            <div className="stat-card">
              <div className="label">Messages sent</div>
              <div className="value">{fmtInt(outcomes?.messages ?? 0)}</div>
              <div className="sub">last {RANGES.find((r) => r.key === range)?.label}</div>
            </div>
            <div className="stat-card is-primary">
              <div className="label">Final delivery</div>
              <div className="value">{fmtPct(outcomes?.final_delivery_rate ?? 0)}</div>
              <div className="sub">
                {fmtInt(outcomes?.delivered_final ?? 0)} messages reached the recipient
              </div>
            </div>
            <div className="stat-card">
              <div className="label">First attempt</div>
              <div className="value">{fmtPct(outcomes?.first_attempt_rate ?? 0)}</div>
              <div className="sub">
                {fmtInt(outcomes?.first_attempt_delivered ?? 0)} delivered without a retry
              </div>
            </div>
            <div className="stat-card">
              <div className="label">Permanently failed</div>
              <div className="value">{fmtInt(outcomes?.permanently_failed ?? 0)}</div>
              <div className="sub">
                {fmtPct(outcomes?.permanent_failure_rate ?? 0)} of messages · bounced or rejected
              </div>
            </div>
            <div className="stat-card">
              <div className="label">Still retrying</div>
              <div className="value">{fmtInt(outcomes?.still_retrying ?? 0)}</div>
              <div className="sub">deferred, not yet resolved</div>
            </div>
            <div className="stat-card">
              <div className="label">Domains</div>
              <div className="value">{fmtInt(domains.length)}</div>
              <div className="sub">
                {health.critical > 0
                  ? `${health.critical} critical`
                  : health.warning > 0
                    ? `${health.warning} warning`
                    : "all healthy"}
              </div>
            </div>
          </div>

          {outcomes && outcomes.ever_deferred > 0 && (
            <div className="metric-note">
              <strong>Messages vs delivery attempts.</strong> The charts below count
              delivery <em>attempts</em>, not messages. A deferred message is retried
              until it delivers or hard-fails, so a small number of stuck messages
              produce a large number of events: in this period{" "}
              <strong>{fmtInt(outcomes.ever_deferred)}</strong> messages
              {" "}({fmtPct(outcomes.ever_deferred / Math.max(1, outcomes.messages), 1)} of
              the total) generated{" "}
              <strong>{fmtInt(outcomes.deferral_events)}</strong> deferral events —
              about {Math.round(outcomes.deferral_events / Math.max(1, outcomes.ever_deferred))}{" "}
              retries each. That is why the attempt-level delivered share
              ({fmtPct(rate)}) is far below the {fmtPct(outcomes.final_delivery_rate)} of
              messages that actually got through.
            </div>
          )}

          <div className="card chart-card">
            <div className="card-header">
              <h2>Delivery attempts by outcome</h2>
              <div className="chart-header-tools">
                <Legend keys={OUTCOME_KEYS} />
                <button type="button" className="btn btn-ghost btn-sm" onClick={() => setShowTable((v) => !v)}>
                  {showTable ? "Show chart" : "Show table"}
                </button>
              </div>
            </div>
            <div className="card-body">
              {points.length === 0 ? (
                <p className="no-results">No delivery attempts in this period.</p>
              ) : showTable ? (
                <div className="table-wrap">
                  <table>
                    <thead>
                      <tr>
                        <th>Bucket</th>
                        {OUTCOME_KEYS.map((k) => (
                          <th key={k}>{OUTCOME_LABEL[k]}</th>
                        ))}
                        <th>Total</th>
                        <th>Rate</th>
                      </tr>
                    </thead>
                    <tbody>
                      {points.map((p) => (
                        <tr key={p.bucket}>
                          <td style={{ whiteSpace: "nowrap" }}>{new Date(p.bucket).toLocaleString()}</td>
                          {OUTCOME_KEYS.map((k) => (
                            <td key={k}>{fmtInt(p[k])}</td>
                          ))}
                          <td>{fmtInt(p.total)}</td>
                          <td>{fmtPct(p.delivered_rate)}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              ) : (
                <>
                  <TrendChart points={points} granularity={granularity} />
                  <p className="chart-note">
                    One bar per {granularity === "day" ? "day" : granularity === "hour" ? "hour" : "5 minutes"}, counting
                    delivery <em>attempts</em> — a retried message appears once per attempt.
                    Periods with no traffic are omitted rather than drawn as zero.
                  </p>
                </>
              )}
            </div>
          </div>

          {points.length > 0 && !showTable && (
            <div className="card chart-card">
              <div className="card-header">
                <h2>Attempt success rate</h2>
              </div>
              <div className="card-body">
                <RateChart points={points} granularity={granularity} />
              </div>
            </div>
          )}

          <div className="overview-grid">
            <div className="card">
              <div className="card-header">
                <h2>Attempts by receiving provider</h2>
                <Legend keys={OUTCOME_KEYS} />
              </div>
              <div className="card-body">
                {providers.length === 0 ? (
                  <p className="no-results">No provider data in this period.</p>
                ) : (
                  <ProviderBars rows={providers} />
                )}
              </div>
            </div>

            <div className="card">
              <div className="card-header">
                <h2>Top sending domains</h2>
              </div>
              <div className="card-body">
                {!summary.top_domains?.length ? (
                  <p className="no-results">No sending domains in this period.</p>
                ) : (
                  <BarList
                    rows={summary.top_domains.map((d) => ({
                      label: d.domain,
                      value: d.total,
                      sub: d.total > 0 ? fmtPct(d.delivered / d.total, 0) : undefined,
                    }))}
                  />
                )}
              </div>
            </div>

            <div className="card">
              <div className="card-header">
                <h2>Domain health</h2>
                {canOpenDomains && (
                  <Link href="/domains" className="linklike">All domains →</Link>
                )}
              </div>
              <div className="card-body">
                {domains.length === 0 ? (
                  <p className="no-results">No domains configured yet.</p>
                ) : (
                  <ul className="health-list">
                    {domains.slice(0, 8).map((d) => (
                      <li key={d.id}>
                        {canOpenDomains ? (
                          <Link href={`/domains/${d.id}`}>{d.name}</Link>
                        ) : (
                          <span>{d.name}</span>
                        )}
                        <Badge kind="status" value={d.overall as OverallStatus} />
                      </li>
                    ))}
                  </ul>
                )}
              </div>
            </div>

            <div className="card">
              <div className="card-header">
                <h2>Open incidents</h2>
                <Link href="/incidents" className="linklike">All incidents →</Link>
              </div>
              <div className="card-body">
                {incidents.length === 0 ? (
                  <p className="no-results">No open incidents.</p>
                ) : (
                  <ul className="incident-list">
                    {incidents.map((i) => (
                      <li key={i.id}>
                        <span className={`sev-dot sev-${i.severity}`} aria-hidden="true" />
                        <div>
                          <Link href="/incidents">{i.title}</Link>
                          <div className="incident-meta">
                            {i.domain || "—"} · {new Date(i.created_at).toLocaleString()}
                          </div>
                        </div>
                      </li>
                    ))}
                  </ul>
                )}
              </div>
            </div>
          </div>
        </>
      )}
    </>
  );
}

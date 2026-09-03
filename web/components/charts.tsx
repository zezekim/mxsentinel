"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import type { SeriesPoint, Granularity, ReportProviderRow } from "@/lib/api";

// ─── Palette ──────────────────────────────────────────────────────────────────
//
// These four are chart-mark colours, deliberately NOT the --green/--yellow/--red
// tokens used by badges. Those tokens are tuned for text on a tinted chip; as
// touching fills in a stack they collapse (the badge orange and red sit at ΔE 2.9
// under deuteranopia — indistinguishable). These steps were validated as an
// adjacent set: worst CVD pair ΔE 14.0, worst normal-vision pair ΔE 18.6.
//
// Amber sits below 3:1 against white, which is allowed only because the value is
// always readable another way — every chart here ships direct labels and a table
// view. Keep that relief if you change these.
export const OUTCOME = {
  delivered: "#157f4f",
  deferred: "#d9a406",
  bounced: "#e2622a",
  rejected: "#9b1c1c",
} as const;

export type OutcomeKey = keyof typeof OUTCOME;

// Stack order is severity order: good at the bottom, worst on top.
export const OUTCOME_KEYS: OutcomeKey[] = ["delivered", "deferred", "bounced", "rejected"];

export const OUTCOME_LABEL: Record<OutcomeKey, string> = {
  delivered: "Delivered",
  deferred: "Deferred",
  bounced: "Bounced",
  rejected: "Rejected",
};

// Magnitude bars encode one measure, so they take a single hue rather than one
// colour per row — colouring nominal rows by rank would spend the identity
// channel re-encoding what bar length already shows.
const BAR_HUE = "#005eb8";

const SURFACE = "#ffffff";
const GRID = "#e4e9eb";
const AXIS_TEXT = "#4c6272";

export const fmtInt = (n: number) => n.toLocaleString();

export function fmtPct(n: number, digits = 1) {
  return `${(n * 100).toFixed(digits)}%`;
}

// ─── Sizing ───────────────────────────────────────────────────────────────────

// Charts are drawn at real pixel width so text stays crisp and geometry stays
// honest — scaling a viewBox to fit would stretch the type with it.
function useMeasure() {
  const ref = useRef<HTMLDivElement | null>(null);
  const [width, setWidth] = useState(0);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver((entries) => {
      for (const e of entries) setWidth(e.contentRect.width);
    });
    ro.observe(el);
    setWidth(el.getBoundingClientRect().width);
    return () => ro.disconnect();
  }, []);

  return { ref, width };
}

// ─── Shared chrome ────────────────────────────────────────────────────────────

export function Legend({ keys }: { keys: OutcomeKey[] }) {
  return (
    <ul className="chart-legend">
      {keys.map((k) => (
        <li key={k}>
          <span className="chart-swatch" style={{ background: OUTCOME[k] }} aria-hidden="true" />
          {OUTCOME_LABEL[k]}
        </li>
      ))}
    </ul>
  );
}

function Tooltip({ x, y, width, children }: { x: number; y: number; width: number; children: ReactNode }) {
  // Flip to the left of the cursor near the right edge so the tooltip never leaves
  // the card.
  const flip = x > width - 190;
  return (
    <div
      className="chart-tooltip"
      style={{ left: flip ? x - 186 : x + 14, top: y }}
      role="presentation"
    >
      {children}
    </div>
  );
}

function niceTicks(max: number, count = 4): number[] {
  if (max <= 0) return [0];
  const raw = max / count;
  const mag = Math.pow(10, Math.floor(Math.log10(raw)));
  const norm = raw / mag;
  const step = (norm >= 5 ? 10 : norm >= 2 ? 5 : norm >= 1 ? 2 : 1) * mag;
  // Keep stepping until the top tick is at or above the data max. Stopping at
  // "<= max" leaves the axis short whenever max isn't a clean multiple of the step
  // (30,037 against a 10k step topped out at 30,000), and the tallest column then
  // draws above the plot.
  const out: number[] = [];
  for (let v = 0; ; v += step) {
    out.push(v);
    if (v >= max) break;
  }
  return out;
}

function bucketLabel(iso: string, g: Granularity): string {
  const d = new Date(iso);
  if (g === "day") return d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
  if (g === "hour") return d.toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric" });
  return d.toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" });
}

// ─── Trend: stacked columns ───────────────────────────────────────────────────

// Columns rather than an area chart on purpose. The API omits buckets with no
// traffic, and an area would draw a straight line across a silent day as though
// volume had glided through it. Columns just leave a gap, which is the truth.
export function TrendChart({
  points,
  granularity,
}: {
  points: SeriesPoint[];
  granularity: Granularity;
}) {
  const { ref, width } = useMeasure();
  const [hover, setHover] = useState<number | null>(null);

  const H = 260;
  const PAD = { top: 12, right: 12, bottom: 26, left: 52 };
  const plotW = Math.max(0, width - PAD.left - PAD.right);
  const plotH = H - PAD.top - PAD.bottom;

  const max = points.reduce((m, p) => Math.max(m, p.total), 0);
  const ticks = niceTicks(max);
  const scaleMax = ticks[ticks.length - 1] || 1;
  const y = (v: number) => PAD.top + plotH - (v / scaleMax) * plotH;

  const slot = points.length > 0 ? plotW / points.length : 0;
  const barW = Math.max(1, Math.min(28, slot * 0.72));

  const onMove = useCallback(
    (e: React.MouseEvent<SVGSVGElement>) => {
      if (!points.length || slot <= 0) return;
      const rect = e.currentTarget.getBoundingClientRect();
      const rel = e.clientX - rect.left - PAD.left;
      const i = Math.floor(rel / slot);
      setHover(i >= 0 && i < points.length ? i : null);
    },
    [points.length, slot, PAD.left],
  );

  const active = hover !== null ? points[hover] : null;

  return (
    <div className="chart" ref={ref}>
      {width > 0 && (
        <svg
          width={width}
          height={H}
          onMouseMove={onMove}
          onMouseLeave={() => setHover(null)}
          role="img"
          aria-label={`Message volume by outcome, ${points.length} ${granularity} buckets`}
        >
          {ticks.map((t) => (
            <g key={t}>
              <line x1={PAD.left} x2={width - PAD.right} y1={y(t)} y2={y(t)} stroke={GRID} strokeWidth={1} />
              <text x={PAD.left - 8} y={y(t) + 4} textAnchor="end" fontSize={11} fill={AXIS_TEXT}>
                {t >= 1000 ? `${Math.round(t / 1000)}k` : t}
              </text>
            </g>
          ))}

          {points.map((p, i) => {
            const cx = PAD.left + slot * i + slot / 2;
            const x = cx - barW / 2;
            let acc = 0;
            // Only the topmost non-empty segment gets rounded ends, so the stack
            // reads as one column with a cap rather than four stacked pills.
            const lastNonEmpty = OUTCOME_KEYS.reduce(
              (last, k, idx) => (p[k] > 0 ? idx : last),
              -1,
            );
            return (
              <g key={p.bucket} opacity={hover === null || hover === i ? 1 : 0.55}>
                {OUTCOME_KEYS.map((k, idx) => {
                  const v = p[k];
                  if (v <= 0) return null;
                  const h = (v / scaleMax) * plotH;
                  const yTop = y(acc + v);
                  acc += v;
                  const isTop = idx === lastNonEmpty;
                  return (
                    <rect
                      key={k}
                      x={x}
                      y={yTop}
                      width={barW}
                      // A 2px surface gap keeps touching segments legible. The outer
                      // min() stops the clamp from making a sub-2px segment TALLER
                      // than its own value, which would push the bottom of the stack
                      // through the baseline.
                      height={Math.min(h, Math.max(0.5, h - 2))}
                      rx={isTop ? 2 : 0}
                      fill={OUTCOME[k]}
                    />
                  );
                })}
              </g>
            );
          })}

          {hover !== null && (
            <line
              x1={PAD.left + slot * hover + slot / 2}
              x2={PAD.left + slot * hover + slot / 2}
              y1={PAD.top}
              y2={PAD.top + plotH}
              stroke="#93a1ab"
              strokeWidth={1}
              strokeDasharray="3 3"
              pointerEvents="none"
            />
          )}

          {points.map((p, i) => {
            // Thin the x labels to whatever fits, so they never collide.
            const every = Math.max(1, Math.ceil(points.length / Math.max(2, Math.floor(plotW / 78))));
            if (i % every !== 0) return null;
            return (
              <text
                key={p.bucket}
                x={PAD.left + slot * i + slot / 2}
                y={H - 8}
                textAnchor="middle"
                fontSize={11}
                fill={AXIS_TEXT}
              >
                {bucketLabel(p.bucket, granularity)}
              </text>
            );
          })}
        </svg>
      )}

      {active && hover !== null && (
        <Tooltip x={PAD.left + slot * hover + slot / 2} y={12} width={width}>
          <div className="chart-tooltip-title">{bucketLabel(active.bucket, granularity)}</div>
          {OUTCOME_KEYS.filter((k) => active[k] > 0).map((k) => (
            <div key={k} className="chart-tooltip-row">
              <span className="chart-swatch" style={{ background: OUTCOME[k] }} aria-hidden="true" />
              <span>{OUTCOME_LABEL[k]}</span>
              <b>{fmtInt(active[k])}</b>
            </div>
          ))}
          <div className="chart-tooltip-foot">
            <span>Total</span>
            <b>{fmtInt(active.total)}</b>
          </div>
          <div className="chart-tooltip-foot">
            <span>Delivered</span>
            <b>{fmtPct(active.delivered_rate)}</b>
          </div>
        </Tooltip>
      )}
    </div>
  );
}

// ─── Delivery rate over time ──────────────────────────────────────────────────

// A separate chart, not a second axis on the volume chart. Two measures on two
// y-scales in one frame invite a comparison the geometry does not support.
export function RateChart({ points, granularity }: { points: SeriesPoint[]; granularity: Granularity }) {
  const { ref, width } = useMeasure();
  const [hover, setHover] = useState<number | null>(null);

  const H = 150;
  const PAD = { top: 12, right: 12, bottom: 24, left: 52 };
  const plotW = Math.max(0, width - PAD.left - PAD.right);
  const plotH = H - PAD.top - PAD.bottom;

  const y = (v: number) => PAD.top + plotH - v * plotH;
  const slot = points.length > 0 ? plotW / points.length : 0;
  const px = (i: number) => PAD.left + slot * i + slot / 2;

  // Gaps are real: a bucket the API skipped is unknown, not zero. Break the line
  // wherever the buckets are not consecutive so it never bridges silence.
  const stepMs = granularity === "day" ? 86400000 : granularity === "hour" ? 3600000 : 300000;
  const segments: { i: number; p: SeriesPoint }[][] = [];
  points.forEach((p, i) => {
    const prev = points[i - 1];
    const consecutive =
      prev && new Date(p.bucket).getTime() - new Date(prev.bucket).getTime() <= stepMs * 1.5;
    if (!consecutive) segments.push([]);
    segments[segments.length - 1].push({ i, p });
  });

  const onMove = useCallback(
    (e: React.MouseEvent<SVGSVGElement>) => {
      if (!points.length || slot <= 0) return;
      const rect = e.currentTarget.getBoundingClientRect();
      const i = Math.floor((e.clientX - rect.left - PAD.left) / slot);
      setHover(i >= 0 && i < points.length ? i : null);
    },
    [points.length, slot, PAD.left],
  );

  const active = hover !== null ? points[hover] : null;

  return (
    <div className="chart" ref={ref}>
      {width > 0 && (
        <svg
          width={width}
          height={H}
          onMouseMove={onMove}
          onMouseLeave={() => setHover(null)}
          role="img"
          aria-label="Delivery rate over time"
        >
          {[0, 0.25, 0.5, 0.75, 1].map((t) => (
            <g key={t}>
              <line x1={PAD.left} x2={width - PAD.right} y1={y(t)} y2={y(t)} stroke={GRID} strokeWidth={1} />
              <text x={PAD.left - 8} y={y(t) + 4} textAnchor="end" fontSize={11} fill={AXIS_TEXT}>
                {Math.round(t * 100)}%
              </text>
            </g>
          ))}

          {segments.filter((s) => s.length > 0).map((seg, si) =>
            seg.length === 1 ? (
              <circle key={si} cx={px(seg[0].i)} cy={y(seg[0].p.delivered_rate)} r={2.5} fill={OUTCOME.delivered} />
            ) : (
              <path
                key={si}
                d={seg.map((s, k) => `${k === 0 ? "M" : "L"}${px(s.i)},${y(s.p.delivered_rate)}`).join(" ")}
                fill="none"
                stroke={OUTCOME.delivered}
                strokeWidth={2}
                strokeLinejoin="round"
                strokeLinecap="round"
              />
            ),
          )}

          {hover !== null && active && (
            <>
              <line
                x1={px(hover)}
                x2={px(hover)}
                y1={PAD.top}
                y2={PAD.top + plotH}
                stroke="#93a1ab"
                strokeWidth={1}
                strokeDasharray="3 3"
              />
              {/* 2px surface ring keeps the marker readable on top of the line */}
              <circle cx={px(hover)} cy={y(active.delivered_rate)} r={5} fill={OUTCOME.delivered} stroke={SURFACE} strokeWidth={2} />
            </>
          )}
        </svg>
      )}

      {active && hover !== null && (
        <Tooltip x={px(hover)} y={8} width={width}>
          <div className="chart-tooltip-title">{bucketLabel(active.bucket, granularity)}</div>
          <div className="chart-tooltip-foot">
            <span>Delivery rate</span>
            <b>{fmtPct(active.delivered_rate)}</b>
          </div>
          <div className="chart-tooltip-foot">
            <span>Of</span>
            <b>{fmtInt(active.total)} msgs</b>
          </div>
        </Tooltip>
      )}
    </div>
  );
}

// ─── Provider breakdown: horizontal stacked bars ──────────────────────────────

// Horizontal because provider names are long and read better as row labels than
// as rotated tick text.
export function ProviderBars({ rows }: { rows: ReportProviderRow[] }) {
  const max = rows.reduce((m, r) => Math.max(m, r.total), 0) || 1;

  return (
    <div className="provider-bars">
      {rows.map((r) => {
        const rate = r.total > 0 ? r.delivered / r.total : 0;
        return (
          <div key={r.provider} className="provider-row">
            <div className="provider-name" title={r.provider}>{r.provider}</div>
            <div className="provider-track">
              <div className="provider-stack" style={{ width: `${(r.total / max) * 100}%` }}>
                {OUTCOME_KEYS.map((k) => {
                  const v = r[k];
                  if (v <= 0) return null;
                  return (
                    <span
                      key={k}
                      className="provider-seg"
                      style={{ width: `${(v / r.total) * 100}%`, background: OUTCOME[k] }}
                      title={`${OUTCOME_LABEL[k]}: ${fmtInt(v)}`}
                    />
                  );
                })}
              </div>
            </div>
            <div className="provider-figs">
              <b>{fmtPct(rate, 1)}</b>
              <span>{fmtInt(r.total)}</span>
            </div>
          </div>
        );
      })}
    </div>
  );
}

// ─── Magnitude bars (top sending domains) ─────────────────────────────────────

export function BarList({
  rows,
}: {
  rows: { label: string; value: number; sub?: string; href?: string }[];
}) {
  const max = rows.reduce((m, r) => Math.max(m, r.value), 0) || 1;
  return (
    <div className="bar-list">
      {rows.map((r) => (
        <div key={r.label} className="bar-row">
          <div className="bar-label" title={r.label}>
            {r.href ? <a href={r.href}>{r.label}</a> : r.label}
          </div>
          <div className="bar-track">
            <span className="bar-fill" style={{ width: `${(r.value / max) * 100}%`, background: BAR_HUE }} />
          </div>
          <div className="bar-figs">
            <b>{fmtInt(r.value)}</b>
            {r.sub && <span>{r.sub}</span>}
          </div>
        </div>
      ))}
    </div>
  );
}

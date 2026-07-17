"use client";

import { useEffect, useState } from "react";
import { apiGet } from "@/lib/api";

interface TrendPoint {
  date: string;
  total: number;
  pass: number;
  fail: number;
  pass_rate: number;
}

interface TrendResponse {
  points: TrendPoint[];
}

interface Props {
  since?: string;
  until?: string;
  domain?: string;
}

function buildQuery(props: Props): string {
  const params = new URLSearchParams();
  if (props.since) params.set("since", props.since);
  if (props.until) params.set("until", props.until);
  if (props.domain) params.set("domain", props.domain);
  const qs = params.toString();
  return qs ? `?${qs}` : "";
}

function fmtDate(d: string): string {
  if (!d) return "";
  try {
    return new Date(`${d}T00:00:00Z`).toLocaleDateString(undefined, {
      month: "short",
      day: "numeric",
    });
  } catch {
    return d;
  }
}

const VB_W = 600;
const VB_H = 160;
const PAD_L = 8;
const PAD_R = 8;
const PAD_T = 12;
const PAD_B = 22;
const GREEN = "#16a34a";

export default function TrendChart({ since, until, domain }: Props) {
  const [points, setPoints] = useState<TrendPoint[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let active = true;
    setLoading(true);
    setError(null);
    apiGet<TrendResponse>(`/v1/dmarc/trend${buildQuery({ since, until, domain })}`)
      .then((d) => {
        if (!active) return;
        setPoints(d.points ?? []);
      })
      .catch((e: unknown) => {
        if (!active) return;
        setError(e instanceof Error ? e.message : String(e));
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [since, until, domain]);

  if (loading) {
    return <p className="no-results">Loading trend…</p>;
  }

  if (error) {
    return (
      <p className="no-results" style={{ color: "#991b1b" }}>
        Failed to load trend: {error}
      </p>
    );
  }

  if (!points || points.length === 0) {
    return <p className="no-results">No data for this range.</p>;
  }

  const plotW = VB_W - PAD_L - PAD_R;
  const plotH = VB_H - PAD_T - PAD_B;
  const totalMessages = points.reduce((acc, p) => acc + p.total, 0);

  // X positions: evenly spaced across points (single point sits at left edge).
  const xAt = (i: number): number => {
    if (points.length === 1) return PAD_L;
    return PAD_L + (i / (points.length - 1)) * plotW;
  };
  // Y positions: pass_rate 0..1, 1 (100%) at top, 0 at bottom.
  const yAt = (rate: number): number => {
    const clamped = Math.max(0, Math.min(1, rate));
    return PAD_T + (1 - clamped) * plotH;
  };

  const linePts = points.map((p, i) => `${xAt(i)},${yAt(p.pass_rate)}`);
  const linePath = `M ${linePts.join(" L ")}`;
  const areaPath =
    points.length === 1
      ? ""
      : `M ${xAt(0)},${PAD_T + plotH} L ${linePts.join(" L ")} L ${xAt(
          points.length - 1
        )},${PAD_T + plotH} Z`;

  const gridLines = [
    { rate: 1, label: "100%" },
    { rate: 0.5, label: "50%" },
    { rate: 0, label: "0%" },
  ];

  return (
    <div>
      <svg
        viewBox={`0 0 ${VB_W} ${VB_H}`}
        preserveAspectRatio="none"
        style={{ width: "100%", height: 160, display: "block" }}
        role="img"
        aria-label="DMARC pass rate over time"
      >
        {gridLines.map((g) => {
          const y = yAt(g.rate);
          return (
            <g key={g.label}>
              <line
                x1={PAD_L}
                y1={y}
                x2={VB_W - PAD_R}
                y2={y}
                stroke="#e5e7eb"
                strokeWidth={1}
              />
              <text
                x={VB_W - PAD_R}
                y={y - 2}
                fontSize={9}
                fill="#9ca3af"
                textAnchor="end"
              >
                {g.label}
              </text>
            </g>
          );
        })}

        {areaPath && <path d={areaPath} fill={GREEN} fillOpacity={0.12} />}

        <path
          d={linePath}
          fill="none"
          stroke={GREEN}
          strokeWidth={2}
          strokeLinejoin="round"
          strokeLinecap="round"
          vectorEffect="non-scaling-stroke"
        />

        {points.length === 1 && (
          <circle cx={xAt(0)} cy={yAt(points[0].pass_rate)} r={3} fill={GREEN} />
        )}

        <text
          x={PAD_L}
          y={VB_H - 6}
          fontSize={9}
          fill="#6b7280"
          textAnchor="start"
        >
          {fmtDate(points[0].date)}
        </text>
        {points.length > 1 && (
          <text
            x={VB_W - PAD_R}
            y={VB_H - 6}
            fontSize={9}
            fill="#6b7280"
            textAnchor="end"
          >
            {fmtDate(points[points.length - 1].date)}
          </text>
        )}
      </svg>
      <p
        className="no-results"
        style={{ marginTop: 4, fontSize: 12, color: "#6b7280" }}
      >
        {totalMessages.toLocaleString()} message
        {totalMessages === 1 ? "" : "s"} in range
      </p>
    </div>
  );
}

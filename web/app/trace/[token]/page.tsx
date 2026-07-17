"use client";

import React, { useEffect, useState } from "react";
import { useParams } from "next/navigation";
import { getPublicTrace, type PublicTrace, type TraceEvent } from "@/lib/api";

// ── Presentation maps ──────────────────────────────────────────────────────────

// Overall message status → headline styling. Keyed on the latest event type.
const statusConfig: Record<
  string,
  { color: string; bg: string; border: string; label: string; icon: string }
> = {
  delivered: { color: "#16a34a", bg: "#f0fdf4", border: "#bbf7d0", label: "Delivered", icon: "✓" },
  received:  { color: "#2563eb", bg: "#eff6ff", border: "#bfdbfe", label: "Accepted — in transit", icon: "→" },
  deferred:  { color: "#ca8a04", bg: "#fefce8", border: "#fde68a", label: "Deferred — retrying", icon: "⧗" },
  bounced:   { color: "#dc2626", bg: "#fef2f2", border: "#fecaca", label: "Bounced", icon: "✕" },
  rejected:  { color: "#dc2626", bg: "#fef2f2", border: "#fecaca", label: "Rejected", icon: "✕" },
  unknown:   { color: "#6b7280", bg: "#f9fafb", border: "#e5e7eb", label: "Unknown", icon: "?" },
};

// Per-event dot color in the timeline.
const eventColors: Record<string, string> = {
  received: "#2563eb",
  delivered: "#16a34a",
  deferred: "#ca8a04",
  bounced: "#dc2626",
  rejected: "#dc2626",
};

function statusFor(s: string) {
  return statusConfig[s] ?? statusConfig.unknown;
}

function fmt(ts: string): string {
  try {
    return new Date(ts).toLocaleString();
  } catch {
    return ts;
  }
}

// ── Page ────────────────────────────────────────────────────────────────────────

const container: React.CSSProperties = {
  minHeight: "100vh",
  backgroundColor: "#f8fafc",
  fontFamily: "-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif",
  color: "#1e293b",
  padding: "40px 16px",
};
const inner: React.CSSProperties = { maxWidth: 720, margin: "0 auto" };

export default function TracePage() {
  const params = useParams();
  const token = params?.token as string;

  const [data, setData] = useState<PublicTrace | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!token) return;
    getPublicTrace(token)
      .then(setData)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, [token]);

  if (loading) {
    return (
      <div style={container}>
        <div style={inner}>
          <div style={{ textAlign: "center", padding: "80px 0", color: "#64748b" }}>
            Loading message status…
          </div>
        </div>
      </div>
    );
  }

  if (error) {
    return (
      <div style={container}>
        <div style={inner}>
          <Brand />
          <div
            style={{
              textAlign: "center",
              padding: "64px 24px",
              background: "#fff",
              border: "1px solid #e5e7eb",
              borderRadius: 12,
            }}
          >
            <div style={{ fontSize: 40, marginBottom: 12 }}>🔍</div>
            <div style={{ fontSize: 20, fontWeight: 600, marginBottom: 8 }}>
              Message status unavailable
            </div>
            <div style={{ color: "#64748b", fontSize: 14, maxWidth: 440, margin: "0 auto" }}>
              {error}. This link may be invalid, revoked, expired, or the message may have aged
              out of retention.
            </div>
          </div>
        </div>
      </div>
    );
  }

  if (!data) return null;

  const sc = statusFor(data.status);

  return (
    <div style={container}>
      <div style={inner}>
        <Brand />

        {/* Headline status */}
        <div
          style={{
            backgroundColor: sc.bg,
            border: `1px solid ${sc.border}`,
            borderRadius: 12,
            padding: "28px 28px",
            marginBottom: 24,
            display: "flex",
            alignItems: "center",
            gap: 20,
          }}
        >
          <div
            style={{
              width: 52,
              height: 52,
              borderRadius: "50%",
              backgroundColor: sc.color,
              color: "#fff",
              display: "flex",
              alignItems: "center",
              justifyContent: "center",
              fontSize: 26,
              flexShrink: 0,
              boxShadow: `0 0 0 6px ${sc.border}`,
            }}
          >
            {sc.icon}
          </div>
          <div>
            <div style={{ fontSize: 24, fontWeight: 700, color: sc.color, lineHeight: 1.2 }}>
              {sc.label}
            </div>
            {data.label && (
              <div style={{ fontSize: 14, color: "#475569", marginTop: 4 }}>{data.label}</div>
            )}
          </div>
        </div>

        {/* Message summary */}
        <div
          style={{
            background: "#fff",
            border: "1px solid #e5e7eb",
            borderRadius: 12,
            padding: "18px 22px",
            marginBottom: 24,
            display: "grid",
            gridTemplateColumns: "repeat(auto-fit, minmax(180px, 1fr))",
            gap: "14px 24px",
          }}
        >
          <Field label="From domain" value={data.from_domain} />
          <Field label="Recipient domain" value={data.recipient_domain} />
          <Field label="Provider" value={data.provider} />
          <Field label="Message-ID" value={data.message_id} mono />
        </div>

        {/* Timeline */}
        <div style={{ fontSize: 12, fontWeight: 600, textTransform: "uppercase", letterSpacing: "0.06em", color: "#64748b", marginBottom: 12 }}>
          Delivery timeline
        </div>
        <div style={{ background: "#fff", border: "1px solid #e5e7eb", borderRadius: 12, padding: "8px 0" }}>
          {data.events.map((e, i) => (
            <TimelineRow key={i} e={e} last={i === data.events.length - 1} />
          ))}
        </div>

        <div style={{ textAlign: "center", marginTop: 28, fontSize: 12, color: "#94a3b8" }}>
          Checked {fmt(data.checked_at)} · Powered by MX Sentinel
        </div>
      </div>
    </div>
  );
}

function Brand() {
  return (
    <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 24, color: "#334155" }}>
      <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
        <path d="M12 2L2 7l10 5 10-5-10-5z" />
        <path d="M2 17l10 5 10-5" />
        <path d="M2 12l10 5 10-5" />
      </svg>
      <span style={{ fontWeight: 700, fontSize: 15 }}>MX Sentinel</span>
      <span style={{ fontSize: 13, color: "#94a3b8" }}>· Message tracking</span>
    </div>
  );
}

function Field({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div>
      <div style={{ fontSize: 11, color: "#94a3b8", textTransform: "uppercase", letterSpacing: "0.04em", marginBottom: 3 }}>
        {label}
      </div>
      <div
        style={{
          fontSize: mono ? 12 : 14,
          fontWeight: 500,
          color: "#1e293b",
          wordBreak: "break-all",
          fontFamily: mono ? "ui-monospace, SFMono-Regular, Menlo, monospace" : undefined,
        }}
      >
        {value || "—"}
      </div>
    </div>
  );
}

function TimelineRow({ e, last }: { e: TraceEvent; last: boolean }) {
  const color = eventColors[e.event_type] ?? "#6b7280";
  const isFailure = e.event_type === "bounced" || e.event_type === "rejected" || e.event_type === "deferred";
  return (
    <div style={{ display: "flex", gap: 14, padding: "12px 22px", position: "relative" }}>
      {/* dot + connector */}
      <div style={{ display: "flex", flexDirection: "column", alignItems: "center", flexShrink: 0 }}>
        <div style={{ width: 12, height: 12, borderRadius: "50%", backgroundColor: color, marginTop: 4 }} />
        {!last && <div style={{ width: 2, flex: 1, backgroundColor: "#e5e7eb", marginTop: 4 }} />}
      </div>
      <div style={{ flex: 1, paddingBottom: last ? 0 : 6 }}>
        <div style={{ display: "flex", justifyContent: "space-between", gap: 12, flexWrap: "wrap" }}>
          <span style={{ fontWeight: 600, fontSize: 14, color, textTransform: "capitalize" }}>
            {e.event_type}
            {e.mx_host && (
              <span style={{ fontWeight: 400, color: "#64748b" }}> · {e.mx_host}</span>
            )}
          </span>
          <span style={{ fontSize: 12, color: "#94a3b8", whiteSpace: "nowrap" }}>{fmt(e.event_time)}</span>
        </div>
        {(e.smtp_code > 0 || e.enhanced_status) && (
          <div style={{ fontSize: 12, color: "#64748b", marginTop: 3 }}>
            {e.smtp_code > 0 && <span style={{ fontFamily: "ui-monospace, monospace" }}>{e.smtp_code}</span>}
            {e.enhanced_status && <span style={{ marginLeft: 6 }}>{e.enhanced_status}</span>}
            {e.bounce_class && e.bounce_class !== "none" && (
              <span style={{ marginLeft: 6, color: isFailure ? "#b91c1c" : "#64748b", fontWeight: 600 }}>
                [{e.bounce_class}]
              </span>
            )}
          </div>
        )}
        {e.response_text && (
          <div
            style={{
              fontSize: 12.5,
              color: isFailure ? "#7f1d1d" : "#475569",
              marginTop: 6,
              padding: "8px 10px",
              background: isFailure ? "#fef2f2" : "#f8fafc",
              border: `1px solid ${isFailure ? "#fecaca" : "#e5e7eb"}`,
              borderRadius: 6,
              fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
              wordBreak: "break-word",
            }}
          >
            {e.response_text}
          </div>
        )}
      </div>
    </div>
  );
}

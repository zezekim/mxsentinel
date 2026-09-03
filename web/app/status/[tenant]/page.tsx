"use client";

import React, { useState, useEffect } from "react";
import { useParams } from "next/navigation";
import { API_BASE } from "@/lib/api-base";

interface TenantInfo {
  name: string;
  slug: string;
}

interface DomainStatus {
  name: string;
  overall: "healthy" | "warning" | "critical" | "unknown";
}

interface Incident {
  kind: string;
  severity: string;
  title: string;
  domain: string;
  created_at: string;
}

interface StatusResponse {
  tenant: TenantInfo;
  status: "operational" | "degraded" | "down";
  domains: DomainStatus[];
  open_incidents: Incident[];
  checked_at: string;
}

function timeAgo(dateStr: string): string {
  const date = new Date(dateStr);
  const now = new Date();
  const diff = Math.floor((now.getTime() - date.getTime()) / 1000);
  if (diff < 60) return `${diff}s ago`;
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
  return `${Math.floor(diff / 86400)}d ago`;
}

function formatCheckedAt(dateStr: string): string {
  try {
    return new Date(dateStr).toLocaleString();
  } catch {
    return dateStr;
  }
}

const statusConfig = {
  operational: {
    color: "#16a34a",
    bg: "#f0fdf4",
    border: "#bbf7d0",
    label: "All Systems Operational",
  },
  degraded: {
    color: "#ca8a04",
    bg: "#fefce8",
    border: "#fde68a",
    label: "Degraded Performance",
  },
  down: {
    color: "#dc2626",
    bg: "#fef2f2",
    border: "#fecaca",
    label: "Service Disruption",
  },
};

const overallColors: Record<string, string> = {
  healthy: "#16a34a",
  warning: "#ca8a04",
  critical: "#dc2626",
  unknown: "#9ca3af",
};

const severityColors: Record<string, { bg: string; text: string }> = {
  critical: { bg: "#fee2e2", text: "#991b1b" },
  high: { bg: "#ffedd5", text: "#9a3412" },
  medium: { bg: "#fef9c3", text: "#854d0e" },
  low: { bg: "#eff6ff", text: "#1e40af" },
  info: { bg: "#f1f5f9", text: "#475569" },
};

export default function StatusPage() {
  const params = useParams();
  const slug = params?.tenant as string;

  const [data, setData] = useState<StatusResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [notFound, setNotFound] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!slug) return;
    const url = `${API_BASE}/v1/status/${slug}`;

    fetch(url)
      .then(async (res) => {
        if (res.status === 404) {
          setNotFound(true);
          return;
        }
        if (!res.ok) {
          throw new Error(`HTTP ${res.status}`);
        }
        const json = await res.json();
        setData(json);
      })
      .catch((err) => {
        setError(err.message ?? "Failed to load status");
      })
      .finally(() => setLoading(false));
  }, [slug]);

  const containerStyle: React.CSSProperties = {
    minHeight: "100vh",
    backgroundColor: "#f8fafc",
    fontFamily:
      "-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif",
    color: "#1e293b",
    padding: "40px 16px",
  };

  const innerStyle: React.CSSProperties = {
    maxWidth: 800,
    margin: "0 auto",
  };

  if (loading) {
    return (
      <div style={containerStyle}>
        <div style={innerStyle}>
          <div
            style={{
              textAlign: "center",
              padding: "80px 0",
              color: "#64748b",
              fontSize: 16,
            }}
          >
            Loading status...
          </div>
        </div>
      </div>
    );
  }

  if (notFound) {
    return (
      <div style={containerStyle}>
        <div style={innerStyle}>
          <div
            style={{
              textAlign: "center",
              padding: "80px 0",
            }}
          >
            <div style={{ fontSize: 48, marginBottom: 16 }}>404</div>
            <div
              style={{ fontSize: 22, fontWeight: 600, marginBottom: 8 }}
            >
              Tenant not found
            </div>
            <div style={{ color: "#64748b", fontSize: 15 }}>
              No status page found for &ldquo;{slug}&rdquo;.
            </div>
          </div>
        </div>
      </div>
    );
  }

  if (error) {
    return (
      <div style={containerStyle}>
        <div style={innerStyle}>
          <div
            style={{
              textAlign: "center",
              padding: "80px 0",
              color: "#dc2626",
            }}
          >
            <div style={{ fontSize: 20, fontWeight: 600, marginBottom: 8 }}>
              Unable to load status
            </div>
            <div style={{ fontSize: 14, color: "#64748b" }}>{error}</div>
          </div>
        </div>
      </div>
    );
  }

  if (!data) return null;

  const sc = statusConfig[data.status] ?? statusConfig.operational;
  const hasIncidents =
    Array.isArray(data.open_incidents) && data.open_incidents.length > 0;

  return (
    <div style={containerStyle}>
      <div style={innerStyle}>
        {/* Status header */}
        <div
          style={{
            backgroundColor: sc.bg,
            border: `1px solid ${sc.border}`,
            borderRadius: 12,
            padding: "36px 32px",
            marginBottom: 28,
            display: "flex",
            alignItems: "center",
            gap: 24,
          }}
        >
          {/* Status circle */}
          <div
            style={{
              width: 56,
              height: 56,
              borderRadius: "50%",
              backgroundColor: sc.color,
              flexShrink: 0,
              boxShadow: `0 0 0 8px ${sc.border}`,
            }}
          />
          <div>
            <div
              style={{
                fontSize: 26,
                fontWeight: 700,
                color: sc.color,
                lineHeight: 1.2,
                marginBottom: 6,
              }}
            >
              {sc.label}
            </div>
            <div style={{ fontSize: 15, color: "#475569" }}>
              Email infrastructure for{" "}
              <span style={{ fontWeight: 600, color: "#1e293b" }}>
                {data.tenant.name}
              </span>
            </div>
          </div>
        </div>

        {/* Domain health grid */}
        {Array.isArray(data.domains) && data.domains.length > 0 && (
          <div style={{ marginBottom: 28 }}>
            <div
              style={{
                fontSize: 13,
                fontWeight: 600,
                textTransform: "uppercase",
                letterSpacing: "0.06em",
                color: "#64748b",
                marginBottom: 12,
              }}
            >
              Domain Health
            </div>
            <div
              style={{
                display: "grid",
                gridTemplateColumns: "repeat(auto-fill, minmax(220px, 1fr))",
                gap: 10,
              }}
            >
              {data.domains.map((domain) => {
                const dotColor =
                  overallColors[domain.overall] ?? overallColors.unknown;
                return (
                  <div
                    key={domain.name}
                    style={{
                      backgroundColor: "#ffffff",
                      border: "1px solid #e2e8f0",
                      borderRadius: 8,
                      padding: "14px 16px",
                      display: "flex",
                      alignItems: "center",
                      gap: 12,
                    }}
                  >
                    <div
                      style={{
                        width: 10,
                        height: 10,
                        borderRadius: "50%",
                        backgroundColor: dotColor,
                        flexShrink: 0,
                      }}
                    />
                    <span
                      style={{
                        fontSize: 14,
                        fontWeight: 500,
                        color: "#1e293b",
                        overflow: "hidden",
                        textOverflow: "ellipsis",
                        whiteSpace: "nowrap",
                      }}
                    >
                      {domain.name}
                    </span>
                  </div>
                );
              })}
            </div>
          </div>
        )}

        {/* Open incidents */}
        {hasIncidents && (
          <div style={{ marginBottom: 28 }}>
            <div
              style={{
                fontSize: 13,
                fontWeight: 600,
                textTransform: "uppercase",
                letterSpacing: "0.06em",
                color: "#64748b",
                marginBottom: 12,
              }}
            >
              Open Incidents
            </div>
            <div
              style={{
                backgroundColor: "#ffffff",
                border: "1px solid #e2e8f0",
                borderRadius: 8,
                overflow: "hidden",
              }}
            >
              <table
                style={{
                  width: "100%",
                  borderCollapse: "collapse",
                  fontSize: 14,
                }}
              >
                <thead>
                  <tr
                    style={{
                      backgroundColor: "#f8fafc",
                      borderBottom: "1px solid #e2e8f0",
                    }}
                  >
                    {["Kind", "Title", "Domain", "Severity", "Started"].map(
                      (h) => (
                        <th
                          key={h}
                          style={{
                            padding: "10px 14px",
                            textAlign: "left",
                            fontWeight: 600,
                            color: "#475569",
                            fontSize: 12,
                            textTransform: "uppercase",
                            letterSpacing: "0.04em",
                          }}
                        >
                          {h}
                        </th>
                      )
                    )}
                  </tr>
                </thead>
                <tbody>
                  {data.open_incidents.map((inc, i) => {
                    const sevStyle =
                      severityColors[inc.severity?.toLowerCase()] ??
                      severityColors.info;
                    return (
                      <tr
                        key={i}
                        style={{
                          borderBottom:
                            i < data.open_incidents.length - 1
                              ? "1px solid #f1f5f9"
                              : "none",
                        }}
                      >
                        <td
                          style={{
                            padding: "12px 14px",
                            color: "#64748b",
                            fontFamily: "monospace",
                            fontSize: 12,
                            whiteSpace: "nowrap",
                          }}
                        >
                          {inc.kind}
                        </td>
                        <td
                          style={{
                            padding: "12px 14px",
                            color: "#1e293b",
                            fontWeight: 500,
                          }}
                        >
                          {inc.title}
                        </td>
                        <td
                          style={{
                            padding: "12px 14px",
                            color: "#475569",
                            fontSize: 13,
                          }}
                        >
                          {inc.domain || "—"}
                        </td>
                        <td style={{ padding: "12px 14px" }}>
                          <span
                            style={{
                              display: "inline-block",
                              padding: "2px 8px",
                              borderRadius: 4,
                              fontSize: 12,
                              fontWeight: 600,
                              backgroundColor: sevStyle.bg,
                              color: sevStyle.text,
                              textTransform: "capitalize",
                            }}
                          >
                            {inc.severity}
                          </span>
                        </td>
                        <td
                          style={{
                            padding: "12px 14px",
                            color: "#94a3b8",
                            fontSize: 13,
                            whiteSpace: "nowrap",
                          }}
                        >
                          {timeAgo(inc.created_at)}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          </div>
        )}

        {/* Footer */}
        <div
          style={{
            textAlign: "center",
            color: "#94a3b8",
            fontSize: 13,
            paddingTop: 8,
          }}
        >
          Powered by{" "}
          <span style={{ fontWeight: 600, color: "#64748b" }}>MX Sentinel</span>{" "}
          &bull; Updated {formatCheckedAt(data.checked_at)}
        </div>
      </div>
    </div>
  );
}

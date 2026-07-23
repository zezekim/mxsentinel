"use client";

import { useCallback, useEffect, useState } from "react";
import LoadingError from "@/components/LoadingError";
import {
  getSMTPProbes,
  getSMTPProbeHistory,
  type ProbeStatusResponse,
  type ProbeEndpointStatus,
  type ProbeUptime,
} from "@/lib/api-smtpprobes";

function fmt(ts: string | null | undefined): string {
  if (!ts) return "—";
  return new Date(ts).toLocaleString();
}

function endpointLabel(e: ProbeEndpointStatus): string {
  return `${e.endpoint.host}:${e.endpoint.port}`;
}

function StatusBadge({ e }: { e: ProbeEndpointStatus }) {
  if (!e.probed) return <span className="badge badge-unknown">unprobed</span>;
  if (e.ok) return <span className="badge badge-ok">healthy</span>;
  return <span className="badge badge-critical">failing</span>;
}

function CertBadge({ e }: { e: ProbeEndpointStatus }) {
  if (!e.tls || !e.tls.negotiated) {
    return <span className="badge badge-unknown">no TLS</span>;
  }
  const days = e.tls.days_until_expiry;
  if (e.tls.expiring) {
    const cls = days != null && days <= 7 ? "badge-critical" : "badge-warning";
    const label = days != null && days < 0 ? "cert EXPIRED" : `cert ${days ?? "?"}d`;
    return (
      <span className={`badge ${cls}`} title={e.tls.cert_subject || ""}>
        {label}
      </span>
    );
  }
  return (
    <span className="badge badge-ok" title={e.tls.cert_subject || ""}>
      {days != null ? `${days}d` : "valid"}
    </span>
  );
}

export default function SMTPProbesPage() {
  const [data, setData] = useState<ProbeStatusResponse | null>(null);
  const [uptime, setUptime] = useState<ProbeUptime[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const fetchData = useCallback(() => {
    setLoading(true);
    setError(null);
    Promise.all([getSMTPProbes(), getSMTPProbeHistory({ limit: 2000 })])
      .then(([status, history]) => {
        setData(status);
        setUptime(history.uptime || []);
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    fetchData();
  }, [fetchData]);

  const summary = data?.summary;
  const uptimeByEndpoint = new Map(uptime.map((u) => [u.endpoint, u]));

  return (
    <>
      <h1>SMTP Probes</h1>
      <p style={{ marginBottom: "0.75rem", color: "#555", fontSize: "0.875rem" }}>
        Active synthetic probing of the relay&apos;s SMTP endpoints (ports 25 / 465 / 587):
        TCP latency, banner, EHLO capabilities, STARTTLS / TLS certificate expiry, and AUTH
        advertisement. Targets come from <code>MXS_PROBE_*</code> / <code>RELAY_*</code>.
      </p>

      <LoadingError loading={loading} error={error} />

      {!loading && !error && data && (
        <>
          {summary && (
            <div
              className={`badge ${
                summary.total_endpoints === 0
                  ? "badge-unknown"
                  : summary.failing === 0 && summary.cert_warnings === 0
                  ? "badge-ok"
                  : summary.failing > 0
                  ? "badge-critical"
                  : "badge-warning"
              }`}
              style={{
                display: "block",
                padding: "0.75rem 1rem",
                marginBottom: "1rem",
                fontSize: "0.95rem",
                textTransform: "none",
                letterSpacing: 0,
              }}
            >
              {summary.total_endpoints === 0
                ? "No probe endpoints configured. Set MXS_PROBE_ENDPOINTS (or MXS_PROBE_HOST) for the probed service."
                : `${summary.healthy}/${summary.total_endpoints} healthy` +
                  (summary.failing > 0 ? ` · ${summary.failing} failing` : "") +
                  (summary.cert_warnings > 0 ? ` · ${summary.cert_warnings} cert warning(s)` : "") +
                  (summary.unprobed > 0 ? ` · ${summary.unprobed} not yet probed` : "")}
              {data.probed_at && (
                <span style={{ fontWeight: 400, marginLeft: "0.5rem", opacity: 0.8 }}>
                  Last probed {fmt(data.probed_at)}.
                </span>
              )}
            </div>
          )}

          {data.endpoints.length === 0 ? (
            <p className="no-results">No SMTP endpoints are being probed.</p>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Endpoint</th>
                    <th>Mode</th>
                    <th>Status</th>
                    <th>Latency</th>
                    <th>TLS / Cert</th>
                    <th>AUTH</th>
                    <th>Uptime</th>
                    <th>Last Probe</th>
                  </tr>
                </thead>
                <tbody>
                  {data.endpoints.map((e) => {
                    const label = endpointLabel(e);
                    const up = uptimeByEndpoint.get(label);
                    return (
                      <tr key={label}>
                        <td style={{ whiteSpace: "nowrap", fontFamily: "monospace" }}>{label}</td>
                        <td style={{ whiteSpace: "nowrap" }}>{e.endpoint.mode}</td>
                        <td>
                          <StatusBadge e={e} />
                          {e.error && (
                            <div style={{ fontSize: "0.75rem", color: "#b00", marginTop: "0.25rem" }}>
                              {e.stage ? `${e.stage}: ` : ""}
                              {e.error}
                            </div>
                          )}
                        </td>
                        <td style={{ whiteSpace: "nowrap" }}>
                          {e.probed ? `${e.latency_ms} ms` : "—"}
                        </td>
                        <td>
                          <CertBadge e={e} />
                          {e.tls?.negotiated && e.tls.version && (
                            <span
                              style={{ fontSize: "0.75rem", color: "#666", marginLeft: "0.4rem" }}
                            >
                              {e.tls.version}
                              {!e.tls.chain_valid && (
                                <span className="badge badge-warning" style={{ marginLeft: "0.35rem" }}>
                                  untrusted chain
                                </span>
                              )}
                            </span>
                          )}
                        </td>
                        <td>
                          {e.auth_advertised ? (
                            <span
                              className="badge badge-ok"
                              title={(e.auth_mechs || []).join(", ")}
                            >
                              {e.auth_mechs && e.auth_mechs.length > 0
                                ? e.auth_mechs.join("/")
                                : "AUTH"}
                            </span>
                          ) : (
                            <span className="badge badge-unknown">none</span>
                          )}
                          {e.greylisting && (
                            <span className="badge badge-warning" style={{ marginLeft: "0.35rem" }}>
                              greylisting
                            </span>
                          )}
                        </td>
                        <td style={{ whiteSpace: "nowrap" }}>
                          {up ? (
                            <span title={`${up.ok_count}/${up.total} probes · p95 ${up.p95_latency_ms.toFixed(0)} ms`}>
                              {up.uptime_pct.toFixed(1)}%
                            </span>
                          ) : (
                            "—"
                          )}
                        </td>
                        <td style={{ whiteSpace: "nowrap" }}>{fmt(e.probed_at)}</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
    </>
  );
}

"use client";

import { useCallback, useEffect, useState } from "react";
import { getRBLStatus, type RBLStatusResponse, type RBLZoneStatus } from "@/lib/api";
import LoadingError from "@/components/LoadingError";

function fmt(ts: string | null | undefined): string {
  if (!ts) return "—";
  return new Date(ts).toLocaleString();
}

function ZoneBadges({ listings }: { listings: RBLZoneStatus[] }) {
  if (!listings || listings.length === 0) {
    return <span className="badge badge-unknown">not checked</span>;
  }
  return (
    <div style={{ display: "flex", flexWrap: "wrap", gap: "0.35rem" }}>
      {listings.map((z) => (
        <span
          key={z.zone}
          className={`badge ${z.listed ? "badge-critical" : "badge-ok"}`}
          title={
            z.listed
              ? `${z.reason || "listed"}${
                  z.listed_since ? ` — since ${fmt(z.listed_since)}` : ""
                }`
              : "clean"
          }
        >
          {z.zone}
        </span>
      ))}
    </div>
  );
}

export default function IPHealthPage() {
  const [data, setData] = useState<RBLStatusResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const fetchData = useCallback(() => {
    setLoading(true);
    setError(null);
    getRBLStatus()
      .then(setData)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    fetchData();
  }, [fetchData]);

  const summary = data?.summary;
  const allClean = !!summary && summary.listed === 0 && summary.total_ips > 0;

  return (
    <>
      <h1>IP Health</h1>
      <p style={{ marginBottom: "0.75rem", color: "#555", fontSize: "0.875rem" }}>
        RBL/DNSBL self-monitoring of the relay&apos;s outbound (egress) IPs. Listed IPs are
        pulled from the sending rotation by the host-side healthy-IP hook.
      </p>

      <LoadingError loading={loading} error={error} />

      {!loading && !error && data && (
        <>
          {summary && (
            <div
              className={`badge ${
                summary.total_ips === 0
                  ? "badge-unknown"
                  : allClean
                  ? "badge-ok"
                  : "badge-critical"
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
              {summary.total_ips === 0
                ? "No egress IPs configured. Set RELAY_EGRESS_IPS (or RELAY_NODE_IP) for the rbld service."
                : allClean
                ? `All clear — ${summary.healthy} of ${summary.total_ips} egress IP(s) clean on every monitored DNSBL.`
                : `${summary.listed} of ${summary.total_ips} egress IP(s) LISTED on a DNSBL. Investigate and request delisting.`}
              {data.checked_at && (
                <span style={{ fontWeight: 400, marginLeft: "0.5rem", opacity: 0.8 }}>
                  Last checked {fmt(data.checked_at)}.
                </span>
              )}
            </div>
          )}

          {data.ips.length === 0 ? (
            <p className="no-results">No egress IPs are being monitored.</p>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Egress IP</th>
                    <th>Status</th>
                    <th>Zone Listings</th>
                    <th>Listed Since</th>
                  </tr>
                </thead>
                <tbody>
                  {data.ips.map((ip) => {
                    const firstListed = ip.listings.find((z) => z.listed);
                    return (
                      <tr key={ip.ip}>
                        <td style={{ whiteSpace: "nowrap", fontFamily: "monospace" }}>
                          {ip.ip}
                        </td>
                        <td>
                          {!ip.checked ? (
                            <span className="badge badge-unknown">unchecked</span>
                          ) : ip.healthy ? (
                            <span className="badge badge-ok">clean</span>
                          ) : (
                            <span className="badge badge-critical">listed</span>
                          )}
                        </td>
                        <td>
                          <ZoneBadges listings={ip.listings} />
                        </td>
                        <td style={{ whiteSpace: "nowrap" }}>
                          {firstListed ? fmt(firstListed.listed_since) : "—"}
                        </td>
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

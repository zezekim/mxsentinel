"use client";

import { useCallback, useEffect, useState } from "react";
import LoadingError from "@/components/LoadingError";
import {
  getSnds,
  getJmrp,
  type SndsIP,
  type JmrpComplaint,
} from "@/lib/api-microsoft";

function fmt(ts: string | null): string {
  return ts ? new Date(ts).toLocaleString() : "—";
}

// Maps an SNDS filter verdict to one of the shared badge classes.
function filterBadge(result: string): string {
  switch (result.toUpperCase()) {
    case "GREEN":
      return "badge-ok";
    case "YELLOW":
      return "badge-warning";
    case "RED":
      return "badge-critical";
    default:
      return "badge-unknown";
  }
}

// A compact text sparkline of recent filter verdicts (G/Y/R), oldest -> newest.
function trendSpark(trend: SndsIP["trend"]): string {
  if (!trend || trend.length === 0) return "—";
  return trend
    .map((p) => {
      switch (p.filter_result.toUpperCase()) {
        case "GREEN":
          return "G";
        case "YELLOW":
          return "Y";
        case "RED":
          return "R";
        default:
          return "·";
      }
    })
    .join("");
}

export default function MicrosoftPage() {
  const [ips, setIps] = useState<SndsIP[]>([]);
  const [complaints, setComplaints] = useState<JmrpComplaint[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const fetchData = useCallback(() => {
    setLoading(true);
    setError(null);
    Promise.all([getSnds(14), getJmrp()])
      .then(([snds, jmrp]) => {
        setIps(snds.ips);
        setComplaints(jmrp.complaints);
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    fetchData();
  }, [fetchData]);

  return (
    <>
      <h1>Microsoft (Outlook / Hotmail)</h1>

      <p style={{ marginBottom: "0.75rem", color: "#555", fontSize: "0.875rem" }}>
        Deliverability signals from Microsoft Smart Network Data Services (SNDS) per sending IP
        and Junk Mail Reporting Program (JMRP) complaints. SNDS is attributed by sending IP;
        JMRP complaints by sending domain. This is the Outlook/Hotmail counterpart to Reputation.
      </p>

      <LoadingError loading={loading} error={error} />

      {!loading && !error && (
        <>
          <h2 style={{ marginTop: "1rem" }}>SNDS — Per-IP Filter State</h2>
          {ips.length === 0 ? (
            <p className="no-results">
              No SNDS data yet. Enroll your egress IP ranges at Microsoft SNDS and set
              MXS_SNDS_KEY for the sndsd daemon.
            </p>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Sending IP</th>
                    <th>Filter Result</th>
                    <th>Complaint Band</th>
                    <th>Trap Hits</th>
                    <th>Recipients</th>
                    <th>Trend (14d)</th>
                    <th>Data Date</th>
                    <th>Updated</th>
                  </tr>
                </thead>
                <tbody>
                  {ips.map((d) => (
                    <tr key={d.ip}>
                      <td>{d.ip}</td>
                      <td>
                        {d.filter_result ? (
                          <span className={`badge ${filterBadge(d.filter_result)}`}>
                            {d.filter_result}
                          </span>
                        ) : (
                          <span className="badge badge-unknown">unknown</span>
                        )}
                      </td>
                      <td>{d.complaint_band || "—"}</td>
                      <td>{d.trap_hits.toLocaleString()}</td>
                      <td>{d.message_recipients.toLocaleString()}</td>
                      <td style={{ fontFamily: "monospace", letterSpacing: "0.1em" }}>
                        {trendSpark(d.trend)}
                      </td>
                      <td style={{ whiteSpace: "nowrap" }}>{d.data_date}</td>
                      <td style={{ whiteSpace: "nowrap" }}>{fmt(d.fetched_at)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}

          <h2 style={{ marginTop: "1.5rem" }}>JMRP — Complaint Feed</h2>
          {complaints.length === 0 ? (
            <p className="no-results">
              No JMRP complaints recorded. Enroll your sending IPs in the Microsoft JMRP and route
              the ARF feedback into the sndsd drop directory (MXS_JMRP_DIR).
            </p>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Sending Domain</th>
                    <th>Sending IP</th>
                    <th>Type</th>
                    <th>Complaints</th>
                    <th>Date</th>
                    <th>Last Seen</th>
                  </tr>
                </thead>
                <tbody>
                  {complaints.map((c, i) => (
                    <tr key={`${c.sender_domain}-${c.sending_ip}-${c.complaint_date}-${i}`}>
                      <td>{c.sender_domain || "—"}</td>
                      <td>{c.sending_ip || "—"}</td>
                      <td>{c.feedback_type}</td>
                      <td>{c.complaint_count.toLocaleString()}</td>
                      <td style={{ whiteSpace: "nowrap" }}>{c.complaint_date}</td>
                      <td style={{ whiteSpace: "nowrap" }}>{fmt(c.last_seen)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
    </>
  );
}

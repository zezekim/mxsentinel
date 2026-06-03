"use client";

import React, { useEffect, useState, useCallback } from "react";
import {
  listIncidents,
  resolveIncident,
  type Incident,
  type IncidentStatus,
} from "@/lib/api";
import Badge from "@/components/Badge";
import LoadingError from "@/components/LoadingError";

function fmtDate(ts: string | null): string {
  if (!ts) return "—";
  try {
    return new Date(ts).toLocaleString();
  } catch {
    return ts;
  }
}

function kindLabel(kind: Incident["kind"]): string {
  switch (kind) {
    case "rejection_spike":
      return "Rejection Spike";
    case "blacklist":
      return "Blacklist";
    case "dns_validation":
      return "DNS Validation";
    default:
      return "Other";
  }
}

// ─── Expandable row detail ────────────────────────────────────────────────────

function IncidentDetail({
  incident,
  onResolve,
}: {
  incident: Incident;
  onResolve: (id: string) => void;
}) {
  const [resolving, setResolving] = useState(false);

  function handleResolve() {
    setResolving(true);
    resolveIncident(incident.id)
      .then(() => onResolve(incident.id))
      .catch(() => setResolving(false));
  }

  return (
    <div className="incident-detail-block">
      {/* AI Summary */}
      <div>
        <div className="detail-section-title">AI Summary</div>
        {incident.ai_analyzed_at === null ? (
          <span className="incident-ai-pending">AI analysis pending</span>
        ) : incident.ai_summary ? (
          <p className="incident-ai-summary">{incident.ai_summary}</p>
        ) : (
          <span className="incident-ai-pending">No summary available</span>
        )}
      </div>

      {/* AI Remediation */}
      {incident.ai_remediation && incident.ai_remediation.length > 0 && (
        <div>
          <div className="detail-section-title">Remediation Steps</div>
          <div className="incident-remediation-list">
            {incident.ai_remediation.map((rec, idx) => (
              <div key={idx} className="incident-remediation-item">
                <span className="rem-action">{rec.action}</span>
                <span>{rec.summary}</span>
                {rec.target && (
                  <span className="rem-target">{rec.target}</span>
                )}
                <span className="rem-priority">{rec.priority}</span>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Confidence */}
      {incident.confidence !== null && (
        <div>
          <div className="detail-section-title">Confidence</div>
          <span style={{ fontSize: "0.875rem" }}>
            {(incident.confidence * 100).toFixed(0)}%
          </span>
        </div>
      )}

      {/* Raw detail */}
      <div>
        <div className="detail-section-title">Raw Detail</div>
        <pre className="incident-detail-json">
          {JSON.stringify(incident.detail, null, 2)}
        </pre>
      </div>

      {/* Resolve button */}
      {incident.status === "open" && (
        <div>
          <button
            className="btn-resolve"
            disabled={resolving}
            onClick={handleResolve}
          >
            {resolving ? "Resolving…" : "Resolve"}
          </button>
        </div>
      )}
    </div>
  );
}

// ─── Main page ────────────────────────────────────────────────────────────────

export default function IncidentsPage() {
  const [statusFilter, setStatusFilter] = useState<IncidentStatus | "">("");
  const [domainFilter, setDomainFilter] = useState("");
  const [incidents, setIncidents] = useState<Incident[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [expandedIds, setExpandedIds] = useState<Set<string>>(new Set());

  const fetchIncidents = useCallback(
    (status: IncidentStatus | "", domain: string) => {
      setLoading(true);
      setError(null);
      listIncidents({ status, domain, limit: 50 })
        .then((d) => setIncidents(d.incidents))
        .catch((e: unknown) =>
          setError(e instanceof Error ? e.message : String(e))
        )
        .finally(() => setLoading(false));
    },
    []
  );

  useEffect(() => {
    fetchIncidents("", "");
  }, [fetchIncidents]);

  function handleSearch(e: React.FormEvent) {
    e.preventDefault();
    fetchIncidents(statusFilter, domainFilter);
  }

  function toggleRow(id: string) {
    setExpandedIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) {
        next.delete(id);
      } else {
        next.add(id);
      }
      return next;
    });
  }

  function handleResolve(id: string) {
    // Refresh the list after a successful resolve
    fetchIncidents(statusFilter, domainFilter);
    // Collapse the row
    setExpandedIds((prev) => {
      const next = new Set(prev);
      next.delete(id);
      return next;
    });
  }

  return (
    <>
      <h1>Incidents</h1>

      <form className="filters" onSubmit={handleSearch}>
        <select
          value={statusFilter}
          onChange={(e) =>
            setStatusFilter(e.target.value as IncidentStatus | "")
          }
        >
          <option value="">Any status</option>
          <option value="open">Open</option>
          <option value="acknowledged">Acknowledged</option>
          <option value="resolved">Resolved</option>
        </select>
        <input
          type="text"
          placeholder="Filter by domain"
          value={domainFilter}
          onChange={(e) => setDomainFilter(e.target.value)}
        />
        <button type="submit">Filter</button>
      </form>

      <LoadingError loading={loading} error={error} />

      {!loading && !error && (
        <>
          {incidents.length === 0 ? (
            <p className="no-results">No incidents found.</p>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Severity</th>
                    <th>Kind</th>
                    <th>Domain</th>
                    <th>Subject</th>
                    <th>Title</th>
                    <th>Status</th>
                    <th>Created</th>
                    <th></th>
                  </tr>
                </thead>
                <tbody>
                  {incidents.map((inc) => {
                    const expanded = expandedIds.has(inc.id);
                    return (
                      <React.Fragment key={inc.id}>
                        <tr
                          className="incident-row-main"
                          onClick={() => toggleRow(inc.id)}
                          aria-expanded={expanded}
                        >
                          <td>
                            <Badge kind="severity" value={inc.severity} />
                          </td>
                          <td style={{ whiteSpace: "nowrap" }}>
                            {kindLabel(inc.kind)}
                          </td>
                          <td>{inc.domain || "—"}</td>
                          <td
                            style={{
                              maxWidth: "160px",
                              overflow: "hidden",
                              textOverflow: "ellipsis",
                              whiteSpace: "nowrap",
                            }}
                            title={inc.subject}
                          >
                            {inc.subject || "—"}
                          </td>
                          <td
                            style={{
                              maxWidth: "220px",
                              overflow: "hidden",
                              textOverflow: "ellipsis",
                              whiteSpace: "nowrap",
                            }}
                            title={inc.title}
                          >
                            {inc.title}
                          </td>
                          <td>
                            <span className={`badge badge-${inc.status}`}>
                              {inc.status}
                            </span>
                          </td>
                          <td style={{ whiteSpace: "nowrap" }}>
                            {fmtDate(inc.created_at)}
                          </td>
                          <td style={{ fontSize: "0.75rem", color: "#9ca3af" }}>
                            {expanded ? "▲" : "▼"}
                          </td>
                        </tr>
                        {expanded && (
                          <tr className="incident-detail-row">
                            <td colSpan={8}>
                              <IncidentDetail
                                incident={inc}
                                onResolve={handleResolve}
                              />
                            </td>
                          </tr>
                        )}
                      </React.Fragment>
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

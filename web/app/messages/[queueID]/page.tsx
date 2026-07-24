"use client";

import { useEffect, useState, useCallback } from "react";
import { useParams } from "next/navigation";
import {
  getMessageDetail,
  reclassifyMessage,
  type MessageDetail,
} from "@/lib/api";
import LoadingError from "@/components/LoadingError";

type Tab = "envelope" | "spam" | "headers" | "timeline";

function authBadge(result: string) {
  const r = (result || "none").toLowerCase();
  const color = r === "pass" ? "#2e7d32" : r === "fail" ? "#c62828" : "#8a8a8a";
  return (
    <span style={{ color, fontWeight: 600, textTransform: "uppercase", fontSize: "0.75rem" }}>
      {r}
    </span>
  );
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <tr>
      <th
        style={{
          textAlign: "left",
          padding: "0.5rem 0.75rem",
          whiteSpace: "nowrap",
          verticalAlign: "top",
          width: "180px",
          borderBottom: "1px solid var(--border, #e3e3e3)",
        }}
      >
        {label}
      </th>
      <td
        style={{
          padding: "0.5rem 0.75rem",
          borderBottom: "1px solid var(--border, #e3e3e3)",
          wordBreak: "break-word",
        }}
      >
        {children}
      </td>
    </tr>
  );
}

export default function MessageDetailPage() {
  const params = useParams<{ queueID: string }>();
  const queueID = decodeURIComponent(params.queueID);

  const [data, setData] = useState<MessageDetail | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [tab, setTab] = useState<Tab>("envelope");
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      setData(await getMessageDetail(queueID));
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }, [queueID]);

  useEffect(() => {
    load();
  }, [load]);

  async function reclassify(spam: boolean) {
    setBusy(true);
    setNotice(null);
    try {
      await reclassifyMessage(queueID, spam);
      setNotice(spam ? "Marked as spam." : "Marked as not spam.");
      await load();
    } catch (e: unknown) {
      setNotice(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  if (loading || error) {
    return <LoadingError loading={loading} error={error} />;
  }
  if (!data) return null;

  const { envelope: env, timeline, spam, content, content_captured, content_restricted } = data;
  const isSpam = spam?.is_spam ?? false;

  const tabs: [Tab, string][] = [
    ["envelope", "Envelope"],
    ["spam", content_captured ? "Spam" : "Spam (no capture)"],
    ["headers", "Headers"],
    ["timeline", "Timeline"],
  ];

  return (
    <div style={{ maxWidth: 1000, margin: "0 auto", padding: "1rem" }}>
      {notice && (
        <div
          style={{
            background: "#e8f4fd",
            border: "1px solid #b6e0fe",
            padding: "0.6rem 0.9rem",
            borderRadius: 4,
            marginBottom: "1rem",
            fontSize: "0.85rem",
          }}
        >
          {notice}
        </div>
      )}

      <div
        style={{
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          gap: "1rem",
          flexWrap: "wrap",
          marginBottom: "1rem",
        }}
      >
        <div>
          <div style={{ fontSize: "0.8rem", color: "#888" }}>Message</div>
          <h1 style={{ margin: 0, fontFamily: "monospace", fontSize: "1.3rem" }}>{env.queue_id}</h1>
        </div>
        <div style={{ display: "flex", gap: "0.5rem" }}>
          {content_captured && (
            <button type="button" disabled={busy} onClick={() => reclassify(!isSpam)}>
              {isSpam ? "Mark Not Spam" : "Mark Spam"}
            </button>
          )}
          <a href="/messages" style={{ fontSize: "0.85rem", alignSelf: "center" }}>
            ← All messages
          </a>
        </div>
      </div>

      {/* status strip */}
      <div style={{ display: "flex", gap: "0.75rem", flexWrap: "wrap", marginBottom: "1rem" }}>
        <span
          style={{
            padding: "0.25rem 0.6rem",
            borderRadius: 4,
            background: env.outcome === "delivered" ? "#e6f4ea" : env.outcome === "bounced" || env.outcome === "rejected" ? "#fdecea" : "#fff4e5",
            color: env.outcome === "delivered" ? "#1e7e34" : env.outcome === "bounced" || env.outcome === "rejected" ? "#c62828" : "#8a6d00",
            fontWeight: 600,
            fontSize: "0.8rem",
            textTransform: "uppercase",
          }}
        >
          {env.outcome}
        </span>
        {content_captured && spam && (
          <span
            style={{
              padding: "0.25rem 0.6rem",
              borderRadius: 4,
              background: isSpam ? "#fdecea" : "#e6f4ea",
              color: isSpam ? "#c62828" : "#1e7e34",
              fontWeight: 600,
              fontSize: "0.8rem",
            }}
          >
            {isSpam ? "SPAM" : "HAM"} {spam.score.toFixed(2)}
          </span>
        )}
      </div>

      {/* tabs */}
      <div style={{ display: "flex", gap: "0.25rem", borderBottom: "2px solid #e3e3e3", marginBottom: "1rem" }}>
        {tabs.map(([t, label]) => (
          <button
            key={t}
            type="button"
            onClick={() => setTab(t)}
            style={{
              border: "none",
              background: "none",
              padding: "0.5rem 0.9rem",
              cursor: "pointer",
              fontWeight: tab === t ? 700 : 400,
              borderBottom: tab === t ? "2px solid #1976d2" : "2px solid transparent",
              marginBottom: "-2px",
              color: tab === t ? "#1976d2" : "inherit",
            }}
          >
            {label}
          </button>
        ))}
      </div>

      {tab === "envelope" && (
        <table style={{ width: "100%", borderCollapse: "collapse", fontSize: "0.9rem" }}>
          <tbody>
            <Row label="Queue ID">
              <span style={{ fontFamily: "monospace" }}>{env.queue_id}</span>
            </Row>
            <Row label="Queued">{env.queued?.replace("T", " ").replace("Z", " UTC")}</Row>
            <Row label="Message-ID">
              <span style={{ fontFamily: "monospace", fontSize: "0.82rem" }}>{env.message_id || "—"}</span>
            </Row>
            <Row label="Authenticated User">{env.sasl_username || "—"}</Row>
            <Row label="Originating IP">{env.source_ip || "—"}</Row>
            <Row label="MAIL FROM">{env.envelope_from || "—"}</Row>
            <Row label="Recipient Domain">{env.recipient_domain || "—"}</Row>
            <Row label="Destination Provider">{env.provider || "—"}</Row>
            <Row label="Authentication">
              <span style={{ display: "flex", gap: "1rem", flexWrap: "wrap" }}>
                <span>SPF {authBadge(env.spf_result)}</span>
                <span>DKIM {authBadge(env.dkim_result)}</span>
                <span>DMARC {authBadge(env.dmarc_result)}</span>
              </span>
            </Row>
            <Row label="TLS">
              {env.tls_used ? `Yes${env.tls_version ? ` (${env.tls_version})` : ""}` : "No"}
            </Row>
            <Row label="Size">{env.size_bytes ? `${env.size_bytes.toLocaleString()} bytes` : "—"}</Row>
            <Row label="Final Response">
              {env.smtp_code ? `${env.smtp_code} ${env.enhanced_status} ${env.response_text}` : "—"}
            </Row>
          </tbody>
        </table>
      )}

      {tab === "spam" && (
        <div>
          {!content_captured || !spam ? (
            <p style={{ color: "#888" }}>
              No spam verdict captured for this message. Enable the rspamd exporter
              (<code>deploy/rspamd/mxs_trace.lua</code>) to populate the Spam and Headers tabs.
            </p>
          ) : (
            <>
              <p style={{ marginTop: 0 }}>
                <strong>Score:</strong> {spam.score.toFixed(2)} &nbsp;·&nbsp;
                <strong>Action:</strong> {spam.action} &nbsp;·&nbsp;
                <strong>Verdict:</strong> {isSpam ? "Spam" : "Ham"}
              </p>
              <div style={{ display: "flex", flexWrap: "wrap", gap: "0.4rem" }}>
                {spam.symbols
                  .slice()
                  .sort((a, b) => b.score - a.score)
                  .map((sym) => {
                    const pos = sym.score > 0;
                    const neg = sym.score < 0;
                    return (
                      <span
                        key={sym.name}
                        title={`${sym.name}: ${sym.score}`}
                        style={{
                          display: "inline-flex",
                          gap: "0.4rem",
                          alignItems: "center",
                          padding: "0.2rem 0.5rem",
                          borderRadius: 4,
                          fontSize: "0.75rem",
                          background: pos ? "#fdecea" : neg ? "#e6f4ea" : "#eee",
                          color: pos ? "#b71c1c" : neg ? "#1b5e20" : "#555",
                          fontFamily: "monospace",
                        }}
                      >
                        <span>{sym.name}</span>
                        <span style={{ fontWeight: 700 }}>{sym.score.toFixed(sym.score % 1 === 0 ? 0 : 1)}</span>
                      </span>
                    );
                  })}
              </div>
            </>
          )}
        </div>
      )}

      {tab === "headers" && (
        <div>
          {content_restricted && (
            <p style={{ color: "#b26a00" }}>
              Subject and headers are message content — visible only with an admin-scoped token.
            </p>
          )}
          {!content_captured && !content_restricted && (
            <p style={{ color: "#888" }}>No headers captured for this message.</p>
          )}
          {content && (
            <>
              {content.subject && (
                <p style={{ marginTop: 0 }}>
                  <strong>Subject:</strong> {content.subject}
                </p>
              )}
              <table style={{ width: "100%", borderCollapse: "collapse", fontSize: "0.85rem" }}>
                <tbody>
                  {content.parsed_headers.map((h, i) => (
                    <Row key={`${h.name}-${i}`} label={h.name}>
                      <span style={{ fontFamily: "monospace", fontSize: "0.8rem" }}>{h.value}</span>
                    </Row>
                  ))}
                </tbody>
              </table>
            </>
          )}
        </div>
      )}

      {tab === "timeline" && (
        <table style={{ width: "100%", borderCollapse: "collapse", fontSize: "0.85rem" }}>
          <thead>
            <tr>
              <th style={{ textAlign: "left", padding: "0.4rem 0.6rem" }}>When</th>
              <th style={{ textAlign: "left", padding: "0.4rem 0.6rem" }}>Event</th>
              <th style={{ textAlign: "left", padding: "0.4rem 0.6rem" }}>Detail</th>
            </tr>
          </thead>
          <tbody>
            {timeline.map((e, i) => (
              <tr key={i} style={{ borderTop: "1px solid #eee" }}>
                <td style={{ padding: "0.4rem 0.6rem", whiteSpace: "nowrap" }}>
                  {e.event_time.replace("T", " ").replace("Z", "")}
                </td>
                <td style={{ padding: "0.4rem 0.6rem", fontWeight: 600 }}>{e.event_type}</td>
                <td style={{ padding: "0.4rem 0.6rem" }}>
                  {[e.provider, e.smtp_code ? String(e.smtp_code) : "", e.enhanced_status, e.response_text]
                    .filter(Boolean)
                    .join(" · ")}
                </td>
              </tr>
            ))}
            {timeline.length === 0 && (
              <tr>
                <td colSpan={3} style={{ padding: "0.6rem", color: "#888" }}>
                  No timeline events.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      )}
    </div>
  );
}

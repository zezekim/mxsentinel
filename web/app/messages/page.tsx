"use client";

import { useEffect, useState, useCallback } from "react";
import { apiGet, createShareLink, type MessagesResponse, type Message } from "@/lib/api";
import LoadingError from "@/components/LoadingError";

// ShareControl mints a public, capability-URL trace link for one message (by relay queue id)
// so it can be sent to a client — "did my message deliver?" without exposing the dashboard.
function ShareControl({ queueId }: { queueId: string }) {
  const [url, setUrl] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  if (!queueId) {
    return (
      <span style={{ fontSize: "0.75rem", color: "#aaa" }}>
        No queue id — this message can’t be shared.
      </span>
    );
  }

  async function mint() {
    setBusy(true);
    setErr(null);
    try {
      const link = await createShareLink(queueId);
      // link.url is absolute when the server has MXS_PUBLIC_BASE_URL, else a relative /trace path.
      const full = link.url.startsWith("http")
        ? link.url
        : `${window.location.origin}${link.path}`;
      setUrl(full);
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  function copy() {
    if (!url) return;
    navigator.clipboard?.writeText(url).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  }

  if (url) {
    return (
      <div style={{ display: "flex", gap: "0.5rem", alignItems: "center", flexWrap: "wrap" }}>
        <input
          readOnly
          value={url}
          onFocus={(e) => e.target.select()}
          style={{ flex: 1, minWidth: "260px", fontSize: "0.78rem", padding: "0.3rem 0.5rem" }}
        />
        <button type="button" onClick={copy} style={{ fontSize: "0.78rem" }}>
          {copied ? "Copied!" : "Copy"}
        </button>
        <a href={url} target="_blank" rel="noreferrer" style={{ fontSize: "0.78rem" }}>
          Open ↗
        </a>
      </div>
    );
  }

  return (
    <div style={{ display: "flex", gap: "0.5rem", alignItems: "center" }}>
      <button type="button" onClick={mint} disabled={busy} style={{ fontSize: "0.78rem" }}>
        {busy ? "Creating…" : "Create share link"}
      </button>
      {err && <span style={{ fontSize: "0.75rem", color: "#c0392b" }}>{err}</span>}
    </div>
  );
}

const OUTCOMES = ["any", "delivered", "deferred", "bounced", "rejected", "received"];

function fmt(ts: string): string {
  return new Date(ts).toLocaleString();
}

function outcomeBadgeClass(outcome: string) {
  if (outcome === "delivered") return "badge-ok";
  if (outcome === "bounced" || outcome === "rejected") return "badge-critical";
  if (outcome === "deferred") return "badge-warning";
  return "badge-unknown";
}

function DetailRow({ m, cols }: { m: Message; cols: number }) {
  const fields: [string, string | number | null | undefined][] = [
    ["Time",             fmt(m.event_time)],
    ["Event ID",         m.event_id],
    ["Message-ID",       m.message_id],
    ["Queue ID",         m.queue_id],
    ["Outcome",          m.outcome],
    ["Event type",       m.event_type],
    ["From domain",      m.from_domain],
    ["Recipient domain", m.recipient_domain],
    ["Provider",         m.provider],
    ["Relay IP",         m.relay_ip],
    ["SMTP user",        m.sasl_username],
    ["SMTP code",        m.smtp_code || null],
    ["Enhanced status",  m.enhanced_status],
    ["Bounce class",     m.bounce_class],
    ["Response text",    m.response_text],
  ];

  return (
    <tr>
      <td
        colSpan={cols}
        style={{
          background: "#f9f9f9",
          padding: "0.75rem 1rem",
          borderTop: "none",
        }}
      >
        <div
          style={{
            display: "grid",
            gridTemplateColumns: "repeat(auto-fill, minmax(280px, 1fr))",
            gap: "0.35rem 1.5rem",
            fontSize: "0.8rem",
          }}
        >
          {fields.map(([label, value]) =>
            value ? (
              <div key={label} style={{ display: "flex", gap: "0.4rem", alignItems: "flex-start" }}>
                <span style={{ color: "#888", flexShrink: 0, minWidth: "110px" }}>{label}:</span>
                <span style={{ wordBreak: "break-all", color: "#222" }}>{value}</span>
              </div>
            ) : null
          )}
        </div>
        <div style={{ marginTop: "0.75rem", paddingTop: "0.75rem", borderTop: "1px solid #eee" }}>
          <div style={{ fontSize: "0.72rem", color: "#888", marginBottom: "0.35rem" }}>
            Share a client-facing status link for this message:
          </div>
          <ShareControl queueId={m.queue_id} />
        </div>
      </td>
    </tr>
  );
}

export default function MessagesPage() {
  const [domain, setDomain] = useState("");
  const [sender, setSender] = useState("");
  const [messageId, setMessageId] = useState("");
  const [provider, setProvider] = useState("");
  const [user, setUser] = useState("");
  const [outcome, setOutcome] = useState("any");
  const [dateFrom, setDateFrom] = useState("");
  const [dateTo, setDateTo] = useState("");

  const [messages, setMessages] = useState<Message[]>([]);
  const [count, setCount] = useState<number | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [expanded, setExpanded] = useState<string | null>(null);

  const buildQuery = useCallback(() => {
    const params = new URLSearchParams();
    if (domain) params.set("domain", domain);
    if (sender) params.set("sender", sender);
    if (messageId) params.set("message_id", messageId);
    if (provider) params.set("provider", provider);
    if (user) params.set("user", user);
    if (outcome !== "any") params.set("outcome", outcome);
    if (dateFrom) params.set("since", new Date(`${dateFrom}T00:00:00`).toISOString());
    if (dateTo) params.set("until", new Date(`${dateTo}T23:59:59.999`).toISOString());
    params.set("limit", "100");
    return params.toString();
  }, [domain, sender, messageId, provider, user, outcome, dateFrom, dateTo]);

  const fetchMessages = useCallback(() => {
    setLoading(true);
    setError(null);
    setExpanded(null);
    apiGet<MessagesResponse>(`/v1/messages?${buildQuery()}`)
      .then((d) => {
        setMessages(d.messages);
        setCount(d.count);
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, [buildQuery]);

  useEffect(() => {
    fetchMessages();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  function handleSearch(e: React.FormEvent) {
    e.preventDefault();
    fetchMessages();
  }

  function toggleRow(id: string) {
    setExpanded((prev) => (prev === id ? null : id));
  }

  const COLS = 8;

  return (
    <>
      <h1>Message Explorer</h1>

      <form className="filters" onSubmit={handleSearch}>
        <input type="text" placeholder="Domain"    value={domain}    onChange={(e) => setDomain(e.target.value)} />
        <input type="text" placeholder="Sender"    value={sender}    onChange={(e) => setSender(e.target.value)} />
        <input type="text" placeholder="Message-ID" value={messageId} onChange={(e) => setMessageId(e.target.value)} />
        <input type="text" placeholder="Provider"  value={provider}  onChange={(e) => setProvider(e.target.value)} />
        <input type="text" placeholder="SMTP user" value={user}      onChange={(e) => setUser(e.target.value)} />
        <select value={outcome} onChange={(e) => setOutcome(e.target.value)}>
          {OUTCOMES.map((o) => (
            <option key={o} value={o}>{o.charAt(0).toUpperCase() + o.slice(1)}</option>
          ))}
        </select>
        <label className="filter-date">
          <span>From</span>
          <input
            type="date"
            value={dateFrom}
            max={dateTo || undefined}
            onChange={(e) => setDateFrom(e.target.value)}
          />
        </label>
        <label className="filter-date">
          <span>To</span>
          <input
            type="date"
            value={dateTo}
            min={dateFrom || undefined}
            onChange={(e) => setDateTo(e.target.value)}
          />
        </label>
        <button type="submit">Search</button>
        {(dateFrom || dateTo) && (
          <button
            type="button"
            className="filter-date-clear"
            onClick={() => { setDateFrom(""); setDateTo(""); }}
          >
            Clear dates
          </button>
        )}
      </form>

      <LoadingError loading={loading} error={error} />

      {!loading && !error && (
        <>
          {count !== null && (
            <p style={{ marginBottom: "0.75rem", color: "#555", fontSize: "0.875rem" }}>
              {count} message{count !== 1 ? "s" : ""} found
            </p>
          )}
          {messages.length === 0 ? (
            <p className="no-results">No messages match your filters.</p>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th></th>
                    <th>Time</th>
                    <th>Outcome</th>
                    <th>From Domain</th>
                    <th>Recipient Domain</th>
                    <th>Provider</th>
                    <th>Code</th>
                    <th>Reason</th>
                  </tr>
                </thead>
                <tbody>
                  {messages.map((m) => {
                    const isExpanded = expanded === m.event_id;
                    const isFailed = m.outcome === "bounced" || m.outcome === "rejected" || m.outcome === "deferred";
                    const failColor = m.outcome === "deferred" ? "#b87d00" : "#c0392b";

                    return (
                      <>
                        <tr
                          key={m.event_id}
                          onClick={() => toggleRow(m.event_id)}
                          style={{ cursor: "pointer" }}
                        >
                          <td style={{ width: "1.5rem", color: "#aaa", fontSize: "0.7rem", userSelect: "none" }}>
                            {isExpanded ? "▼" : "▶"}
                          </td>
                          <td style={{ whiteSpace: "nowrap" }}>{fmt(m.event_time)}</td>
                          <td>
                            <span className={`badge ${outcomeBadgeClass(m.outcome)}`}>
                              {m.outcome}
                            </span>
                          </td>
                          <td>{m.from_domain}</td>
                          <td>{m.recipient_domain}</td>
                          <td>{m.provider}</td>
                          <td style={{ whiteSpace: "nowrap" }}>
                            {m.smtp_code || "—"}
                            {m.enhanced_status && (
                              <span style={{ color: "#888", fontSize: "0.75rem", marginLeft: "0.3rem" }}>
                                {m.enhanced_status}
                              </span>
                            )}
                          </td>
                          <td style={{ maxWidth: "260px" }}>
                            {isFailed && (m.bounce_class || m.response_text) ? (
                              <span style={{ fontSize: "0.8rem", color: failColor }}>
                                {m.bounce_class && (
                                  <strong style={{ marginRight: "0.3rem" }}>[{m.bounce_class}]</strong>
                                )}
                                <span style={{
                                  overflow: "hidden",
                                  textOverflow: "ellipsis",
                                  whiteSpace: "nowrap",
                                  display: "inline-block",
                                  maxWidth: "200px",
                                  verticalAlign: "middle",
                                }}>
                                  {m.response_text}
                                </span>
                              </span>
                            ) : (
                              <span style={{ color: "#aaa" }}>—</span>
                            )}
                          </td>
                        </tr>
                        {isExpanded && <DetailRow m={m} cols={COLS} />}
                      </>
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

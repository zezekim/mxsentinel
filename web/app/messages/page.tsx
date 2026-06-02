"use client";

import { useEffect, useState, useCallback } from "react";
import { apiGet, type MessagesResponse, type Message } from "@/lib/api";
import LoadingError from "@/components/LoadingError";

const OUTCOMES = ["any", "delivered", "deferred", "bounced", "rejected", "received"];

function fmt(ts: string): string {
  return new Date(ts).toLocaleString();
}

export default function MessagesPage() {
  const [domain, setDomain] = useState("");
  const [sender, setSender] = useState("");
  const [messageId, setMessageId] = useState("");
  const [provider, setProvider] = useState("");
  const [outcome, setOutcome] = useState("any");

  const [messages, setMessages] = useState<Message[]>([]);
  const [count, setCount] = useState<number | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const buildQuery = useCallback(() => {
    const params = new URLSearchParams();
    if (domain) params.set("domain", domain);
    if (sender) params.set("sender", sender);
    if (messageId) params.set("message_id", messageId);
    if (provider) params.set("provider", provider);
    if (outcome !== "any") params.set("outcome", outcome);
    params.set("limit", "100");
    return params.toString();
  }, [domain, sender, messageId, provider, outcome]);

  const fetchMessages = useCallback(() => {
    setLoading(true);
    setError(null);
    apiGet<MessagesResponse>(`/v1/messages?${buildQuery()}`)
      .then((d) => {
        setMessages(d.messages);
        setCount(d.count);
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, [buildQuery]);

  // Load on mount
  useEffect(() => {
    fetchMessages();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  function handleSearch(e: React.FormEvent) {
    e.preventDefault();
    fetchMessages();
  }

  return (
    <>
      <h1>Message Explorer</h1>

      <form className="filters" onSubmit={handleSearch}>
        <input
          type="text"
          placeholder="Domain"
          value={domain}
          onChange={(e) => setDomain(e.target.value)}
        />
        <input
          type="text"
          placeholder="Sender"
          value={sender}
          onChange={(e) => setSender(e.target.value)}
        />
        <input
          type="text"
          placeholder="Message-ID"
          value={messageId}
          onChange={(e) => setMessageId(e.target.value)}
        />
        <input
          type="text"
          placeholder="Provider"
          value={provider}
          onChange={(e) => setProvider(e.target.value)}
        />
        <select value={outcome} onChange={(e) => setOutcome(e.target.value)}>
          {OUTCOMES.map((o) => (
            <option key={o} value={o}>
              {o.charAt(0).toUpperCase() + o.slice(1)}
            </option>
          ))}
        </select>
        <button type="submit">Search</button>
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
                    <th>Time</th>
                    <th>Outcome</th>
                    <th>From Domain</th>
                    <th>Recipient Domain</th>
                    <th>Provider</th>
                    <th>SMTP</th>
                    <th>Message-ID</th>
                    <th>Relay IP</th>
                  </tr>
                </thead>
                <tbody>
                  {messages.map((m) => (
                    <tr key={m.event_id}>
                      <td style={{ whiteSpace: "nowrap" }}>{fmt(m.event_time)}</td>
                      <td>
                        <span
                          className={`badge ${
                            m.outcome === "delivered"
                              ? "badge-ok"
                              : m.outcome === "bounced" || m.outcome === "rejected"
                              ? "badge-critical"
                              : m.outcome === "deferred"
                              ? "badge-warning"
                              : "badge-unknown"
                          }`}
                        >
                          {m.outcome}
                        </span>
                      </td>
                      <td>{m.from_domain}</td>
                      <td>{m.recipient_domain}</td>
                      <td>{m.provider}</td>
                      <td>{m.smtp_code || "—"}</td>
                      <td>
                        <span
                          style={{
                            fontSize: "0.75rem",
                            maxWidth: "200px",
                            display: "inline-block",
                            overflow: "hidden",
                            textOverflow: "ellipsis",
                            whiteSpace: "nowrap",
                            verticalAlign: "middle",
                          }}
                          title={m.message_id}
                        >
                          {m.message_id || "—"}
                        </span>
                      </td>
                      <td>{m.relay_ip || "—"}</td>
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

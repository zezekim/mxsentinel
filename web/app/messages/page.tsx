"use client";

import { Fragment, useCallback, useEffect, useState } from "react";
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
          {m.queue_id && (
            <div style={{ marginBottom: "0.6rem" }}>
              <a
                href={`/messages/${encodeURIComponent(m.queue_id)}`}
                style={{ fontSize: "0.8rem", fontWeight: 600 }}
                onClick={(e) => e.stopPropagation()}
              >
                Full report → (envelope · spam · headers · timeline)
              </a>
            </div>
          )}
          <div style={{ fontSize: "0.72rem", color: "#888", marginBottom: "0.35rem" }}>
            Share a client-facing status link for this message:
          </div>
          <ShareControl queueId={m.queue_id} />
        </div>
      </td>
    </tr>
  );
}

// One page of rows. The API caps limit at 1000; 100 keeps the table quick to scan
// and the payload small.
const PAGE_SIZE = 100;

// Hard stop on how deep the pager will go. Beyond this the honest answer is a
// narrower filter, not more pages — deep offsets get progressively more expensive
// for ClickHouse to skip past.
const MAX_PAGES = 20;

// The filter values a result set was actually fetched with. Kept separate from the
// live form state so paging never silently applies half-typed edits: you see page 2
// of the search you ran, not page 2 of what you are in the middle of typing.
interface AppliedFilters {
  domain: string;
  sender: string;
  messageId: string;
  provider: string;
  user: string;
  outcome: string;
  dateFrom: string;
  dateTo: string;
}

const EMPTY_FILTERS: AppliedFilters = {
  domain: "",
  sender: "",
  messageId: "",
  provider: "",
  user: "",
  outcome: "any",
  dateFrom: "",
  dateTo: "",
};

// Windowed page numbers around the current page, so the control stays a fixed width
// whether you are on page 1 or 17.
function pageWindow(current: number, last: number): number[] {
  const span = 5;
  let start = Math.max(1, current - Math.floor(span / 2));
  const end = Math.min(last, start + span - 1);
  start = Math.max(1, end - span + 1);
  const out: number[] = [];
  for (let p = start; p <= end; p++) out.push(p);
  return out;
}

function Pager({
  page,
  hasNext,
  onPage,
}: {
  page: number;
  hasNext: boolean;
  onPage: (p: number) => void;
}) {
  // There is no total count to divide by — /v1/messages returns only the rows it
  // hands back — so the last reachable page is inferred: the furthest page we know
  // exists is the current one, plus one more if the over-fetch found extra rows.
  const known = hasNext ? page + 1 : page;
  const last = Math.min(MAX_PAGES, known);
  if (last <= 1) return null;

  return (
    <nav className="pager" aria-label="Message pages">
      <button
        type="button"
        className="pager-btn"
        onClick={() => onPage(page - 1)}
        disabled={page <= 1}
      >
        ← Prev
      </button>

      {pageWindow(page, last).map((p) => (
        <button
          key={p}
          type="button"
          className={`pager-btn pager-num${p === page ? " is-active" : ""}`}
          aria-current={p === page ? "page" : undefined}
          onClick={() => onPage(p)}
        >
          {p}
        </button>
      ))}

      {hasNext && last < MAX_PAGES && <span className="pager-ellipsis">…</span>}

      <button
        type="button"
        className="pager-btn"
        onClick={() => onPage(page + 1)}
        disabled={!hasNext || page >= MAX_PAGES}
      >
        Next →
      </button>
    </nav>
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

  const [applied, setApplied] = useState<AppliedFilters>(EMPTY_FILTERS);
  const [page, setPage] = useState(1);

  const [messages, setMessages] = useState<Message[]>([]);
  const [hasNext, setHasNext] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [expanded, setExpanded] = useState<string | null>(null);

  const buildQuery = useCallback(
    (f: AppliedFilters, p: number) => {
      const params = new URLSearchParams();
      if (f.domain) params.set("domain", f.domain);
      if (f.sender) params.set("sender", f.sender);
      if (f.messageId) params.set("message_id", f.messageId);
      if (f.provider) params.set("provider", f.provider);
      if (f.user) params.set("user", f.user);
      if (f.outcome !== "any") params.set("outcome", f.outcome);
      if (f.dateFrom) params.set("since", new Date(`${f.dateFrom}T00:00:00`).toISOString());
      if (f.dateTo) params.set("until", new Date(`${f.dateTo}T23:59:59.999`).toISOString());
      // Ask for one row more than we show: if it comes back, there is another page.
      // Cheaper than a second COUNT query against ClickHouse.
      params.set("limit", String(PAGE_SIZE + 1));
      params.set("offset", String((p - 1) * PAGE_SIZE));
      return params.toString();
    },
    [],
  );

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError(null);
    setExpanded(null);

    apiGet<MessagesResponse>(`/v1/messages?${buildQuery(applied, page)}`)
      .then((d) => {
        if (cancelled) return;
        const rows = d.messages ?? [];
        setHasNext(rows.length > PAGE_SIZE);
        setMessages(rows.slice(0, PAGE_SIZE));
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });

    // Ignore a response that lands after the filters or page moved on.
    return () => {
      cancelled = true;
    };
  }, [applied, page, buildQuery]);

  function handleSearch(e: React.FormEvent) {
    e.preventDefault();
    // New search: back to page 1, and freeze the current form values as the ones
    // this result set belongs to.
    setPage(1);
    setApplied({ domain, sender, messageId, provider, user, outcome, dateFrom, dateTo });
  }

  function goToPage(p: number) {
    const next = Math.min(MAX_PAGES, Math.max(1, p));
    if (next === page) return;
    setPage(next);
    window.scrollTo({ top: 0, behavior: "smooth" });
  }

  function toggleRow(id: string) {
    setExpanded((prev) => (prev === id ? null : id));
  }

  const COLS = 8;
  const firstRow = (page - 1) * PAGE_SIZE + 1;
  const lastRow = (page - 1) * PAGE_SIZE + messages.length;

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
          {messages.length === 0 ? (
            <p className="no-results">
              {page > 1
                ? "No more messages on this page — try an earlier page or widen your filters."
                : "No messages match your filters."}
            </p>
          ) : (
            <>
              <p className="result-summary">
                Showing <strong>{firstRow.toLocaleString()}–{lastRow.toLocaleString()}</strong>
                {" "}· page {page}
                {hasNext && page < MAX_PAGES ? " of many" : ""}
              </p>

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
                        <Fragment key={m.event_id}>
                          <tr
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
                        </Fragment>
                      );
                    })}
                  </tbody>
                </table>
              </div>

              <Pager page={page} hasNext={hasNext} onPage={goToPage} />

              {page >= MAX_PAGES && hasNext && (
                <p className="pager-cap">
                  That is the {MAX_PAGES}-page limit ({(MAX_PAGES * PAGE_SIZE).toLocaleString()} messages).
                  Narrow the date range or filters to reach older messages.
                </p>
              )}
            </>
          )}
        </>
      )}
    </>
  );
}

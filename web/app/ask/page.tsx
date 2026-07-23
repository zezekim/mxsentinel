"use client";

import { useCallback, useEffect, useState } from "react";
import {
  ask,
  askHistory,
  type AskResponse,
  type AskQueryResult,
  type AskHistoryEntry,
} from "@/lib/api-ask";
import LoadingError from "@/components/LoadingError";

const EXAMPLES = [
  "Why did mail to Yahoo drop this week?",
  "What are my top rejection reasons in the last 24h?",
  "Which senders are getting flagged as spam?",
  "What is my DMARC pass rate this month?",
];

function fmtCell(v: unknown): string {
  if (v === null || v === undefined) return "—";
  if (typeof v === "number") {
    // Rates are fractional (0–1); render as % when clearly a rate.
    return Number.isInteger(v) ? v.toLocaleString() : `${(v * 100).toFixed(2)}%`;
  }
  return String(v);
}

function ResultTable({ r }: { r: AskQueryResult }) {
  return (
    <div style={{ marginTop: "0.75rem" }}>
      <div style={{ fontSize: "0.8rem", fontWeight: 600, marginBottom: "0.25rem" }}>{r.label}</div>
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              {r.columns.map((c) => (
                <th key={c}>{c.replace(/_/g, " ")}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {r.rows.length === 0 ? (
              <tr>
                <td colSpan={r.columns.length} className="no-results">
                  No rows.
                </td>
              </tr>
            ) : (
              r.rows.map((row, i) => (
                <tr key={i}>
                  {r.columns.map((c) => (
                    <td key={c}>{fmtCell(row[c])}</td>
                  ))}
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}

export default function AskPage() {
  const [question, setQuestion] = useState("");
  const [result, setResult] = useState<AskResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [history, setHistory] = useState<AskHistoryEntry[]>([]);

  const loadHistory = useCallback(() => {
    askHistory(15)
      .then((h) => setHistory(h.history))
      .catch(() => {
        /* history is best-effort */
      });
  }, []);

  useEffect(() => {
    loadHistory();
  }, [loadHistory]);

  const submit = useCallback(
    (q: string) => {
      const trimmed = q.trim();
      if (!trimmed) return;
      setLoading(true);
      setError(null);
      setResult(null);
      ask(trimmed)
        .then((res) => {
          setResult(res);
          loadHistory();
        })
        .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
        .finally(() => setLoading(false));
    },
    [loadHistory],
  );

  return (
    <>
      <h1>Ask Your Mail Logs</h1>

      <p style={{ marginBottom: "0.75rem", color: "#555", fontSize: "0.875rem" }}>
        Ask a plain-English question about your deliverability. A local model plans which
        pre-approved <strong>aggregate</strong> queries to run and explains the results. It only
        ever sees counts and rates — never message bodies or subject lines, and it cannot run
        free-form SQL.
      </p>

      <form
        onSubmit={(e) => {
          e.preventDefault();
          submit(question);
        }}
        style={{ display: "flex", gap: "0.5rem", marginBottom: "0.75rem" }}
      >
        <input
          type="text"
          value={question}
          onChange={(e) => setQuestion(e.target.value)}
          placeholder="e.g. Why did mail to Yahoo drop 40% Tuesday?"
          maxLength={2000}
          style={{
            flex: 1,
            padding: "0.5rem 0.75rem",
            border: "1px solid #ccc",
            borderRadius: "6px",
            fontSize: "0.9rem",
          }}
        />
        <button type="submit" disabled={loading || !question.trim()} className="btn">
          {loading ? "Thinking…" : "Ask"}
        </button>
      </form>

      <div style={{ display: "flex", flexWrap: "wrap", gap: "0.4rem", marginBottom: "1rem" }}>
        {EXAMPLES.map((ex) => (
          <button
            key={ex}
            type="button"
            className="badge badge-unknown"
            style={{ cursor: "pointer", border: "none" }}
            onClick={() => {
              setQuestion(ex);
              submit(ex);
            }}
          >
            {ex}
          </button>
        ))}
      </div>

      <LoadingError loading={loading} error={error} />

      {result && !loading && (
        <div
          style={{
            border: "1px solid #e2e2e2",
            borderRadius: "8px",
            padding: "1rem",
            marginBottom: "1.5rem",
          }}
        >
          <p style={{ whiteSpace: "pre-wrap", margin: 0, lineHeight: 1.5 }}>{result.answer}</p>

          {result.used_queries.length > 0 && (
            <div style={{ marginTop: "0.5rem", fontSize: "0.75rem", color: "#777" }}>
              Backed by:{" "}
              {result.used_queries.map((q) => (
                <span key={q} className="badge badge-ok" style={{ marginRight: "0.3rem" }}>
                  {q}
                </span>
              ))}
            </div>
          )}

          {result.data.map((r) => (
            <ResultTable key={r.tool + r.label} r={r} />
          ))}
        </div>
      )}

      {history.length > 0 && (
        <>
          <h2 style={{ fontSize: "1rem", marginBottom: "0.5rem" }}>Recent questions</h2>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>When</th>
                  <th>Question</th>
                  <th>Answer</th>
                </tr>
              </thead>
              <tbody>
                {history.map((h) => (
                  <tr
                    key={h.id}
                    style={{ cursor: "pointer" }}
                    onClick={() => {
                      setQuestion(h.question);
                      submit(h.question);
                    }}
                  >
                    <td style={{ whiteSpace: "nowrap" }}>
                      {new Date(h.created_at).toLocaleString()}
                    </td>
                    <td>{h.question}</td>
                    <td style={{ color: "#555" }}>{h.answer}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
    </>
  );
}

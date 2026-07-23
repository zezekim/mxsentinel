// API client for the natural-language analytics ("Ask your mail logs") feature.
// Talks to POST /v1/ask and GET /v1/ask/history. Auth is reused from @/lib/api
// (getToken) — we do NOT modify that shared module.
//
// Privacy note (mirrors the backend): the model only ever plans over a fixed whitelist of
// aggregate queries. The `data` returned here is aggregate rows (counts/rates) — never any
// message body or subject.

import { getToken } from "@/lib/api";

const API_BASE =
  process.env.NEXT_PUBLIC_API_BASE ?? "http://localhost:8080";

export interface AskQueryResult {
  tool: string;
  label: string;
  columns: string[];
  rows: Record<string, unknown>[];
}

export interface AskResponse {
  answer: string;
  used_queries: string[];
  data: AskQueryResult[];
}

export interface AskHistoryEntry {
  id: string;
  question: string;
  chosen_tools: unknown;
  answer: string;
  created_at: string;
}

export interface AskHistoryResponse {
  history: AskHistoryEntry[];
}

function authHeaders(): Record<string, string> {
  const token = getToken();
  return token
    ? { "Content-Type": "application/json", Authorization: `Bearer ${token}` }
    : { "Content-Type": "application/json" };
}

async function handle<T>(res: Response): Promise<T> {
  if (!res.ok) {
    let msg = `HTTP ${res.status}`;
    try {
      const body = await res.json();
      if (body?.error?.message) msg = body.error.message;
    } catch {
      /* ignore */
    }
    throw new Error(msg);
  }
  return res.json() as Promise<T>;
}

export async function ask(question: string): Promise<AskResponse> {
  const res = await fetch(`${API_BASE}/v1/ask`, {
    method: "POST",
    headers: authHeaders(),
    body: JSON.stringify({ question }),
  });
  return handle<AskResponse>(res);
}

export async function askHistory(limit = 20): Promise<AskHistoryResponse> {
  const res = await fetch(`${API_BASE}/v1/ask/history?limit=${limit}`, {
    headers: authHeaders(),
  });
  return handle<AskHistoryResponse>(res);
}

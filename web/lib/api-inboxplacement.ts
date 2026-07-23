// API client for inbox-placement / seed-list testing. Uses the shared token helper from
// @/lib/api (do not edit that module) and talks to the apid /v1/seed-* endpoints.

import { getToken } from "@/lib/api";

const API_BASE = process.env.NEXT_PUBLIC_API_BASE ?? "http://localhost:8080";

function authHeaders(): HeadersInit {
  const token = getToken();
  return token ? { Authorization: `Bearer ${token}` } : {};
}

async function req<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await fetch(`${API_BASE}${path}`, {
    ...options,
    cache: "no-store",
    headers: {
      "Content-Type": "application/json",
      ...authHeaders(),
      ...(options?.headers ?? {}),
    },
  });
  if (!res.ok) {
    let msg = `HTTP ${res.status}`;
    try {
      const body = await res.json();
      if (body?.error?.message) msg = body.error.message;
    } catch {
      // ignore
    }
    throw new Error(msg);
  }
  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}

// ─── Types ──────────────────────────────────────────────────────────────────

export type Provider = "gmail" | "outlook" | "yahoo" | "other";
export type Placement = "unknown" | "inbox" | "spam" | "missing";
export type RunStatus =
  | "pending"
  | "sending"
  | "collecting"
  | "completed"
  | "failed";

export interface SeedAddress {
  id: string;
  address: string;
  provider: Provider;
  enabled: boolean;
}

export interface SeedList {
  id: string;
  name: string;
  description: string;
  address_count: number;
  addresses?: SeedAddress[];
  created_at: string;
}

export interface SeedRun {
  id: string;
  list_id: string | null;
  name: string;
  run_tag: string;
  from_address: string;
  ip_pool: string;
  status: RunStatus;
  seed_count: number;
  sent_count: number;
  started_at: string | null;
  completed_at: string | null;
  created_at: string;
}

export interface SeedResult {
  address: string;
  provider: Provider;
  status: string;
  placement: Placement;
  mailbox: string;
  spf_pass: boolean | null;
  dkim_pass: boolean | null;
  dmarc_pass: boolean | null;
  detail: string;
  sent_at: string | null;
  observed_at: string | null;
}

export interface ProviderSummary {
  provider: string;
  total: number;
  inbox: number;
  spam: number;
  missing: number;
  pending: number;
  inbox_rate: number;
  spam_rate: number;
  missing_rate: number;
  spf_pass_rate: number;
  dkim_pass_rate: number;
  dmarc_pass_rate: number;
}

export interface PlacementSummary {
  overall: ProviderSummary;
  providers: ProviderSummary[];
}

export interface SeedRunDetail extends SeedRun {
  summary: PlacementSummary;
  results: SeedResult[];
}

// ─── Calls ──────────────────────────────────────────────────────────────────

export function listSeedLists(): Promise<{ lists: SeedList[] }> {
  return req<{ lists: SeedList[] }>("/v1/seed-lists");
}

export function getSeedList(id: string): Promise<SeedList> {
  return req<SeedList>(`/v1/seed-lists/${id}`);
}

export function createSeedList(body: {
  name: string;
  description?: string;
  addresses?: { address: string; provider?: string }[];
}): Promise<SeedList> {
  return req<SeedList>("/v1/seed-lists", {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export function deleteSeedList(id: string): Promise<void> {
  return req<void>(`/v1/seed-lists/${id}`, { method: "DELETE" });
}

export function listSeedRuns(): Promise<{ runs: SeedRun[] }> {
  return req<{ runs: SeedRun[] }>("/v1/seed-tests");
}

export function startSeedRun(body: {
  list_id: string;
  name?: string;
  from_address?: string;
  ip_pool?: string;
}): Promise<SeedRun> {
  return req<SeedRun>("/v1/seed-tests", {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export function getSeedRun(id: string): Promise<SeedRunDetail> {
  return req<SeedRunDetail>(`/v1/seed-tests/${id}`);
}

import { getToken } from "@/lib/api";

const API_BASE = process.env.NEXT_PUBLIC_API_BASE ?? "http://localhost:8080";

// ─── Types ──────────────────────────────────────────────────────────────────

export type BimiReadiness =
  | "ready"
  | "partial"
  | "vmc_expired"
  | "blocked"
  | "not_configured"
  | "unknown";

export type ChecklistStatus = "ok" | "warn" | "fail";

export interface BimiChecklistItem {
  code: string;
  label: string;
  status: ChecklistStatus;
  detail: string;
}

export interface BimiSummaryItem {
  domain_id: string;
  domain: string;
  readiness_state: BimiReadiness;
  dmarc_enforced: boolean;
  logo_url: string;
  vmc_url: string;
  vmc_expiry: string | null;
  checked_at: string | null;
}

export interface BimiDetail {
  domain_id: string;
  domain: string;
  readiness_state: BimiReadiness;
  record: string;
  logo_url: string;
  vmc_url: string;
  vmc_expiry: string | null;
  dmarc_enforced: boolean;
  checklist: BimiChecklistItem[];
  checked_at: string | null;
}

// ─── Fetch helpers ──────────────────────────────────────────────────────────

function authHeaders(): HeadersInit {
  const token = getToken();
  return token ? { Authorization: `Bearer ${token}` } : {};
}

async function apiFetch<T>(path: string, options?: RequestInit): Promise<T> {
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
  return res.json() as Promise<T>;
}

export function listBimi(): Promise<{ domains: BimiSummaryItem[] }> {
  return apiFetch<{ domains: BimiSummaryItem[] }>("/v1/bimi");
}

export function getBimiDetail(domainId: string): Promise<BimiDetail> {
  return apiFetch<BimiDetail>(`/v1/domains/${domainId}/bimi`);
}

export function recheckBimi(domainId: string): Promise<BimiDetail> {
  return apiFetch<BimiDetail>(`/v1/domains/${domainId}/bimi/recheck`, {
    method: "POST",
  });
}

// API client for the Bounce classification + Suppression list feature.
// Imports shared auth/fetch helpers from @/lib/api (never edits it).

import { apiGet, apiPost, apiDelete, getToken } from "@/lib/api";
import { API_BASE } from "@/lib/api-base";


// ─── Bounces ────────────────────────────────────────────────────────────────

export type BounceCategory =
  | "hard"
  | "soft"
  | "block"
  | "spam_block"
  | "invalid_recipient"
  | "mailbox_full"
  | "reputation"
  | "unknown";

export type BounceWindow = "1h" | "24h" | "7d" | "30d";

export interface ClassifiedBounce {
  event_time: string;
  from_domain: string;
  recipient_domain: string;
  recipient_hash: string;
  provider: string;
  smtp_code: number;
  enhanced_status: string;
  response_text: string;
  category: BounceCategory;
  suppressed: boolean;
}

export interface DomainBounceRate {
  domain: string;
  total: number;
  bounced: number;
  rate: number;
}

export interface CategoryCount {
  category: BounceCategory;
  count: number;
}

export interface BouncesResponse {
  window: BounceWindow;
  categories: CategoryCount[];
  domain_rates: DomainBounceRate[];
  recent: ClassifiedBounce[];
}

export function getBounces(window: BounceWindow): Promise<BouncesResponse> {
  return apiGet<BouncesResponse>(`/v1/bounces?window=${encodeURIComponent(window)}`);
}

// ─── Suppression list ─────────────────────────────────────────────────────────

export interface SuppressionEntry {
  recipient_hash: string;
  reason: string;
  category: string;
  source: string;
  created_at: string;
  expires_at: string | null;
}

export interface SuppressionResponse {
  entries: SuppressionEntry[];
  count: number;
}

export function getSuppression(includeExpired = false): Promise<SuppressionResponse> {
  const q = includeExpired ? "?include_expired=true" : "";
  return apiGet<SuppressionResponse>(`/v1/suppression${q}`);
}

export interface AddSuppressionInput {
  recipient_hash?: string;
  email?: string;
  reason?: string;
  category?: string;
  source?: string;
  ttl_hours?: number;
}

export function addSuppression(input: AddSuppressionInput): Promise<SuppressionEntry> {
  return apiPost<SuppressionEntry>("/v1/suppression", input);
}

export function deleteSuppression(hash: string): Promise<{ deleted: boolean }> {
  return apiDelete<{ deleted: boolean }>(`/v1/suppression/${encodeURIComponent(hash)}`);
}

// getSuppressionExport fetches the raw relay-sync artifact (text/plain). Goes through a raw
// fetch (not apiGet) because the response is text, not JSON.
export async function getSuppressionExport(format: "plain" | "postfix"): Promise<string> {
  const token = getToken();
  const res = await fetch(`${API_BASE}/v1/suppression/export?format=${format}`, {
    headers: token ? { Authorization: `Bearer ${token}` } : {},
    cache: "no-store",
  });
  if (!res.ok) {
    throw new Error(`export failed: HTTP ${res.status}`);
  }
  return res.text();
}

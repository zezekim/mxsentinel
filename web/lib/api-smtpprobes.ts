// API client for the synthetic SMTP probing feature. Imports auth/base helpers from the
// shared @/lib/api module (which owns the token + fetch plumbing) without modifying it.
import { apiGet } from "@/lib/api";

export type ProbeMode = "plain" | "starttls" | "implicit_tls";

export interface ProbeEndpoint {
  host: string;
  port: number;
  mode: ProbeMode;
}

export interface ProbeTLS {
  negotiated: boolean;
  version?: string;
  cipher?: string;
  chain_valid: boolean;
  cert_subject?: string;
  cert_issuer?: string;
  cert_not_after?: string | null;
  days_until_expiry?: number | null;
  expiring: boolean;
}

export interface ProbeEndpointStatus {
  endpoint: ProbeEndpoint;
  probed: boolean;
  ok: boolean;
  stage?: string;
  error?: string;
  latency_ms: number;
  banner?: string;
  starttls_offered: boolean;
  auth_advertised: boolean;
  auth_mechs?: string[];
  tls?: ProbeTLS | null;
  greylisting: boolean;
  probed_at?: string | null;
}

export interface ProbeSummary {
  total_endpoints: number;
  healthy: number;
  failing: number;
  unprobed: number;
  cert_warnings: number;
}

export interface ProbeStatusResponse {
  probed_at: string | null;
  summary: ProbeSummary;
  endpoints: ProbeEndpointStatus[];
}

export interface ProbePoint {
  probed_at: string;
  endpoint: string;
  ok: boolean;
  latency_ms: number;
  stage?: string;
  cert_days_until_expiry: number;
}

export interface ProbeUptime {
  endpoint: string;
  total: number;
  ok_count: number;
  uptime_pct: number;
  avg_latency_ms: number;
  p95_latency_ms: number;
  max_latency_ms: number;
}

export interface ProbeHistoryResponse {
  endpoint?: string;
  points: ProbePoint[];
  uptime: ProbeUptime[];
  source: string;
}

export function getSMTPProbes(): Promise<ProbeStatusResponse> {
  return apiGet<ProbeStatusResponse>("/v1/smtp-probes");
}

export function getSMTPProbeHistory(params?: {
  endpoint?: string;
  limit?: number;
  since?: string;
}): Promise<ProbeHistoryResponse> {
  const q = new URLSearchParams();
  if (params?.endpoint) q.set("endpoint", params.endpoint);
  if (params?.limit) q.set("limit", String(params.limit));
  if (params?.since) q.set("since", params.since);
  const qs = q.toString();
  return apiGet<ProbeHistoryResponse>(`/v1/smtp-probes/history${qs ? `?${qs}` : ""}`);
}

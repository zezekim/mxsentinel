// API client for the TLS Reporting (TLS-RPT + MTA-STS) feature. It reuses the shared
// fetch/auth helpers from @/lib/api (getToken etc.) rather than duplicating them.
import { apiGet } from "@/lib/api";

export interface MTASTSFinding {
  category: string;
  severity: "info" | "warning" | "critical";
  code: string;
  message: string;
  detail?: Record<string, unknown>;
}

export interface MTASTSDomain {
  id: string;
  domain_id: string;
  domain: string;
  mode: string; // none|testing|enforce|"" (no policy)
  max_age: number;
  mx_hosts: string[];
  checksum: string;
  cert_expiry?: string;
  healthy: boolean;
  findings?: MTASTSFinding[];
  captured_at: string;
}

export interface MTASTSListResponse {
  domains: MTASTSDomain[];
}

export interface TLSRPTReport {
  id: string;
  org_name: string;
  report_id: string;
  domain: string;
  date_begin: string;
  date_end: string;
  policy_count: number;
  success_count: number;
  failure_count: number;
}

export interface TLSRPTFailureType {
  result_type: string;
  failures: number;
}

export interface TLSRPTSummary {
  success: number;
  failure: number;
  success_rate: number;
  by_type: TLSRPTFailureType[];
}

export interface TLSRPTReportsResponse {
  reports: TLSRPTReport[];
  summary: TLSRPTSummary;
}

export function getMTASTSDomains(): Promise<MTASTSListResponse> {
  return apiGet<MTASTSListResponse>("/v1/tls-reporting/mta-sts");
}

export function getTLSRPTReports(domain: string, limit = 50): Promise<TLSRPTReportsResponse> {
  const params = new URLSearchParams({ limit: String(limit) });
  if (domain) params.set("domain", domain);
  return apiGet<TLSRPTReportsResponse>(`/v1/tls-reporting/reports?${params.toString()}`);
}

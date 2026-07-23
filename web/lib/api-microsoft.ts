// API client for the Microsoft SNDS + JMRP endpoints. Reuses the shared auth-aware apiGet from
// @/lib/api (token handling, 401 redirect) without modifying it.
import { apiGet } from "@/lib/api";

export interface SndsTrendPoint {
  date: string;
  filter_result: string;
  trap_hits: number;
  rcpt_count: number;
}

export interface SndsIP {
  ip: string;
  data_date: string;
  filter_result: string;
  complaint_band: string;
  trap_hits: number;
  rcpt_count: number;
  data_count: number;
  message_recipients: number;
  sample_helo: string;
  sample_from: string;
  fetched_at: string;
  trend: SndsTrendPoint[];
}

export interface SndsResponse {
  ips: SndsIP[];
}

export interface JmrpComplaint {
  sender_domain: string;
  sending_ip: string;
  feedback_type: string;
  provider: string;
  complaint_date: string;
  complaint_count: number;
  last_seen: string;
}

export interface JmrpResponse {
  complaints: JmrpComplaint[];
}

export function getSnds(days = 14): Promise<SndsResponse> {
  return apiGet<SndsResponse>(`/v1/microsoft/snds?days=${days}`);
}

export function getJmrp(): Promise<JmrpResponse> {
  return apiGet<JmrpResponse>("/v1/microsoft/jmrp");
}

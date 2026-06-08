const API_BASE =
  process.env.NEXT_PUBLIC_API_BASE ?? "http://localhost:8080";

// ─── Token helpers ────────────────────────────────────────────────────────────

export function getToken(): string {
  if (typeof window !== "undefined") {
    const stored = localStorage.getItem("mxs_token");
    if (stored) return stored;
  }
  return process.env.NEXT_PUBLIC_API_TOKEN ?? "";
}

export function setToken(t: string): void {
  if (typeof window !== "undefined") {
    localStorage.setItem("mxs_token", t);
  }
}

export function clearToken(): void {
  if (typeof window !== "undefined") {
    localStorage.removeItem("mxs_token");
  }
}

function authHeaders(): HeadersInit {
  const token = getToken();
  return token ? { Authorization: `Bearer ${token}` } : {};
}

function handle401(): void {
  clearToken();
  if (typeof window !== "undefined") {
    window.location.href = "/login";
  }
}

async function handleResponse<T>(res: Response): Promise<T> {
  if (res.status === 401) {
    handle401();
    throw new Error("Session expired. Please log in again.");
  }
  if (!res.ok) {
    let msg = `HTTP ${res.status}`;
    try {
      const body = await res.json();
      if (body?.error?.message) msg = body.error.message;
    } catch {
      // ignore parse error
    }
    throw new Error(msg);
  }
  return res.json() as Promise<T>;
}

export async function apiGet<T>(path: string): Promise<T> {
  const res = await fetch(`${API_BASE}${path}`, {
    headers: authHeaders(),
    cache: "no-store",
  });
  return handleResponse<T>(res);
}

async function apiSend<T>(
  method: "POST" | "PUT" | "PATCH" | "DELETE",
  path: string,
  body?: unknown,
): Promise<T> {
  const headers: Record<string, string> = {
    ...(authHeaders() as Record<string, string>),
  };
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
  }
  const res = await fetch(`${API_BASE}${path}`, {
    method,
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
    cache: "no-store",
  });
  return handleResponse<T>(res);
}

export function apiPost<T>(path: string, body?: unknown): Promise<T> {
  return apiSend<T>("POST", path, body);
}

export function apiPut<T>(path: string, body?: unknown): Promise<T> {
  return apiSend<T>("PUT", path, body);
}

export function apiPatch<T>(path: string, body?: unknown): Promise<T> {
  return apiSend<T>("PATCH", path, body);
}

export function apiDelete<T>(path: string): Promise<T> {
  return apiSend<T>("DELETE", path);
}

// ─── Auth ─────────────────────────────────────────────────────────────────────

export interface LoginResponse {
  token: string;
  expires_at: string;
  user: {
    id: string;
    email: string;
    tenant_id: string;
    role: string;
  };
}

export async function login(email: string, password: string): Promise<LoginResponse> {
  const res = await fetch(`${API_BASE}/v1/auth/login`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ email, password }),
    cache: "no-store",
  });
  if (!res.ok) {
    let msg = "Invalid credentials";
    try {
      const body = await res.json();
      if (body?.error?.message) msg = body.error.message;
    } catch {
      // ignore parse error
    }
    throw new Error(msg);
  }
  const data = (await res.json()) as LoginResponse;
  setToken(data.token);
  return data;
}

export async function logout(): Promise<void> {
  try {
    await apiPost<{ ok: boolean }>("/v1/auth/logout");
  } catch {
    // best-effort; clear token regardless
  } finally {
    clearToken();
  }
}

// ─── Me ──────────────────────────────────────────────────────────────────────

export interface Me {
  user_id: string;
  tenant_id: string;
  role: string;
  scopes: string[];
}

export function me(): Promise<Me> {
  return apiGet<Me>("/v1/me");
}

export function changePassword(
  currentPassword: string,
  newPassword: string,
): Promise<{ ok: boolean }> {
  return apiPost<{ ok: boolean }>("/v1/me/password", {
    current_password: currentPassword,
    new_password: newPassword,
  });
}

// ─── Response types ──────────────────────────────────────────────────────────

export type StatusColor = "ok" | "warning" | "critical" | "unknown";
export type OverallStatus = "healthy" | "warning" | "critical" | "unknown";
export type Severity = "info" | "warning" | "critical";

export interface DomainCategories {
  spf: StatusColor;
  dkim: StatusColor;
  dmarc: StatusColor;
  mx: StatusColor;
}

export interface DomainSummary {
  id: string;
  name: string;
  status: string;
  categories: DomainCategories;
  overall: OverallStatus;
  last_checked_at: string | null;
  finding_count: number;
}

export interface DomainsResponse {
  domains: DomainSummary[];
}

export interface Snapshot {
  id: string;
  captured_at: string;
  checksum: string;
  healthy: boolean;
}

export interface DomainHealthResponse {
  domain: { id: string; name: string; status: string };
  snapshot: (Snapshot & { finding_count?: number }) | null;
  categories: DomainCategories;
  overall: OverallStatus;
  findings: Finding[];
}

export interface RecheckResponse extends DomainHealthResponse {
  changed: boolean;
}

export interface Finding {
  category: string;
  severity: Severity;
  code: string;
  message: string;
}

export interface SnapshotSummary {
  id: string;
  captured_at: string;
  checksum: string;
  healthy: boolean;
  finding_count: number;
}

export interface SnapshotsResponse {
  snapshots: SnapshotSummary[];
}

export interface Message {
  event_id: string;
  event_time: string;
  event_type: string;
  outcome: string;
  message_id: string;
  from_domain: string;
  recipient_domain: string;
  provider: string;
  relay_ip: string;
  smtp_code: number;
  enhanced_status: string;
  bounce_class: string;
  response_text: string;
  sasl_username: string;
}

export interface MessagesResponse {
  messages: Message[];
  count: number;
}

export interface DmarcReport {
  id: string;
  org_name: string;
  report_id: string;
  domain: string;
  date_begin: string;
  date_end: string;
  record_count: number;
}

export interface DmarcAlignment {
  total: number;
  dkim_aligned: number;
  spf_aligned: number;
  dkim_pass_rate: number;
  spf_pass_rate: number;
}

export interface DmarcReportsResponse {
  reports: DmarcReport[];
  alignment: DmarcAlignment;
}

// ─── Incidents ────────────────────────────────────────────────────────────────

export type IncidentKind =
  | "rejection_spike"
  | "blacklist"
  | "dns_validation"
  | "other";

export type IncidentStatus = "open" | "acknowledged" | "resolved";

export interface AIRecommendation {
  action: string;
  summary: string;
  target: string;
  priority: string;
}

export interface Incident {
  id: string;
  source_event_id: string;
  kind: IncidentKind;
  severity: Severity;
  domain: string;
  subject: string;
  title: string;
  detail: Record<string, unknown>;
  status: IncidentStatus;
  confidence: number | null;
  created_at: string;
  resolved_at: string | null;
  ai_summary: string | null;
  ai_remediation: AIRecommendation[] | null;
  ai_model: string | null;
  ai_analyzed_at: string | null;
}

export interface IncidentsResponse {
  incidents: Incident[];
}

export interface ListIncidentsParams {
  status?: IncidentStatus | "";
  domain?: string;
  limit?: number;
}

export function listIncidents(params: ListIncidentsParams = {}): Promise<IncidentsResponse> {
  const q = new URLSearchParams({ limit: String(params.limit ?? 50) });
  if (params.status) q.set("status", params.status);
  if (params.domain) q.set("domain", params.domain);
  return apiGet<IncidentsResponse>(`/v1/incidents?${q.toString()}`);
}

export function resolveIncident(id: string): Promise<{ resolved: boolean }> {
  return apiPost<{ resolved: boolean }>(`/v1/incidents/${id}/resolve`);
}

// ─── SMTP submission users ──────────────────────────────────────────────────

export interface SMTPUser {
  id: string;
  username: string;
  domain: string;
  enabled: boolean;
  created_at: string;
}

export interface SMTPUsersResponse {
  users: SMTPUser[];
}

export interface CreateSMTPUserInput {
  username: string;
  password: string;
  domain?: string;
}

export function listSMTPUsers(): Promise<SMTPUsersResponse> {
  return apiGet<SMTPUsersResponse>("/v1/smtp-users");
}

export function createSMTPUser(input: CreateSMTPUserInput): Promise<SMTPUser> {
  return apiPost<SMTPUser>("/v1/smtp-users", input);
}

export function setSMTPUserEnabled(id: string, enabled: boolean): Promise<{ ok: boolean }> {
  return apiPatch<{ ok: boolean }>(`/v1/smtp-users/${id}`, { enabled });
}

export function resetSMTPUserPassword(id: string, password: string): Promise<{ ok: boolean }> {
  return apiPatch<{ ok: boolean }>(`/v1/smtp-users/${id}`, { password });
}

export function deleteSMTPUser(id: string): Promise<{ deleted: boolean }> {
  return apiDelete<{ deleted: boolean }>(`/v1/smtp-users/${id}`);
}

// ─── Mail settings ──────────────────────────────────────────────────────────

export type DMARCPolicy = "none" | "quarantine" | "reject";

export interface MailSettings {
  spf_include: string;
  dkim_selector: string;
  dmarc_policy: DMARCPolicy;
  dmarc_rua: string;
  dmarc_ruf: string;
  relay_host: string;
  relay_port: number;
  resolver_address: string;
  resolver_timeout_secs: number;
}

export interface SettingsResponse {
  settings: MailSettings;
}

export function getSettings(): Promise<SettingsResponse> {
  return apiGet<SettingsResponse>("/v1/settings");
}

export function updateSettings(settings: MailSettings): Promise<SettingsResponse> {
  return apiPut<SettingsResponse>("/v1/settings", settings);
}

// ── Top Senders ───────────────────────────────────────────────────────────

export type SenderMetric = "volume" | "spam" | "rejected";
export type SenderWindow = "1h" | "24h" | "7d" | "30d";

export interface SenderCount {
  key: string;
  count: number;
}

export interface TopSendersResponse {
  metric: SenderMetric;
  window: SenderWindow;
  by_ip: SenderCount[];
  by_sender: SenderCount[];
  by_domain: SenderCount[];
}

export function getTopSenders(
  metric: SenderMetric,
  window: SenderWindow,
): Promise<TopSendersResponse> {
  const q = new URLSearchParams({ metric, window });
  return apiGet<TopSendersResponse>(`/v1/analytics/top-senders?${q.toString()}`);
}

// --- RBL/DNSBL egress IP health (/v1/rbl/status) ---
export interface RBLZoneStatus {
  zone: string;
  listed: boolean;
  reason?: string;
  listed_since?: string | null;
}

export interface RBLIPStatus {
  ip: string;
  healthy: boolean;
  checked: boolean;
  listings: RBLZoneStatus[];
}

export interface RBLSummary {
  total_ips: number;
  healthy: number;
  listed: number;
}

export interface RBLStatusResponse {
  checked_at: string | null;
  summary: RBLSummary;
  ips: RBLIPStatus[];
}

export async function getRBLStatus(): Promise<RBLStatusResponse> {
  return apiGet<RBLStatusResponse>("/v1/rbl/status");
}

// ─── Send-volume anomalies (Velocity) ──────────────────────────────────────────

export interface VolumeAnomaly {
  sender_domain: string;
  observed_hour_count: number;
  baseline: number;
  factor: number;
  detected_at: string;
}

export interface VolumeMover {
  sender_domain: string;
  current: number;
  baseline: number;
  ratio: number;
}

export interface AnomalyRecentResponse {
  anomalies: VolumeAnomaly[];
  top_movers: VolumeMover[];
}

export function getAnomalyRecent(): Promise<AnomalyRecentResponse> {
  return apiGet<AnomalyRecentResponse>(`/v1/anomaly/recent`);
}

// --- Reputation (feedback-loop complaints + Gmail Postmaster) ---

export interface ReputationDomain {
  domain: string;
  complaints_24h: number;
  complaints_total: number;
  postmaster_reputation: string; // "" when no Postmaster data; else HIGH|GOOD|MEDIUM|LOW|BAD
  spam_rate: number | null;
  fetched_at: string | null;
}

export interface ReputationResponse {
  domains: ReputationDomain[];
}

export async function getReputation(): Promise<ReputationResponse> {
  return apiGet<ReputationResponse>("/v1/reputation");
}

// ─── Auth Security (credential-compromise detection) ────────────────────────

export interface AuthSignal {
  signal: string;
  detail: Record<string, unknown>;
  detected_at: string;
}

export interface AuthCredential {
  sasl_username: string;
  recent_signals: AuthSignal[];
  locked: boolean;
  reason?: string;
  locked_at: string | null;
}

export interface AuthSecurityResponse {
  credentials: AuthCredential[];
}

export interface LockCredentialResponse {
  sasl_username: string;
  locked: boolean;
  reenable_via_smtp_users?: boolean;
}

export function getAuthSecurity(): Promise<AuthSecurityResponse> {
  return apiGet<AuthSecurityResponse>("/v1/auth-security");
}

export function lockCredential(
  user: string,
  locked: boolean,
  reason: string,
): Promise<LockCredentialResponse> {
  return apiPost<LockCredentialResponse>(
    `/v1/auth-security/${encodeURIComponent(user)}/lock`,
    { locked, reason },
  );
}

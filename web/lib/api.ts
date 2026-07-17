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

export interface CreateDomainResponse {
  domain: DomainSummary;
}

export function createDomain(name: string): Promise<CreateDomainResponse> {
  return apiPost<CreateDomainResponse>("/v1/domains", { name });
}

export function deleteDomain(id: string): Promise<{ deleted: boolean }> {
  return apiDelete<{ deleted: boolean }>(`/v1/domains/${id}`);
}

export function updateDomain(
  id: string,
  status: "monitored" | "paused",
): Promise<{ ok: boolean }> {
  return apiPatch<{ ok: boolean }>(`/v1/domains/${id}`, { status });
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
  queue_id: string;
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

// ─── Message share links / public trace ────────────────────────────────────────

export interface ShareLink {
  id: string;
  queue_id: string;
  message_id: string;
  label: string;
  url: string;
  path: string;
  token?: string; // present only in the create response, once
  active: boolean;
  view_count: number;
  expires_at: string | null;
  revoked_at: string | null;
  last_viewed_at: string | null;
  created_at: string;
}

export interface SharesResponse {
  shares: ShareLink[];
  count: number;
}

export function createShareLink(
  queueId: string,
  opts?: { label?: string; ttl_hours?: number },
): Promise<ShareLink> {
  return apiPost<ShareLink>(`/v1/messages/${encodeURIComponent(queueId)}/share`, opts ?? {});
}

export function listShareLinks(queueId: string): Promise<SharesResponse> {
  return apiGet<SharesResponse>(`/v1/messages/${encodeURIComponent(queueId)}/shares`);
}

export function revokeShareLink(id: string): Promise<{ revoked: boolean }> {
  return apiDelete<{ revoked: boolean }>(`/v1/messages/shares/${encodeURIComponent(id)}`);
}

export interface TraceEvent {
  event_time: string;
  event_type: string;
  provider: string;
  mx_host: string;
  recipient_domain: string;
  smtp_code: number;
  enhanced_status: string;
  bounce_class: string;
  response_text: string;
}

export interface PublicTrace {
  message_id: string;
  from_domain: string;
  recipient_domain: string;
  provider: string;
  status: string;
  label: string;
  events: TraceEvent[];
  checked_at: string;
}

// getPublicTrace fetches a shared trace WITHOUT auth headers — the token in the URL is the
// only credential. Deliberately does not go through apiGet (which would attach a bearer token
// and redirect to /login on 401).
export async function getPublicTrace(token: string): Promise<PublicTrace> {
  const res = await fetch(`${API_BASE}/v1/trace/${encodeURIComponent(token)}`, {
    cache: "no-store",
  });
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
  return res.json() as Promise<PublicTrace>;
}

export interface DmarcReport {
  id: string;
  org_name: string;
  report_id: string;
  domain: string;
  date_begin: string;
  date_end: string;
  record_count: number;
  pass_count: number;
  fail_count: number;
}

export interface DmarcRecord {
  source_ip: string;
  count: number;
  disposition: string;
  dkim_aligned: boolean;
  spf_aligned: boolean;
  header_from: string;
  envelope_from: string;
}

export interface DmarcRecordsResponse {
  records: DmarcRecord[];
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

export interface DmarcAnalyticsStats {
  domains: number;
  reports: number;
  messages: number;
  pass_messages: number;
  fail_messages: number;
  pass_rate: number;
}

export interface DmarcTopFailingDomain {
  domain: string;
  fails: number;
  total: number;
  fail_rate: number;
}

export interface DmarcAnalyticsResponse {
  stats: DmarcAnalyticsStats;
  top_failing_domains: DmarcTopFailingDomain[];
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

// ─── Alert Rules ──────────────────────────────────────────────────────────────

export type AlertSignal = "dns_breakage" | "rejection_spike" | "blacklist_hit" | "tls_failure" | "complaint_spike" | "bounce_rate";

export interface AlertRule {
  id: string;
  name: string;
  signal: AlertSignal;
  condition: Record<string, unknown>;
  channel_ids: string[];
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

export interface NotificationChannel {
  id: string;
  kind: "email" | "slack" | "webhook" | "pagerduty";
  name: string;
  config: Record<string, unknown>;
  enabled: boolean;
  created_at: string;
}

export interface AlertEvent {
  id: string;
  rule_id: string;
  state: "open" | "acknowledged" | "resolved" | "muted";
  triggered_at: string;
  resolved_at: string | null;
  payload: Record<string, unknown>;
  created_at: string;
}

export const listAlertRules = () => apiGet<{ alert_rules: AlertRule[] }>("/v1/alert-rules");
export const createAlertRule = (body: { name: string; signal: AlertSignal; condition: Record<string,unknown>; channel_ids: string[] }) => apiPost<AlertRule>("/v1/alert-rules", body);
export const updateAlertRule = (id: string, body: { enabled: boolean; condition?: Record<string,unknown>; channel_ids?: string[] }) => apiPatch<AlertRule>(`/v1/alert-rules/${id}`, body);
export const deleteAlertRule = (id: string) => apiDelete<{ deleted: boolean }>(`/v1/alert-rules/${id}`);
export const listNotificationChannels = () => apiGet<{ channels: NotificationChannel[] }>("/v1/notification-channels");
export const createNotificationChannel = (body: { kind: string; name: string; config: Record<string,unknown> }) => apiPost<NotificationChannel>("/v1/notification-channels", body);
export const deleteNotificationChannel = (id: string) => apiDelete<{ deleted: boolean }>(`/v1/notification-channels/${id}`);
export const listAlertEvents = () => apiGet<{ alert_events: AlertEvent[] }>("/v1/alert-events");

// ─── Heatmap ──────────────────────────────────────────────────────────────────

export interface HeatmapRow {
  provider: string;
  recipient_domain: string;
  delivered: number;
  deferred: number;
  bounced: number;
  rejected: number;
  total: number;
  acceptance_rate: number;
}

export const getHeatmap = (window: string, view: string) => apiGet<{ view: string; window: string; rows: HeatmapRow[] }>(`/v1/heatmap?window=${window}&view=${view}`);

// ─── Warm-up ──────────────────────────────────────────────────────────────────

export interface WarmupPlan {
  id: string;
  name: string;
  ip_pool: string;
  target_daily_volume: number;
  current_day: number;
  stage: "ramping" | "established" | "paused";
  started_at: string;
  completed_at: string | null;
  created_at: string;
}

export interface WarmupDayStat {
  day_number: number;
  stat_date: string;
  sent: number;
  delivered: number;
  bounced: number;
  deferred: number;
  acceptance_rate: number | null;
}

export const listWarmupPlans = () => apiGet<{ plans: WarmupPlan[] }>("/v1/warmup");
export const createWarmupPlan = (body: { name: string; ip_pool: string; target_daily_volume: number }) => apiPost<WarmupPlan>("/v1/warmup", body);
export const getWarmupPlan = (id: string) => apiGet<{ plan: WarmupPlan; day_stats: WarmupDayStat[]; recommended_today: number }>(`/v1/warmup/${id}`);
export const updateWarmupPlan = (id: string, body: { stage: string }) => apiPatch<WarmupPlan>(`/v1/warmup/${id}`, body);
export const deleteWarmupPlan = (id: string) => apiDelete<{ deleted: boolean }>(`/v1/warmup/${id}`);

// ─── Reports ──────────────────────────────────────────────────────────────────

export interface ReportSchedule {
  id: string;
  name: string;
  frequency: "daily" | "weekly" | "monthly";
  recipients: string[];
  include_dns: boolean;
  include_dmarc: boolean;
  include_incidents: boolean;
  include_reputation: boolean;
  enabled: boolean;
  last_sent_at: string | null;
  next_run_at: string | null;
  created_at: string;
}

export const listReports = () => apiGet<{ schedules: ReportSchedule[] }>("/v1/reports");
export const createReport = (body: Omit<ReportSchedule, "id" | "created_at" | "last_sent_at" | "next_run_at">) => apiPost<ReportSchedule>("/v1/reports", body);
export const updateReport = (id: string, body: Partial<ReportSchedule>) => apiPut<ReportSchedule>(`/v1/reports/${id}`, body);
export const deleteReport = (id: string) => apiDelete<{ deleted: boolean }>(`/v1/reports/${id}`);
export const sendReportNow = (id: string) => apiPost<{ queued: boolean }>(`/v1/reports/${id}/send-now`);

// ─── DKIM Rotation ────────────────────────────────────────────────────────────

export interface DKIMPlan {
  id: string;
  domain_id: string;
  selector_old: string;
  selector_new: string;
  public_key_new: string;
  key_bits: number;
  stage: "pending" | "published" | "testing" | "active" | "retired";
  dns_verified: boolean;
  test_passed: boolean;
  dns_record: string;
  created_at: string;
  activated_at: string | null;
  retired_at: string | null;
}

export const listDKIMPlans = (domainId: string) => apiGet<{ plans: DKIMPlan[] }>(`/v1/domains/${domainId}/dkim/plans`);
export const createDKIMPlan = (domainId: string, body: { selector?: string; key_bits?: number }) => apiPost<DKIMPlan>(`/v1/domains/${domainId}/dkim/plans`, body);
export const updateDKIMPlan = (domainId: string, planId: string, body: { stage?: string; dns_verified?: boolean; test_passed?: boolean }) => apiPatch<DKIMPlan>(`/v1/domains/${domainId}/dkim/plans/${planId}`, body);
export const deleteDKIMPlan = (domainId: string, planId: string) => apiDelete<{ deleted: boolean }>(`/v1/domains/${domainId}/dkim/plans/${planId}`);

// ─── Integrations: cPanel / WHMCS ─────────────────────────────────────────────

export interface CpanelServer {
  id: string;
  label: string;
  hostname: string;
  port: number;
  username: string;
  verify_ssl: boolean;
  sync_interval: number;
  last_synced_at: string | null;
  sync_status: "pending" | "syncing" | "ok" | "error";
  sync_error: string;
  account_count: number;
  created_at: string;
}

export interface CpanelAccount {
  id: string;
  server_id: string;
  username: string;
  primary_domain: string;
  owner_email: string;
  plan: string;
  suspended: boolean;
  disk_used_mb: number;
  synced_at: string;
}

export interface WhmcsConnection {
  id: string;
  label: string;
  api_url: string;
  api_identifier: string;
  push_frequency: "daily" | "weekly";
  push_metric_fields: string[];
  enabled: boolean;
  last_pushed_at: string | null;
  created_at: string;
}

export interface WhmcsPushLogEntry {
  id: string;
  pushed_at: string;
  accounts_pushed: number;
  status: "ok" | "partial" | "error";
  error_detail: string;
  period_start: string;
  period_end: string;
}

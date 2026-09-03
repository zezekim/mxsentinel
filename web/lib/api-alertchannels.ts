// API client for alert delivery channels. Thin wrappers over the shared fetch helpers in
// @/lib/api so this feature does not modify the shared module.

import { apiGet, apiPost, apiPatch, apiDelete } from "@/lib/api";

export type ChannelType = "slack" | "webhook" | "pagerduty" | "email" | "telegram";

export interface AlertChannel {
  id: string;
  type: ChannelType;
  name: string;
  // Secrets are redacted to "***" by the server; non-secret fields are returned as-is.
  config: Record<string, unknown>;
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

interface AlertChannelsResponse {
  alert_channels: AlertChannel[];
}

export function listAlertChannels(): Promise<AlertChannelsResponse> {
  return apiGet<AlertChannelsResponse>("/v1/alert-channels");
}

export function createAlertChannel(body: {
  type: ChannelType;
  name: string;
  config: Record<string, unknown>;
}): Promise<{ id: string }> {
  return apiPost<{ id: string }>("/v1/alert-channels", body);
}

export function updateAlertChannel(
  id: string,
  body: Partial<{ name: string; enabled: boolean; config: Record<string, unknown> }>,
): Promise<{ ok: boolean }> {
  return apiPatch<{ ok: boolean }>(`/v1/alert-channels/${id}`, body);
}

export function deleteAlertChannel(id: string): Promise<{ ok: boolean }> {
  return apiDelete<{ ok: boolean }>(`/v1/alert-channels/${id}`);
}

// loginAlertsOn reads the non-secret opt-in flag off a channel's config.
export function loginAlertsOn(ch: AlertChannel): boolean {
  return ch.config?.login_alerts === true || ch.config?.login_alerts === "true";
}

// incidentAlertsOn reports whether the channel receives the firing-incident feed. Absent
// flag = on, matching the server default, so channels predating the flag read as on.
export function incidentAlertsOn(ch: AlertChannel): boolean {
  const v = ch.config?.incident_alerts;
  return v === undefined || v === null || v === true || v === "true";
}

// setIncidentAlerts flips the incident-feed flag. Secrets stay redacted in the round-trip;
// the server keeps their stored values.
export function setIncidentAlerts(ch: AlertChannel, on: boolean): Promise<{ ok: boolean }> {
  return updateAlertChannel(ch.id, { config: { ...ch.config, incident_alerts: on } });
}

// setLoginAlerts flips the opt-in flag. The config is sent back as the API rendered it —
// secrets stay redacted ("***") and the server keeps their stored values.
export function setLoginAlerts(ch: AlertChannel, on: boolean): Promise<{ ok: boolean }> {
  return updateAlertChannel(ch.id, { config: { ...ch.config, login_alerts: on } });
}

export function testAlertChannel(id: string): Promise<{ ok: boolean; status: string }> {
  return apiPost<{ ok: boolean; status: string }>(`/v1/alert-channels/${id}/test`, {});
}

// buildChannelConfig maps the single free-text field in the create form to the config shape
// each channel type expects. Matches the server-side config keys.
export function buildChannelConfig(
  type: ChannelType,
  primary: string,
  extra: {
    signingSecret?: string;
    recipients?: string;
    chatId?: string;
    loginAlerts?: boolean;
    incidentAlerts?: boolean;
  },
): Record<string, unknown> {
  const cfg = buildTypeConfig(type, primary, extra);
  // Non-secret routing flags: which feeds this channel carries.
  if (extra.loginAlerts) cfg.login_alerts = true;
  if (extra.incidentAlerts === false) cfg.incident_alerts = false;
  return cfg;
}

function buildTypeConfig(
  type: ChannelType,
  primary: string,
  extra: { signingSecret?: string; recipients?: string; chatId?: string },
): Record<string, unknown> {
  switch (type) {
    case "slack":
      return { webhook_url: primary };
    case "webhook": {
      const cfg: Record<string, unknown> = { url: primary };
      if (extra.signingSecret) cfg.signing_secret = extra.signingSecret;
      return cfg;
    }
    case "pagerduty":
      return { routing_key: primary };
    case "telegram":
      return { bot_token: primary, chat_id: extra.chatId ?? "" };
    case "email":
      return {
        to: (extra.recipients ?? primary)
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean),
      };
    default:
      return { url: primary };
  }
}

export function channelTypeLabel(type: ChannelType): string {
  switch (type) {
    case "slack":
      return "Slack";
    case "webhook":
      return "Webhook";
    case "pagerduty":
      return "PagerDuty";
    case "email":
      return "Email";
    case "telegram":
      return "Telegram";
    default:
      return type;
  }
}

export function primaryFieldLabel(type: ChannelType): string {
  switch (type) {
    case "slack":
      return "Incoming Webhook URL";
    case "webhook":
      return "Endpoint URL";
    case "pagerduty":
      return "Integration (Routing) Key";
    case "email":
      return "Recipient address(es), comma-separated";
    case "telegram":
      return "Bot Token (from @BotFather)";
    default:
      return "URL";
  }
}

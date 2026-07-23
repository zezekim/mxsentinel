// API client for alert delivery channels. Thin wrappers over the shared fetch helpers in
// @/lib/api so this feature does not modify the shared module.

import { apiGet, apiPost, apiPatch, apiDelete } from "@/lib/api";

export type ChannelType = "slack" | "webhook" | "pagerduty" | "email";

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

export function testAlertChannel(id: string): Promise<{ ok: boolean; status: string }> {
  return apiPost<{ ok: boolean; status: string }>(`/v1/alert-channels/${id}/test`, {});
}

// buildChannelConfig maps the single free-text field in the create form to the config shape
// each channel type expects. Matches the server-side config keys.
export function buildChannelConfig(
  type: ChannelType,
  primary: string,
  extra: { signingSecret?: string; recipients?: string },
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
    default:
      return "URL";
  }
}

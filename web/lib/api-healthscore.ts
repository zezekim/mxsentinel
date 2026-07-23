// API client for the Deliverability Health Score feature. It reuses the shared auth-aware
// fetch helper (apiGet) from @/lib/api so tokens/401 handling stay in one place; it deliberately
// adds nothing to @/lib/api.
import { apiGet } from "@/lib/api";

export interface HealthComponent {
  name: string;
  label: string;
  present: boolean;
  score: number;
  weight: number;
  impact: number;
  detail: string;
}

export interface HealthScoreDomain {
  domain_id: string;
  domain: string;
  score: number;
  grade: string;
  has_data: boolean;
  coverage: number;
  pending: boolean;
  computed_at: string | null;
  components?: HealthComponent[];
}

export interface HealthScoreSummary {
  tenant: {
    score: number;
    grade: string;
    domains_total: number;
    domains_rated: number;
  };
  domains: HealthScoreDomain[];
}

export interface HealthScorePoint {
  score: number;
  grade: string;
  has_data: boolean;
  coverage: number;
  computed_at: string;
}

export function getHealthScoreSummary(): Promise<HealthScoreSummary> {
  return apiGet<HealthScoreSummary>("/v1/health-score");
}

export function getDomainHealthScore(id: string): Promise<HealthScoreDomain> {
  return apiGet<HealthScoreDomain>(`/v1/domains/${id}/health-score`);
}

export function getDomainHealthScoreHistory(
  id: string,
  limit = 100,
): Promise<{ history: HealthScorePoint[] }> {
  return apiGet<{ history: HealthScorePoint[] }>(
    `/v1/domains/${id}/health-score/history?limit=${limit}`,
  );
}

// gradeBadgeClass maps a letter grade to one of the shared badge-* classes from globals.css.
export function gradeBadgeClass(grade: string): string {
  switch (grade.toUpperCase()) {
    case "A":
    case "B":
      return "badge-ok";
    case "C":
    case "D":
      return "badge-warning";
    case "F":
      return "badge-critical";
    default:
      return "badge-unknown";
  }
}

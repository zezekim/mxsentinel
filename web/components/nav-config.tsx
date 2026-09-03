// Navigation model for the service.
//
// Kept separate from the components that render it so the shell can build
// breadcrumbs from the same source of truth the sidebar is drawn from.

import type { ReactElement } from "react";

// Inline SVG icons (24x24 viewBox, stroke-based, single weight)
export const Icons = {
  mail: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><rect width="20" height="16" x="2" y="4" rx="2"/><path d="m22 7-8.97 5.7a1.94 1.94 0 0 1-2.06 0L2 7"/></svg>,
  chartBar: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><line x1="18" x2="18" y1="20" y2="10"/><line x1="12" x2="12" y1="20" y2="4"/><line x1="6" x2="6" y1="20" y2="14"/></svg>,
  grid: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="3" width="7" height="7" rx="1"/><rect x="14" y="14" width="7" height="7" rx="1"/><rect x="3" y="14" width="7" height="7" rx="1"/></svg>,
  zap: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><polygon points="13 2 3 14 12 14 11 22 21 10 12 10 13 2"/></svg>,
  globe: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><circle cx="12" cy="12" r="10"/><path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/><path d="M2 12h20"/></svg>,
  server: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><rect x="2" y="2" width="20" height="8" rx="2" ry="2"/><rect x="2" y="14" width="20" height="8" rx="2" ry="2"/><line x1="6" x2="6.01" y1="6" y2="6"/><line x1="6" x2="6.01" y1="18" y2="18"/></svg>,
  shield: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/></svg>,
  lock: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><rect x="3" y="11" width="18" height="11" rx="2" ry="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/></svg>,
  users: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M22 21v-2a4 4 0 0 0-3-3.87"/><path d="M16 3.13a4 4 0 0 1 0 7.75"/></svg>,
  fileCheck: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="M14.5 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7.5L14.5 2z"/><polyline points="14 2 14 8 20 8"/><polyline points="9 15 11 17 15 13"/></svg>,
  bell: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9"/><path d="M10.3 21a1.94 1.94 0 0 0 3.4 0"/></svg>,
  alertTriangle: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3Z"/><line x1="12" x2="12.01" y1="9" y2="13"/><line x1="12" x2="12.01" y1="17" y2="17"/></svg>,
  flame: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="M8.5 14.5A2.5 2.5 0 0 0 11 12c0-1.38-.5-2-1-3-1.072-2.143-.224-4.054 2-6 .5 2.5 2 4.9 4 6.5 2 1.6 3 3.5 3 5.5a7 7 0 1 1-14 0c0-1.153.433-2.294 1-3a2.5 2.5 0 0 0 2.5 3z"/></svg>,
  calendar: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><rect x="3" y="4" width="18" height="18" rx="2" ry="2"/><line x1="16" x2="16" y1="2" y2="6"/><line x1="8" x2="8" y1="2" y2="6"/><line x1="3" x2="21" y1="10" y2="10"/></svg>,
  settings: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z"/><circle cx="12" cy="12" r="3"/></svg>,
  user: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2"/><circle cx="12" cy="7" r="4"/></svg>,
  book: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="M4 19.5v-15A2.5 2.5 0 0 1 6.5 2H20v20H6.5a2.5 2.5 0 0 1 0-5H20"/></svg>,
  plug: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="M12 22v-5"/><path d="M9 8V2"/><path d="M15 8V2"/><path d="M18 8H6a1 1 0 0 0-1 1v4a5 5 0 0 0 10 0V9a1 1 0 0 0-1-1Z"/></svg>,
};

// The service mark used in the masthead and on the sign-in page.
export const BrandMark = (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.25" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d="M12 2L2 7l10 5 10-5-10-5z"/>
    <path d="M2 17l10 5 10-5"/>
    <path d="M2 12l10 5 10-5"/>
  </svg>
);

export interface NavLink {
  href: string;
  label: string;
  icon: ReactElement;
  // Roles that must not see this entry, and must not reach the route behind it.
  // Presentation only — the API is what actually enforces permissions; this just
  // keeps viewers out of pages that would be read-only or empty for them anyway.
  hideFor?: string[];
}

export interface NavSection {
  label: string;
  links: NavLink[];
}

export const navSections: NavSection[] = [
  {
    label: "Overview",
    links: [
      { href: "/", label: "Dashboard", icon: Icons.chartBar },
    ],
  },
  {
    label: "Deliverability",
    links: [
      { href: "/messages", label: "Messages", icon: Icons.mail },
      { href: "/senders", label: "Top Senders", icon: Icons.chartBar },
      { href: "/heatmap", label: "Heatmap", icon: Icons.grid },
      { href: "/velocity", label: "Velocity", icon: Icons.zap },
      { href: "/health-score", label: "Health Score", icon: Icons.chartBar },
      { href: "/suppression", label: "Bounces", icon: Icons.mail },
      { href: "/inbox-placement", label: "Inbox Placement", icon: Icons.mail },
    ],
  },
  {
    label: "Infrastructure",
    links: [
      { href: "/domains", label: "Domains", icon: Icons.globe, hideFor: ["viewer"] },
      { href: "/ip-health", label: "IP Health", icon: Icons.server },
      { href: "/reputation", label: "Reputation", icon: Icons.shield },
      { href: "/smtp-probes", label: "SMTP Probes", icon: Icons.server },
      { href: "/microsoft", label: "Microsoft", icon: Icons.globe },
    ],
  },
  {
    label: "Security",
    links: [
      { href: "/auth-security", label: "Auth Security", icon: Icons.lock },
      { href: "/smtp-users", label: "SMTP Users", icon: Icons.users },
      { href: "/dmarc", label: "DMARC", icon: Icons.fileCheck },
      { href: "/tls-reporting", label: "TLS Reporting", icon: Icons.lock },
      { href: "/bimi", label: "BIMI", icon: Icons.shield },
    ],
  },
  {
    label: "Operations",
    links: [
      { href: "/incidents", label: "Incidents", icon: Icons.bell },
      { href: "/alerts", label: "Alerts", icon: Icons.alertTriangle },
      { href: "/alert-channels", label: "Alert Channels", icon: Icons.bell },
      { href: "/warmup", label: "Warm-up", icon: Icons.flame },
    ],
  },
  {
    label: "Reporting",
    links: [
      { href: "/reports", label: "Reports", icon: Icons.calendar },
      { href: "/domain-report", label: "Domain Report", icon: Icons.fileCheck },
      { href: "/ask", label: "Ask Logs", icon: Icons.chartBar },
    ],
  },
  {
    label: "Integrations",
    links: [
      { href: "/integrations", label: "cPanel / WHMCS", icon: Icons.plug },
    ],
  },
  {
    label: "Account",
    links: [
      { href: "/settings", label: "Settings", icon: Icons.settings, hideFor: ["viewer"] },
      { href: "/account", label: "Account", icon: Icons.user },
      { href: "/docs", label: "Docs", icon: Icons.book },
    ],
  },
];

// ── Role visibility ───────────────────────────────────────────────────────────

// `null` means the role is not known yet: the shell has not read the cached login role
// or heard back from /v1/me. Callers must distinguish it from a known role — hiding a
// link while unknown is the safe default, but rendering a "no access" notice on that
// basis would show it to everyone for a frame on first paint.
export type Role = string | null;

function matchesRoute(href: string, pathname: string): boolean {
  return pathname === href || pathname.startsWith(href + "/");
}

function linkVisibleTo(link: NavLink, role: Role): boolean {
  if (!link.hideFor) return true;
  if (role === null) return false; // unknown → least privilege
  return !link.hideFor.includes(role);
}

/** navSections with role-gated links removed, dropping any section left empty. */
export function navSectionsFor(role: Role): NavSection[] {
  return navSections
    .map((s) => ({ ...s, links: s.links.filter((l) => linkVisibleTo(l, role)) }))
    .filter((s) => s.links.length > 0);
}

/**
 * True when some role is gated out of `pathname` — the nav entry's own route or anything
 * nested under it, so /domains/<id> is covered along with /domains. Role-independent, so
 * the shell can tell "this route needs a role check" from "this route is always open"
 * and only wait on the role for the former.
 */
export function routeIsGated(pathname: string): boolean {
  return navSections.some((s) =>
    s.links.some((l) => l.hideFor !== undefined && matchesRoute(l.href, pathname)),
  );
}

/**
 * True when `role` must not reach `pathname`. An unknown role returns false — "not yet
 * known" is not "denied"; pair it with routeIsGated to hold rendering until it resolves.
 */
export function routeHiddenFor(role: Role, pathname: string): boolean {
  if (role === null) return false;
  return navSections.some((s) =>
    s.links.some(
      (l) =>
        l.hideFor !== undefined &&
        !linkVisibleTo(l, role) &&
        matchesRoute(l.href, pathname),
    ),
  );
}

// ── Breadcrumbs ───────────────────────────────────────────────────────────────

export interface Crumb {
  label: string;
  href?: string;
}

function isNavRoute(href: string): boolean {
  return navSections.some((s) => s.links.some((l) => l.href === href));
}

// Identifiers (domain UUIDs, queue IDs) are shortened; ordinary slugs are
// turned back into words.
function prettifySegment(segment: string): string {
  let seg = segment;
  try {
    seg = decodeURIComponent(segment);
  } catch {
    // keep the raw segment if it is not valid percent-encoding
  }
  if (seg.length > 16 || /^[0-9a-f]{8}-[0-9a-f]{4}-/i.test(seg)) {
    return seg.slice(0, 8) + "…";
  }
  const words = seg.replace(/[-_]+/g, " ");
  return words.charAt(0).toUpperCase() + words.slice(1);
}

/**
 * Builds the breadcrumb trail for a pathname: Home › <section> › <page>,
 * with a trailing identifier segment where the route has one.
 */
export function crumbsFor(pathname: string): Crumb[] {
  if (pathname === "/") return [{ label: "Home" }];

  const home: Crumb = { label: "Home", href: "/" };

  for (const section of navSections) {
    for (const link of section.links) {
      if (link.href === "/") continue;
      if (pathname !== link.href && !pathname.startsWith(link.href + "/")) {
        continue;
      }
      const crumbs: Crumb[] = [home, { label: section.label }];
      if (pathname === link.href) {
        crumbs.push({ label: link.label });
        return crumbs;
      }
      crumbs.push({ label: link.label, href: link.href });
      const rest = pathname.slice(link.href.length + 1).split("/");
      rest.forEach((seg, i) => {
        const href = link.href + "/" + rest.slice(0, i + 1).join("/");
        const isLast = i === rest.length - 1;
        crumbs.push(
          isLast || !isNavRoute(href)
            ? { label: prettifySegment(seg) }
            : { label: prettifySegment(seg), href },
        );
      });
      return crumbs;
    }
  }

  // Route that is not in the navigation (e.g. /domains/<id>) — derive the
  // trail from the path itself, linking only segments that are real routes.
  const segments = pathname.split("/").filter(Boolean);
  const crumbs: Crumb[] = [home];
  segments.forEach((seg, i) => {
    const href = "/" + segments.slice(0, i + 1).join("/");
    const isLast = i === segments.length - 1;
    crumbs.push(
      isLast || !isNavRoute(href)
        ? { label: prettifySegment(seg) }
        : { label: prettifySegment(seg), href },
    );
  });
  return crumbs;
}

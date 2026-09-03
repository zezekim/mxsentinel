"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useEffect, useState } from "react";
import NavLinks from "@/components/NavLinks";
import {
  BrandMark,
  crumbsFor,
  routeHiddenFor,
  routeIsGated,
  type Role,
} from "@/components/nav-config";
import {
  me,
  logout,
  getToken,
  clearToken,
  getRole,
  setRole,
  type Me,
} from "@/lib/api";

// Routes that render standalone, without the authenticated service shell:
// the sign-in screen, the public per-tenant status pages, and the public
// per-message trace (delivery tracking) pages.
const BARE_ROUTES = ["/login", "/status", "/trace"];

// Tenant identifiers are UUIDs; the masthead shows the leading block and
// keeps the full value in the title attribute.
function shortId(id: string): string {
  return id.length > 8 ? id.slice(0, 8) : id;
}

export default function AppShell({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  const isBare =
    !!pathname &&
    BARE_ROUTES.some((r) => pathname === r || pathname.startsWith(r + "/"));

  const [user, setUser] = useState<Me | null>(null);
  // null until known. Seeded from the cached login role so role-gated navigation settles
  // on the first client paint, then reconciled with /v1/me in case it changed since
  // sign-in. Sessions predating the cache stay null until /v1/me answers.
  const [role, setLocalRole] = useState<Role>(null);

  useEffect(() => {
    const cached = getRole();
    if (cached) setLocalRole(cached);
  }, []);

  useEffect(() => {
    if (isBare || !getToken()) return;
    me()
      .then((m) => {
        setUser(m);
        setLocalRole(m.role);
        setRole(m.role);
      })
      .catch(() => {
        // identity is supplementary chrome; the API layer handles 401s
      });
  }, [isBare]);

  async function handleSignOut() {
    try {
      await logout();
    } catch {
      // API unreachable — clear the token locally and redirect anyway
    }
    clearToken();
    window.location.href = "/login";
  }

  if (isBare) {
    return <>{children}</>;
  }

  const path = pathname ?? "/";
  const crumbs = crumbsFor(path);
  // A gated route reached directly (typed URL, bookmark, stale link) renders a notice
  // in place of the page. The API refuses the underlying calls regardless. Only gated
  // routes wait on the role, so every other page still paints immediately.
  const awaitingRole = role === null && routeIsGated(path);
  const hidden = routeHiddenFor(role, path);

  return (
    <div className="app-shell">
      <a href="#main-content" className="skip-link">
        Skip to main content
      </a>

      <header className="masthead">
        <div className="masthead-inner">
          <Link href="/" className="masthead-brand">
            <span className="brand-icon">{BrandMark}</span>
            <span className="masthead-service">
              <span className="brand-name">MX Sentinel</span>
              <span className="brand-desc">
                Email infrastructure observability
              </span>
            </span>
          </Link>

          <div className="masthead-utility">
            {user && (
              <dl className="utility-meta">
                <div>
                  <dt>Role</dt>
                  <dd>{user.role.toUpperCase()}</dd>
                </div>
                <div>
                  <dt>Tenant</dt>
                  <dd className="mono" title={user.tenant_id}>
                    {shortId(user.tenant_id)}
                  </dd>
                </div>
              </dl>
            )}
            <button
              type="button"
              className="btn-signout"
              onClick={handleSignOut}
            >
              Sign out
            </button>
          </div>
        </div>
      </header>

      <div className="app-body">
        <aside className="sidebar">
          <nav className="sidebar-nav" aria-label="Service sections">
            <NavLinks role={role} />
          </nav>
        </aside>

        <div className="content-wrap">
          <nav className="breadcrumb-bar" aria-label="Breadcrumb">
            <ol className="breadcrumb">
              {crumbs.map((crumb, i) => {
                const isLast = i === crumbs.length - 1;
                return (
                  <li key={`${crumb.label}-${i}`}>
                    {isLast ? (
                      <span aria-current="page">{crumb.label}</span>
                    ) : crumb.href ? (
                      <Link href={crumb.href}>{crumb.label}</Link>
                    ) : (
                      <span>{crumb.label}</span>
                    )}
                  </li>
                );
              })}
            </ol>
          </nav>

          <main id="main-content" tabIndex={-1}>
            {awaitingRole ? null : hidden ? (
              <section className="card">
                <h1>Not available</h1>
                <p>
                  Your account does not have access to this page. Contact an
                  administrator if you need it.
                </p>
                <p>
                  <Link href="/" className="linklike">
                    Back to dashboard →
                  </Link>
                </p>
              </section>
            ) : (
              children
            )}
          </main>

          <footer className="app-footer">
            <div className="app-footer-inner">
              <ul className="app-footer-links">
                <li>
                  <Link href="/docs">Documentation</Link>
                </li>
                <li>
                  <Link href="/integrations">Integrations</Link>
                </li>
                {role !== null && !routeHiddenFor(role, "/settings") && (
                  <li>
                    <Link href="/settings">Settings</Link>
                  </li>
                )}
                <li>
                  <Link href="/account">Account</Link>
                </li>
              </ul>
              <p className="app-footer-meta">
                MX Sentinel — email infrastructure observability
              </p>
            </div>
          </footer>
        </div>
      </div>
    </div>
  );
}

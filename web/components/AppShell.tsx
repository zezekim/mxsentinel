"use client";

import { usePathname } from "next/navigation";
import NavLinks from "@/components/NavLinks";

// Routes that render standalone, without the authenticated sidebar shell:
// the login screen, the public per-tenant status pages, and the public
// per-message trace (delivery tracking) pages.
const BARE_ROUTES = ["/login", "/status", "/trace"];

export default function AppShell({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  const isBare =
    !!pathname &&
    BARE_ROUTES.some((r) => pathname === r || pathname.startsWith(r + "/"));

  if (isBare) {
    return <>{children}</>;
  }

  return (
    <div className="app-shell">
      <aside className="sidebar">
        <a href="/" className="sidebar-brand">
          <span className="brand-icon">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
              <path d="M12 2L2 7l10 5 10-5-10-5z"/>
              <path d="M2 17l10 5 10-5"/>
              <path d="M2 12l10 5 10-5"/>
            </svg>
          </span>
          <span className="brand-name">MX Sentinel</span>
        </a>
        <nav className="sidebar-nav">
          <NavLinks />
        </nav>
      </aside>
      <div className="content-wrap">
        <main>{children}</main>
      </div>
    </div>
  );
}

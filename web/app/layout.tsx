import type { Metadata } from "next";
import "./globals.css";
import NavLinks from "@/components/NavLinks";

export const metadata: Metadata = {
  title: "MX Sentinel",
  description: "Email infrastructure observability",
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en">
      <body>
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
      </body>
    </html>
  );
}

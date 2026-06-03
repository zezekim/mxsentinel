"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useEffect, useState } from "react";
import { me, logout, getToken, type Me } from "@/lib/api";

const links = [
  { href: "/", label: "Domains" },
  { href: "/messages", label: "Messages" },
  { href: "/dmarc", label: "DMARC" },
  { href: "/incidents", label: "Incidents" },
  { href: "/smtp-users", label: "SMTP Users" },
  { href: "/settings", label: "Settings" },
  { href: "/account", label: "Account" },
];

export default function NavLinks() {
  const pathname = usePathname();
  const [user, setUser] = useState<Me | null>(null);

  useEffect(() => {
    // Only fetch /me when a token is present
    if (!getToken()) return;
    me()
      .then(setUser)
      .catch(() => {
        // If /me fails (e.g. token invalid) the 401 handler in apiGet
        // will clear the token and redirect to /login automatically.
      });
  }, []);

  async function handleLogout() {
    await logout();
    window.location.href = "/login";
  }

  return (
    <>
      {links.map((l) => (
        <Link
          key={l.href}
          href={l.href}
          className={
            pathname === l.href ||
            (l.href !== "/" && pathname.startsWith(l.href))
              ? "active"
              : ""
          }
        >
          {l.label}
        </Link>
      ))}

      <div className="nav-user-info">
        {user && (
          <span className="nav-user-email" title={user.user_id}>
            {user.role}
          </span>
        )}
        <button
          className="nav-logout-btn"
          onClick={handleLogout}
          type="button"
        >
          Log out
        </button>
      </div>
    </>
  );
}

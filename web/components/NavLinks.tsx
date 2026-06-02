"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

const links = [
  { href: "/", label: "Domains" },
  { href: "/messages", label: "Messages" },
  { href: "/dmarc", label: "DMARC" },
];

export default function NavLinks() {
  const pathname = usePathname();
  return (
    <>
      {links.map((l) => (
        <Link
          key={l.href}
          href={l.href}
          className={pathname === l.href || (l.href !== "/" && pathname.startsWith(l.href)) ? "active" : ""}
        >
          {l.label}
        </Link>
      ))}
    </>
  );
}

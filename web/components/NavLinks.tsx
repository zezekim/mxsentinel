"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { navSectionsFor, type Role } from "@/components/nav-config";

export default function NavLinks({ role }: { role: Role }) {
  const pathname = usePathname();
  const sections = navSectionsFor(role);

  function isActive(href: string) {
    if (href === "/") return pathname === "/";
    return pathname === href || pathname?.startsWith(href + "/");
  }

  return (
    <>
      {sections.map((section) => (
        <div key={section.label} className="nav-section">
          <div className="nav-section-label">{section.label}</div>
          {section.links.map((link) => {
            const active = isActive(link.href);
            return (
              <Link
                key={link.href}
                href={link.href}
                className={"nav-link" + (active ? " active" : "")}
                aria-current={active ? "page" : undefined}
              >
                {link.icon}
                {link.label}
              </Link>
            );
          })}
        </div>
      ))}
    </>
  );
}

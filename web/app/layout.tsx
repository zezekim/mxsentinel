import type { Metadata } from "next";
import "./globals.css";
import NavLinks from "@/components/NavLinks";

export const metadata: Metadata = {
  title: "MX Sentinel",
  description: "Email deliverability monitoring dashboard",
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en">
      <body>
        <header>
          <a href="/" className="brand">
            MX Sentinel
          </a>
          <nav>
            <NavLinks />
          </nav>
        </header>
        <main>{children}</main>
      </body>
    </html>
  );
}

import type { Metadata, Viewport } from "next";
import "./globals.css";
import AppShell from "@/components/AppShell";

export const metadata: Metadata = {
  title: {
    default: "MX Sentinel",
    template: "%s — MX Sentinel",
  },
  description: "Email infrastructure observability",
};

// Matches the masthead so mobile browser chrome sits flush with the service.
export const viewport: Viewport = {
  themeColor: "#26374a",
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en">
      <body>
        <AppShell>{children}</AppShell>
      </body>
    </html>
  );
}

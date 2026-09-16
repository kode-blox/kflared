import { Inter } from "next/font/google";
import { Provider } from "@/components/provider";
import type { Metadata } from "next";
import type { ReactNode } from "react";
import "./global.css";

const inter = Inter({
  subsets: ["latin"],
});

export const metadata: Metadata = {
  metadataBase: new URL("https://kflared.kodeblox.com"),
  icons: {
    icon: "/images/favicon.svg",
  },
  title: {
    default: "KFlared Docs",
    template: "%s | KFlared Docs",
  },
  description: "Documentation for the KFlared Gateway controller for Cloudflare Tunnel.",
};

export default function Layout({ children }: { children: ReactNode }) {
  return (
    <html lang="en" className={inter.className} suppressHydrationWarning>
      <body className="flex flex-col min-h-screen">
        <Provider>{children}</Provider>
      </body>
    </html>
  );
}

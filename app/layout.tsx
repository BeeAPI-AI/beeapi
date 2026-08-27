import type { Metadata } from "next";
import { Geist, Geist_Mono } from "next/font/google";
import "./globals.css";

const geistSans = Geist({
  variable: "--font-geist-sans",
  subsets: ["latin"],
});

const geistMono = Geist_Mono({
  variable: "--font-geist-mono",
  subsets: ["latin"],
});

export const metadata: Metadata = {
  metadataBase: new URL("https://getbeeapi.com"),
  title: "GetBeeAPI — BeeAPI CLI 与访问线路优选",
  description: "面向 BeeAPI 用户的跨平台 CLI：自动检测 beeapi.ai、beeapi.dev，选择可访问且更快的线路，并配置 Codex、Claude Code 等 AI 工具。",
  openGraph: {
    title: "GetBeeAPI — BeeAPI，从一条命令开始",
    description: "BeeAPI 访问入口、线路优选、网页登录、Key 选择与多智能体配置。",
    type: "website",
    locale: "zh_CN",
    url: "https://getbeeapi.com",
    siteName: "GetBeeAPI",
    images: [{
      url: "/og-v2.png",
      width: 1200,
      height: 630,
      alt: "GetBeeAPI — 一条命令，接好 BeeAPI 与你的 AI 工具",
    }],
  },
  twitter: {
    card: "summary_large_image",
    title: "GetBeeAPI — BeeAPI，从一条命令开始",
    description: "线路优选 → 账户连接 → 工具识别 → 配置完成",
    images: ["/og-v2.png"],
  },
  icons: {
    icon: "/favicon.png",
    shortcut: "/favicon.png",
  },
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="zh-CN">
      <body
        className={`${geistSans.variable} ${geistMono.variable} antialiased`}
      >
        {children}
      </body>
    </html>
  );
}

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
  title: "GetBeeAPI — 为现有 AI 工具快速配置 BeeAPI",
  description: "一条命令为 Claude Code、Claude Desktop、Codex、Gemini CLI、Grok Build、OpenCode、OpenClaw 与 Hermes 配置 BeeAPI。",
  openGraph: {
    title: "GetBeeAPI — 把 BeeAPI 接入你已经在用的 AI 工具",
    description: "识别本地工具，选择 BeeAPI Key 与模型，备份后完成配置。",
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
    title: "GetBeeAPI — 为现有 AI 工具快速配置 BeeAPI",
    description: "8 项工具适配 · 自动识别 · 独立备份 · 一键回滚",
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

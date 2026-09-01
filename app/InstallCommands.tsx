"use client";

import { useState } from "react";

type Locale = "zh" | "en";

const commands = [
  {
    id: "curl",
    label: "curl",
    platform: "Linux / macOS",
    command: "curl -fsSL https://getbeeapi.com/install.sh | sh",
  },
  {
    id: "powershell",
    label: "PowerShell",
    platform: "Windows",
    command: "irm https://getbeeapi.com/install.ps1 | iex",
  },
];

export default function InstallCommands({ locale = "zh" }: { locale?: Locale }) {
  const [active, setActive] = useState(commands[0].id);
  const [copied, setCopied] = useState(false);
  const current = commands.find((item) => item.id === active) ?? commands[0];
  const text = (zh: string, en: string) => locale === "zh" ? zh : en;

  async function copyCommand() {
    await navigator.clipboard.writeText(current.command);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1600);
  }

  return (
    <div className="command-wrap">
      <div className="command-card">
        <div className="command-tabs" role="tablist" aria-label={text("选择操作系统", "Choose an operating system")}>
          {commands.map((item) => (
            <button
              key={item.id}
              className={item.id === active ? "active" : ""}
              onClick={() => { setActive(item.id); setCopied(false); }}
              role="tab"
              aria-selected={item.id === active}
              type="button"
            >
              {item.label}
            </button>
          ))}
        </div>
        <div className="command-line" role="tabpanel">
          <code>{current.command}</code>
          <button className={copied ? "copy-button copied" : "copy-button"} onClick={copyCommand} type="button" aria-label={text("复制安装命令", "Copy installation command")}>
            {copied ? (
              <span aria-live="polite">{text("已复制", "Copied")}</span>
            ) : (
              <svg viewBox="0 0 20 20" aria-hidden="true">
                <rect x="7" y="3" width="9" height="11" rx="1.5" />
                <path d="M13 14v1.5A1.5 1.5 0 0 1 11.5 17h-7A1.5 1.5 0 0 1 3 15.5v-9A1.5 1.5 0 0 1 4.5 5H6" />
              </svg>
            )}
          </button>
        </div>
      </div>
      <span className="command-platform">{text("适用于", "For")} {current.platform}</span>
    </div>
  );
}

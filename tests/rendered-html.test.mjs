import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("exports the GetBeeAPI product page for static hosting", async () => {
  const html = await readFile(
    new URL("../dist/client/index.html", import.meta.url),
    "utf8",
  );
  assert.match(html, /<title>GetBeeAPI/);
  assert.match(html, /把 BeeAPI，接入你/);
  assert.match(html, /不替代你的 AI 工具/);
  assert.match(html, /https:\/\/getbeeapi\.com\/og-v2\.png/);
  assert.doesNotMatch(html, /https:\/\/getbeeapi\.com\/og\.png/);
  assert.match(html, /curl -fsSL https:\/\/getbeeapi\.com\/install\.sh \| sh/);
  assert.match(html, /BeeAPI 访问入口/);
  assert.match(html, /https:\/\/beeapi\.ai/);
  assert.match(html, /https:\/\/beeapi\.dev/);
  assert.match(html, /网页登录并批准设备/);
  assert.match(html, /直接粘贴 API Key/);
  assert.match(html, /账户当前可用的现有 Key/);
  assert.match(html, /批准此设备/);
  assert.match(html, /不创建重复密钥/);
  assert.match(html, /切换命名方案/);
  assert.match(html, /账户余额与每个 Key 的可用状态/);
  assert.doesNotMatch(html, /设备专用 Key/);
  assert.doesNotMatch(html, /选择 1–10 个/);
  for (const tool of [
    "Claude Code",
    "Claude Desktop",
    "Codex",
    "Gemini CLI",
    "Grok Build",
    "OpenCode",
    "OpenClaw",
    "Hermes",
  ]) {
    assert.match(html, new RegExp(tool));
  }
  assert.match(html, /当前版本 · 8 项适配/);
  assert.match(html, /普通 Claude 聊天仍使用 Anthropic 账户模型/);

  const ordered = ["选择可用入口", "连接 BeeAPI", "配置本地工具"];
  let previous = -1;
  for (const text of ordered) {
    const index = html.indexOf(text, previous + 1);
    assert.ok(index > previous, `${text} should appear in the fixed setup order`);
    previous = index;
  }
});

test("ships matching verified installers", async () => {
  const [shell, powerShell, component] = await Promise.all([
    readFile(new URL("../public/install.sh", import.meta.url), "utf8"),
    readFile(new URL("../public/install.ps1", import.meta.url), "utf8"),
    readFile(new URL("../app/InstallCommands.tsx", import.meta.url), "utf8"),
  ]);

  assert.match(shell, /SHA-256 verification failed/);
  assert.match(shell, /getbeeapi\.com\/releases\/latest\/download/);
  assert.match(shell, /github\.com\/BeeAPI-AI\/beeapi\/releases/);
  assert.match(shell, /\( : <\/dev\/tty \) 2>\/dev\/null/);
  assert.match(shell, /"\$install_dir\/beeapi" <\/dev\/tty/);
  assert.match(shell, /getbeeapi PATH/);
  assert.match(shell, /cannot enforce HTTPS-only redirects/);
  assert.match(powerShell, /Get-FileHash -Algorithm SHA256/);
  assert.match(powerShell, /getbeeapi\.com\/releases\/latest\/download/);
  assert.match(powerShell, /github\.com\/BeeAPI-AI\/beeapi\/releases/);
  assert.match(powerShell, /PROCESSOR_ARCHITEW6432/);
  assert.match(powerShell, /PROCESSOR_ARCHITECTURE/);
  assert.match(powerShell, /SecurityProtocolType\]::Tls12/);
  assert.doesNotMatch(powerShell, /RuntimeInformation\]::OSArchitecture\.ToString/);
  assert.match(powerShell, /& \$Target/);
  assert.match(component, /irm https:\/\/getbeeapi\.com\/install\.ps1 \| iex/);
});

test("ships the versioned social preview at Open Graph dimensions", async () => {
  const image = await readFile(
    new URL("../public/og-v2.png", import.meta.url),
  );

  assert.deepEqual([...image.subarray(0, 8)], [137, 80, 78, 71, 13, 10, 26, 10]);
  assert.equal(image.readUInt32BE(16), 1200);
  assert.equal(image.readUInt32BE(20), 630);
});

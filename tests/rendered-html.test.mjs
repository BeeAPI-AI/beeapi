import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("exports the GetBeeAPI product page for static hosting", async () => {
  const html = await readFile(
    new URL("../dist/client/index.html", import.meta.url),
    "utf8",
  );
  assert.match(html, /<title>GetBeeAPI/);
  assert.match(html, /BeeAPI，从一条命令开始/);
  assert.match(html, /curl -fsSL https:\/\/getbeeapi\.com\/install\.sh \| sh/);
  assert.match(html, /BeeAPI 访问入口/);
  assert.match(html, /https:\/\/beeapi\.ai/);
  assert.match(html, /https:\/\/beeapi\.dev/);
  assert.match(html, /网页登录并选择 Key/);
  assert.match(html, /直接粘贴 API Key/);

  const ordered = ["线路优选", "登录并选择 Key", "发现本地工具", "完成配置"];
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
  assert.match(shell, /github\.com\/BeeAPI-AI\/beeapi\/releases/);
  assert.match(shell, /exec "\$install_dir\/beeapi" <\/dev\/tty/);
  assert.match(powerShell, /Get-FileHash -Algorithm SHA256/);
  assert.match(powerShell, /github\.com\/BeeAPI-AI\/beeapi\/releases/);
  assert.match(powerShell, /& \$Target/);
  assert.match(component, /irm https:\/\/getbeeapi\.com\/install\.ps1 \| iex/);
});

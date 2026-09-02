import assert from "node:assert/strict";
import test from "node:test";

import { parseLatestReleaseTag, resolveReleaseRoute } from "../worker/releases.ts";

test("release cache only proxies fixed BeeAPI artifacts", () => {
  const metadata = resolveReleaseRoute(new URL("https://getbeeapi.com/releases/latest.json"));
  assert.deepEqual(metadata, {
    upstream: "https://api.github.com/repos/BeeAPI-AI/beeapi/releases/latest",
    cacheSeconds: 60,
    immutable: false,
    latestRedirectFallback: "https://github.com/BeeAPI-AI/beeapi/releases/latest",
  });

  const latest = resolveReleaseRoute(new URL("https://getbeeapi.com/releases/latest/download/beeapi_linux_amd64.tar.gz"));
  assert.deepEqual(latest, {
    upstream: "https://github.com/BeeAPI-AI/beeapi/releases/latest/download/beeapi_linux_amd64.tar.gz",
    cacheSeconds: 300,
    immutable: false,
  });

  const versioned = resolveReleaseRoute(new URL("https://getbeeapi.com/releases/v1.2.3/download/beeapi_windows_arm64.zip.sha256"));
  assert.equal(versioned?.upstream, "https://github.com/BeeAPI-AI/beeapi/releases/download/v1.2.3/beeapi_windows_arm64.zip.sha256");
  assert.equal(versioned?.immutable, true);
});

test("latest release redirect fallback only accepts this repository's semver tags", () => {
  assert.equal(parseLatestReleaseTag("https://github.com/BeeAPI-AI/beeapi/releases/tag/v0.5.0"), "v0.5.0");
  assert.equal(parseLatestReleaseTag("/BeeAPI-AI/beeapi/releases/tag/v1.2.3-rc.1"), "v1.2.3-rc.1");
  for (const location of [
    "https://attacker.example/BeeAPI-AI/beeapi/releases/tag/v0.5.0",
    "https://github.com/other/repo/releases/tag/v0.5.0",
    "https://github.com/BeeAPI-AI/beeapi/releases/tag/latest",
    "https://github.com/BeeAPI-AI/beeapi/releases/tag/v0.5.0%2Fmalicious",
    "https://github.com/BeeAPI-AI/beeapi/releases/tag/v0.5.0?next=attacker",
  ]) {
    assert.equal(parseLatestReleaseTag(location), null, location);
  }
});

test("release cache supports fixed CFST metadata and artifacts", () => {
  const latest = resolveReleaseRoute(new URL("https://getbeeapi.com/releases/cfst/latest.json"));
  assert.equal(latest?.upstream, "https://api.github.com/repos/XIU2/CloudflareSpeedTest/releases/latest");
  const fallback = JSON.parse(latest?.fallback?.body ?? "null");
  assert.equal(fallback.tag_name, "v2.3.5");
  assert.equal(fallback.assets.length, 6);
  assert.match(fallback.assets[2].digest, /^sha256:[a-f0-9]{64}$/);
  assert.equal(
    resolveReleaseRoute(new URL("https://getbeeapi.com/releases/cfst/v2.3.5/cfst_linux_amd64.tar.gz"))?.upstream,
    "https://github.com/XIU2/CloudflareSpeedTest/releases/download/v2.3.5/cfst_linux_amd64.tar.gz",
  );
});

test("release cache rejects arbitrary proxy paths", () => {
  for (const raw of [
    "https://getbeeapi.com/releases/latest/download/../../private",
    "https://getbeeapi.com/releases/latest/download/evil_linux_amd64.tar.gz",
    "https://getbeeapi.com/releases/latest.json?target=https://example.com",
    "https://getbeeapi.com/releases/cfst/latest.json?target=https://example.com",
    "https://getbeeapi.com/releases/cfst/not-a-version/cfst_linux_amd64.tar.gz",
  ]) {
    assert.equal(resolveReleaseRoute(new URL(raw)), null, raw);
  }
});

import assert from "node:assert/strict";
import test from "node:test";
import {
  handleInstallStatsRequest,
  parseInstallEvent,
} from "../worker/install-stats.ts";

class MemoryPreparedStatement {
  constructor(database, sql, values = []) {
    this.database = database;
    this.sql = sql;
    this.values = values;
  }

  bind(...values) {
    return new MemoryPreparedStatement(this.database, this.sql, values);
  }

  async run() {
    assert.match(this.sql, /INSERT OR IGNORE INTO installation_events/);
    const [eventId, version, os, arch, source, installer] = this.values;
    if (this.database.events.has(eventId)) return { meta: { changes: 0 } };
    this.database.events.set(eventId, {
      event_id: eventId,
      installed_at: new Date().toISOString(),
      version,
      os,
      arch,
      source,
      installer,
    });
    return { meta: { changes: 1 } };
  }

  async first() {
    assert.match(this.sql, /COUNT\(\*\) AS successful_installs/);
    const events = [...this.database.events.values()];
    return {
      successful_installs: events.length,
      getbeeapi_installs: events.filter((event) => event.source === "getbeeapi").length,
      github_installs: events.filter((event) => event.source === "github").length,
      custom_installs: events.filter((event) => event.source === "custom").length,
      updated_at: events.at(-1)?.installed_at ?? null,
    };
  }
}

class MemoryD1 {
  events = new Map();

  prepare(sql) {
    return new MemoryPreparedStatement(this, sql);
  }
}

const installEvent = {
  event_id: "0123456789abcdef0123456789abcdef",
  version: "v0.4.0",
  os: "linux",
  arch: "amd64",
  source: "getbeeapi",
  installer: "shell",
};

function eventRequest(event = installEvent) {
  return new Request("https://getbeeapi.com/api/install-events", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(event),
  });
}

test("validates anonymous installation event fields", () => {
  assert.deepEqual(parseInstallEvent(installEvent), installEvent);
  assert.equal(parseInstallEvent({ ...installEvent, event_id: "predictable" }), null);
  assert.equal(parseInstallEvent({ ...installEvent, source: "release-page-view" }), null);
  assert.equal(parseInstallEvent({ ...installEvent, os: "freebsd" }), null);
});

test("records verified events idempotently and groups their actual download source", async () => {
  const database = new MemoryD1();
  const env = { DB: database };

  const first = await handleInstallStatsRequest(eventRequest(), env);
  assert.equal(first.status, 200);
  assert.deepEqual(await first.json(), { recorded: true });

  const duplicate = await handleInstallStatsRequest(eventRequest(), env);
  assert.deepEqual(await duplicate.json(), { recorded: false });

  const github = await handleInstallStatsRequest(
    eventRequest({
      ...installEvent,
      event_id: "fedcba9876543210fedcba9876543210",
      os: "windows",
      source: "github",
      installer: "powershell",
    }),
    env,
  );
  assert.deepEqual(await github.json(), { recorded: true });

  const response = await handleInstallStatsRequest(
    new Request("https://getbeeapi.com/api/install-stats"),
    env,
  );
  assert.equal(response.status, 200);
  assert.deepEqual(await response.json(), {
    metric: "verified_installations",
    successful_installs: 2,
    by_source: { getbeeapi: 1, github: 1, custom: 0 },
    updated_at: [...database.events.values()].at(-1).installed_at,
  });
});

test("rejects malformed and oversized event bodies before writing", async () => {
  const env = { DB: new MemoryD1() };
  const malformed = await handleInstallStatsRequest(
    new Request("https://getbeeapi.com/api/install-events", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: "not-json",
    }),
    env,
  );
  assert.equal(malformed.status, 400);

  const oversized = await handleInstallStatsRequest(
    new Request("https://getbeeapi.com/api/install-events", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ padding: "x".repeat(3_000) }),
    }),
    env,
  );
  assert.equal(oversized.status, 413);
  assert.equal(env.DB.events.size, 0);
});

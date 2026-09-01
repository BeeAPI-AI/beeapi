const MAX_EVENT_BODY_BYTES = 2_048;

const validOperatingSystems = new Set(["linux", "darwin", "windows"]);
const validArchitectures = new Set(["amd64", "arm64"]);
const validSources = new Set(["getbeeapi", "github", "custom"]);
const validInstallers = new Set(["shell", "powershell"]);

type InstallEvent = {
  event_id: string;
  version: string;
  os: string;
  arch: string;
  source: string;
  installer: string;
};

type StatsRow = {
  successful_installs: number;
  getbeeapi_installs: number;
  github_installs: number;
  custom_installs: number;
  updated_at: string | null;
};

type StatsEnv = Pick<Env, "DB">;

class BodyTooLargeError extends Error {}

function json(payload: unknown, status: number, cacheControl = "no-store"): Response {
  return Response.json(payload, {
    status,
    headers: {
      "Cache-Control": cacheControl,
      "Content-Type": "application/json; charset=utf-8",
      "X-Content-Type-Options": "nosniff",
    },
  });
}

function apiError(status: number, code: string, message: string): Response {
  return json({ error: code, error_description: message }, status);
}

async function readBoundedBody(request: Request): Promise<string> {
  const contentLength = request.headers.get("content-length");
  if (contentLength && Number(contentLength) > MAX_EVENT_BODY_BYTES) {
    throw new BodyTooLargeError("request body is too large");
  }
  if (!request.body) return "";

  const reader = request.body.getReader();
  const chunks: Uint8Array[] = [];
  let total = 0;
  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    total += value.byteLength;
    if (total > MAX_EVENT_BODY_BYTES) {
      await reader.cancel();
      throw new BodyTooLargeError("request body is too large");
    }
    chunks.push(value);
  }

  const body = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    body.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return new TextDecoder().decode(body);
}

export function parseInstallEvent(value: unknown): InstallEvent | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const candidate = value as Record<string, unknown>;
  const event = {
    event_id: candidate.event_id,
    version: candidate.version,
    os: candidate.os,
    arch: candidate.arch,
    source: candidate.source,
    installer: candidate.installer,
  };
  if (Object.values(event).some((field) => typeof field !== "string")) return null;

  const typed = event as InstallEvent;
  if (!/^[a-f0-9]{32}$/.test(typed.event_id)) return null;
  if (!/^(?:dev|v?\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?)$/.test(typed.version)) return null;
  if (!validOperatingSystems.has(typed.os)) return null;
  if (!validArchitectures.has(typed.arch)) return null;
  if (!validSources.has(typed.source)) return null;
  if (!validInstallers.has(typed.installer)) return null;
  return typed;
}

async function recordInstall(request: Request, env: StatsEnv): Promise<Response> {
  if (!request.headers.get("content-type")?.toLowerCase().startsWith("application/json")) {
    return apiError(415, "unsupported_media_type", "content-type must be application/json");
  }

  let rawBody: string;
  try {
    rawBody = await readBoundedBody(request);
  } catch (error) {
    if (error instanceof BodyTooLargeError) {
      return apiError(413, "request_too_large", error.message);
    }
    throw error;
  }

  let decoded: unknown;
  try {
    decoded = JSON.parse(rawBody);
  } catch {
    return apiError(400, "invalid_request", "request body must be valid JSON");
  }
  const event = parseInstallEvent(decoded);
  if (!event) {
    return apiError(400, "invalid_request", "installation event fields are invalid");
  }

  const result = await env.DB.prepare(
    `INSERT OR IGNORE INTO installation_events
      (event_id, version, os, arch, source, installer)
     VALUES (?, ?, ?, ?, ?, ?)`,
  )
    .bind(event.event_id, event.version, event.os, event.arch, event.source, event.installer)
    .run();

  return json({ recorded: result.meta.changes === 1 }, 200);
}

async function readStats(env: StatsEnv, headOnly: boolean): Promise<Response> {
  const row = await env.DB.prepare(
    `SELECT
       COUNT(*) AS successful_installs,
       COALESCE(SUM(CASE WHEN source = 'getbeeapi' THEN 1 ELSE 0 END), 0) AS getbeeapi_installs,
       COALESCE(SUM(CASE WHEN source = 'github' THEN 1 ELSE 0 END), 0) AS github_installs,
       COALESCE(SUM(CASE WHEN source = 'custom' THEN 1 ELSE 0 END), 0) AS custom_installs,
       MAX(installed_at) AS updated_at
     FROM installation_events`,
  ).first<StatsRow>();

  const response = json(
    {
      metric: "verified_installations",
      successful_installs: Number(row?.successful_installs ?? 0),
      by_source: {
        getbeeapi: Number(row?.getbeeapi_installs ?? 0),
        github: Number(row?.github_installs ?? 0),
        custom: Number(row?.custom_installs ?? 0),
      },
      updated_at: row?.updated_at ?? null,
    },
    200,
    "public, max-age=30, s-maxage=60, stale-while-revalidate=300",
  );
  return headOnly ? new Response(null, response) : response;
}

export async function handleInstallStatsRequest(request: Request, env: StatsEnv): Promise<Response> {
  const { pathname } = new URL(request.url);
  try {
    if (pathname === "/api/install-events") {
      if (request.method !== "POST") {
        return json(
          { error: "method_not_allowed", error_description: "use POST for installation events" },
          405,
        );
      }
      return await recordInstall(request, env);
    }

    if (pathname === "/api/install-stats") {
      if (request.method !== "GET" && request.method !== "HEAD") {
        return json(
          { error: "method_not_allowed", error_description: "use GET for installation statistics" },
          405,
        );
      }
      return await readStats(env, request.method === "HEAD");
    }

    return apiError(404, "not_found", "API route not found");
  } catch (error) {
    console.error(JSON.stringify({ event: "install_stats_failed", pathname, error: String(error) }));
    return apiError(500, "internal_error", "installation statistics are temporarily unavailable");
  }
}

/** Cloudflare Worker entry point for getbeeapi.com. */
import { handleImageOptimization, DEFAULT_DEVICE_SIZES, DEFAULT_IMAGE_SIZES } from "vinext/server/image-optimization";
import handler from "vinext/server/app-router-entry";
import { handleInstallStatsRequest } from "./install-stats";
import { resolveReleaseRoute } from "./releases";

const releaseResponseHeaders = [
  "content-disposition",
  "content-type",
  "etag",
  "last-modified",
] as const;

type CloudflareImageFormat = "image/avif" | "image/webp" | "image/jpeg" | "image/png" | "image/gif";

interface WorkerExecutionContext {
  waitUntil(promise: Promise<unknown>): void;
}

function defaultEdgeCache(): Cache {
  return (caches as CacheStorage & { readonly default: Cache }).default;
}

function cloudflareImageFormat(format: string): CloudflareImageFormat {
  switch (format) {
    case "image/avif":
    case "image/webp":
    case "image/jpeg":
    case "image/png":
    case "image/gif":
      return format;
    default:
      return "image/jpeg";
  }
}

async function fetchRelease(request: Request, ctx: WorkerExecutionContext): Promise<Response | null> {
  const route = resolveReleaseRoute(new URL(request.url));
  if (!route) return null;
  if (request.method !== "GET") {
    return new Response("Method Not Allowed", { status: 405, headers: { Allow: "GET" } });
  }

  const cacheKey = new Request(request.url, { method: "GET" });
  const cached = await defaultEdgeCache().match(cacheKey);
  if (cached) return cached;

  const fallbackResponse = (): Response | null => {
    if (!route.fallback) return null;
    const response = new Response(route.fallback.body, {
      status: 200,
      headers: {
        "Cache-Control": `public, max-age=${route.cacheSeconds}`,
        "Content-Type": route.fallback.contentType,
        "X-Content-Type-Options": "nosniff",
        "X-GetBeeAPI-Fallback": "1",
      },
    });
    ctx.waitUntil(defaultEdgeCache().put(cacheKey, response.clone()));
    return response;
  };

  let upstream: Response;
  try {
    upstream = await fetch(route.upstream, {
      headers: {
        Accept: route.upstream.includes("api.github.com") ? "application/vnd.github+json" : "application/octet-stream",
        "User-Agent": "getbeeapi-release-cache/1",
      },
      redirect: "follow",
    });
  } catch (error) {
    console.error(JSON.stringify({ event: "release_proxy_fetch_failed", upstream: route.upstream, error: String(error) }));
    const fallback = fallbackResponse();
    if (fallback) return fallback;
    return new Response("Release upstream unavailable", { status: 502, headers: { "Cache-Control": "no-store" } });
  }

  if (!upstream.ok || !upstream.body) {
    console.error(JSON.stringify({ event: "release_proxy_bad_status", upstream: route.upstream, status: upstream.status }));
    const fallback = fallbackResponse();
    if (fallback) return fallback;
    return new Response("Release upstream error", { status: upstream.status, headers: { "Cache-Control": "no-store" } });
  }

  const headers = new Headers();
  for (const name of releaseResponseHeaders) {
    const value = upstream.headers.get(name);
    if (value) headers.set(name, value);
  }
  headers.set("Cache-Control", `public, max-age=${route.cacheSeconds}${route.immutable ? ", immutable" : ""}`);
  headers.set("X-Content-Type-Options", "nosniff");

  // Preserve the upstream stream. The clone tees it to Cloudflare's cache
  // without loading release archives into Worker memory.
  const response = new Response(upstream.body, { status: upstream.status, headers });
  ctx.waitUntil(defaultEdgeCache().put(cacheKey, response.clone()));
  return response;
}

// Image security config. SVG sources with .svg extension auto-skip the
// optimization endpoint on the client side (served directly, no proxy).
// To route SVGs through the optimizer (with security headers), set
// dangerouslyAllowSVG: true in next.config.js and uncomment below:
// const imageConfig: ImageConfig = { dangerouslyAllowSVG: true };

const worker = {
  async fetch(request: Request, env: Env, ctx: WorkerExecutionContext): Promise<Response> {
    const url = new URL(request.url);

    if (url.pathname.startsWith("/releases/")) {
      const release = await fetchRelease(request, ctx);
      if (release) return release;
    }

    if (url.pathname.startsWith("/api/")) {
      return handleInstallStatsRequest(request, env);
    }

    if (url.pathname === "/_vinext/image") {
      const allowedWidths = [...DEFAULT_DEVICE_SIZES, ...DEFAULT_IMAGE_SIZES];
      return handleImageOptimization(request, {
        fetchAsset: (path) => env.ASSETS.fetch(new Request(new URL(path, request.url))),
        transformImage: async (body, { width, format, quality }) => {
          const result = await env.IMAGES.input(body).transform(width > 0 ? { width } : {}).output({
            format: cloudflareImageFormat(format),
            quality,
          });
          return result.response();
        },
      }, allowedWidths);
    }

    return handler.fetch(request, env, ctx);
  },
};

export default worker;

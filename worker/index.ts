/** Cloudflare Worker entry point for getbeeapi.com. */
import { handleImageOptimization, DEFAULT_DEVICE_SIZES, DEFAULT_IMAGE_SIZES } from "vinext/server/image-optimization";
import handler from "vinext/server/app-router-entry";
import { handleInstallStatsRequest } from "./install-stats";
import { isRetiredBeeAPIReleasePath, parseLatestReleaseTag, resolveReleaseRoute } from "./releases";

const releaseResponseHeaders = [
  "content-disposition",
  "content-type",
  "etag",
  "last-modified",
] as const;
const releaseCacheGeneration = "2";

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

  // Use a private cache generation so a Worker deploy can immediately bypass
  // stale responses written by older release-proxy implementations.
  const cacheURL = new URL(request.url);
  cacheURL.searchParams.set("__getbeeapi_release_cache", releaseCacheGeneration);
  const cacheKey = new Request(cacheURL, { method: "GET" });
  const cached = await defaultEdgeCache().match(cacheKey);
  if (cached) return cached;

  const cacheFallback = (response: Response): Response => {
    ctx.waitUntil(defaultEdgeCache().put(cacheKey, response.clone()));
    return response;
  };

  const fallbackResponse = async (): Promise<Response | null> => {
    if (route.fallback) {
      return cacheFallback(new Response(route.fallback.body, {
        status: 200,
        headers: {
          "Cache-Control": `public, max-age=${route.cacheSeconds}`,
          "Content-Type": route.fallback.contentType,
          "X-Content-Type-Options": "nosniff",
          "X-GetBeeAPI-Fallback": "static-snapshot",
        },
      }));
    }
    if (!route.latestRedirectFallback) return null;

    let latest: Response;
    try {
      latest = await fetch(route.latestRedirectFallback, {
        headers: {
          Accept: "text/html",
          "User-Agent": "getbeeapi-release-cache/2",
        },
        redirect: "manual",
      });
    } catch (error) {
      console.error(JSON.stringify({ event: "release_redirect_fallback_fetch_failed", upstream: route.latestRedirectFallback, error: String(error) }));
      return null;
    }
    const location = latest.headers.get("location") ?? "";
    if (latest.body) await latest.body.cancel();
    const tag = [301, 302, 303, 307, 308].includes(latest.status) ? parseLatestReleaseTag(location) : null;
    if (!tag) {
      console.error(JSON.stringify({ event: "release_redirect_fallback_invalid", upstream: route.latestRedirectFallback, status: latest.status, location }));
      return null;
    }
    return cacheFallback(Response.json({ tag_name: tag }, {
      status: 200,
      headers: {
        "Cache-Control": `public, max-age=${route.cacheSeconds}`,
        "X-Content-Type-Options": "nosniff",
        "X-GetBeeAPI-Fallback": "github-latest-redirect",
      },
    }));
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
    const fallback = await fallbackResponse();
    if (fallback) return fallback;
    return new Response("Release upstream unavailable", { status: 502, headers: { "Cache-Control": "no-store" } });
  }

  if (!upstream.ok || !upstream.body) {
    console.error(JSON.stringify({ event: "release_proxy_bad_status", upstream: route.upstream, status: upstream.status }));
    if (upstream.body) await upstream.body.cancel();
    const fallback = await fallbackResponse();
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
      if (isRetiredBeeAPIReleasePath(url)) {
        return new Response("This BeeAPI release has been retired. Install the latest release from https://getbeeapi.com/.", {
          status: 410,
          headers: {
            "Cache-Control": "no-store",
            "Content-Type": "text/plain; charset=utf-8",
            "X-Content-Type-Options": "nosniff",
          },
        });
      }
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

import vinext from "vinext";
import { defineConfig } from "vite";

// macOS Seatbelt blocks FSEvents, so Codex previews need polling for HMR.
const isCodexSeatbeltSandbox = process.env.CODEX_SANDBOX === "seatbelt";

const localBindingConfig = {
  main: "./worker/index.ts",
  // Keep local Miniflare and production on a date supported by the bundled
  // Workers runtime. This site does not rely on newer compatibility changes.
  compatibility_date: "2026-05-22",
  compatibility_flags: ["nodejs_compat"],
  routes: [
    {
      pattern: "getbeeapi.com/releases/*",
      zone_name: "getbeeapi.com",
    },
    {
      pattern: "getbeeapi.com/api/*",
      zone_name: "getbeeapi.com",
    },
  ],
  assets: {
    binding: "ASSETS",
    // Release mirrors and installation statistics are dynamic Worker routes.
    // Without this rule the static asset layer returns its own 404 first.
    run_worker_first: ["/releases/*", "/api/*"],
  },
  d1_databases: [
    {
      binding: "DB",
      database_name: "getbeeapi-stats",
      database_id: "e3223388-d9d7-4f97-8a22-0e6f5a2629a6",
      // Vinext writes Wrangler's deploy config to dist/server.
      migrations_dir: "../../drizzle",
    },
  ],
  images: { binding: "IMAGES" },
  r2_buckets: [],
};

export default defineConfig(async () => {
  // Keep Wrangler and Miniflare state project-local. These are non-secret tool
  // settings; application environment belongs in ignored `.env*` files.
  process.env.WRANGLER_WRITE_LOGS ??= "false";
  process.env.WRANGLER_LOG_PATH ??= ".wrangler/logs";
  process.env.MINIFLARE_REGISTRY_PATH ??= ".wrangler/registry";

  // Wrangler snapshots its log path while the Cloudflare plugin is imported.
  const { cloudflare } = await import("@cloudflare/vite-plugin");

  return {
    server: isCodexSeatbeltSandbox
      ? { watch: { useFsEvents: false, usePolling: true } }
      : undefined,
    plugins: [
      vinext(),
      cloudflare({
        viteEnvironment: { name: "rsc", childEnvironments: ["ssr"] },
        config: localBindingConfig,
      }),
    ],
  };
});

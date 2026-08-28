export interface ReleaseRoute {
  upstream: string;
  cacheSeconds: number;
  immutable: boolean;
}

const versionPattern = "v[0-9][0-9A-Za-z._-]{0,63}";
const beeapiAssetPattern = "beeapi_(?:linux|darwin|windows)_(?:amd64|arm64)\\.(?:tar\\.gz|zip)(?:\\.sha256)?";
const cfstAssetPattern = "cfst_(?:linux|darwin|windows)_(?:amd64|arm64)\\.(?:tar\\.gz|zip)";

const beeapiRelease = new RegExp(`^/releases/(latest|${versionPattern})/download/(${beeapiAssetPattern})$`);
const cfstRelease = new RegExp(`^/releases/cfst/(${versionPattern})/(${cfstAssetPattern})$`);

export function resolveReleaseRoute(url: URL): ReleaseRoute | null {
  if (url.search || url.hash) return null;

  if (url.pathname === "/releases/cfst/latest.json") {
    return {
      upstream: "https://api.github.com/repos/XIU2/CloudflareSpeedTest/releases/latest",
      cacheSeconds: 600,
      immutable: false,
    };
  }

  const beeapi = beeapiRelease.exec(url.pathname);
  if (beeapi) {
    const [, version, asset] = beeapi;
    return {
      upstream: `https://github.com/BeeAPI-AI/beeapi/releases/${version === "latest" ? "latest/download" : `download/${version}`}/${asset}`,
      cacheSeconds: version === "latest" ? 300 : 31_536_000,
      immutable: version !== "latest",
    };
  }

  const cfst = cfstRelease.exec(url.pathname);
  if (cfst) {
    const [, version, asset] = cfst;
    return {
      upstream: `https://github.com/XIU2/CloudflareSpeedTest/releases/download/${version}/${asset}`,
      cacheSeconds: 31_536_000,
      immutable: true,
    };
  }

  return null;
}

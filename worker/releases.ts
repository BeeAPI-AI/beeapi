export interface ReleaseRoute {
  upstream: string;
  cacheSeconds: number;
  immutable: boolean;
  fallback?: {
    body: string;
    contentType: string;
  };
}

const versionPattern = "v[0-9][0-9A-Za-z._-]{0,63}";
const beeapiAssetPattern = "beeapi_(?:linux|darwin|windows)_(?:amd64|arm64)\\.(?:tar\\.gz|zip)(?:\\.sha256)?";
const cfstAssetPattern = "cfst_(?:linux|darwin|windows)_(?:amd64|arm64)\\.(?:tar\\.gz|zip)";

const beeapiRelease = new RegExp(`^/releases/(latest|${versionPattern})/download/(${beeapiAssetPattern})$`);
const cfstRelease = new RegExp(`^/releases/cfst/(${versionPattern})/(${cfstAssetPattern})$`);

// GitHub's unauthenticated Releases API is rate-limited per egress IP. Keep a
// verified snapshot so first-time installs can still locate CFST when that API
// returns 403. The normal upstream remains authoritative whenever available.
const cfstLatestFallback = JSON.stringify({
  tag_name: "v2.3.5",
  published_at: "2026-04-29T12:03:33Z",
  assets: [
    {
      name: "cfst_darwin_amd64.zip",
      browser_download_url: "https://github.com/XIU2/CloudflareSpeedTest/releases/download/v2.3.5/cfst_darwin_amd64.zip",
      digest: "sha256:66ce3ae89430e851cab9710d54b6d91324e0aae255f0c92a91072d57724561d5",
      size: 3220040,
    },
    {
      name: "cfst_darwin_arm64.zip",
      browser_download_url: "https://github.com/XIU2/CloudflareSpeedTest/releases/download/v2.3.5/cfst_darwin_arm64.zip",
      digest: "sha256:0623f6d24c939e3d3716f556f4d39c7b8781cf6600ee838a1b64e6b2fe4609dc",
      size: 3010490,
    },
    {
      name: "cfst_linux_amd64.tar.gz",
      browser_download_url: "https://github.com/XIU2/CloudflareSpeedTest/releases/download/v2.3.5/cfst_linux_amd64.tar.gz",
      digest: "sha256:c4c8fc76b4e1bf2bdb5ced8b765956d82dda7bc4eb59df5c04053f0f7db98d90",
      size: 3166688,
    },
    {
      name: "cfst_linux_arm64.tar.gz",
      browser_download_url: "https://github.com/XIU2/CloudflareSpeedTest/releases/download/v2.3.5/cfst_linux_arm64.tar.gz",
      digest: "sha256:0ac992fcf24d4684caed33620deb9b83ce82f32d2418dc1f90be490ce5900300",
      size: 2892627,
    },
    {
      name: "cfst_windows_amd64.zip",
      browser_download_url: "https://github.com/XIU2/CloudflareSpeedTest/releases/download/v2.3.5/cfst_windows_amd64.zip",
      digest: "sha256:67d06a0c68b7fd6998d5e6abea1dbf850cac2c19c5d8c5980aa32fc7aba1ff5f",
      size: 3324747,
    },
    {
      name: "cfst_windows_arm64.zip",
      browser_download_url: "https://github.com/XIU2/CloudflareSpeedTest/releases/download/v2.3.5/cfst_windows_arm64.zip",
      digest: "sha256:84db1fd918fbc4e6fc1fc127681b24810dfdb4b31aa22374e5e129c6ec529573",
      size: 3003997,
    },
  ],
});

export function resolveReleaseRoute(url: URL): ReleaseRoute | null {
  if (url.search || url.hash) return null;

  if (url.pathname === "/releases/latest.json") {
    return {
      upstream: "https://api.github.com/repos/BeeAPI-AI/beeapi/releases/latest",
      cacheSeconds: 300,
      immutable: false,
    };
  }

  if (url.pathname === "/releases/cfst/latest.json") {
    return {
      upstream: "https://api.github.com/repos/XIU2/CloudflareSpeedTest/releases/latest",
      cacheSeconds: 600,
      immutable: false,
      fallback: {
        body: cfstLatestFallback,
        contentType: "application/json; charset=utf-8",
      },
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

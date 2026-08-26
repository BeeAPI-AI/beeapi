#!/bin/sh
set -eu

version="latest"
install_dir="${BEEAPI_INSTALL_DIR:-$HOME/.local/bin}"
run_setup=1

usage() {
  printf '%s\n' "GetBeeAPI installer"
  printf '%s\n' "Usage: install.sh [--version vX.Y.Z] [--install-dir PATH] [--no-setup]"
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version)
      [ "$#" -ge 2 ] || { printf '%s\n' "--version requires a value" >&2; exit 2; }
      version="$2"
      shift 2
      ;;
    --install-dir)
      [ "$#" -ge 2 ] || { printf '%s\n' "--install-dir requires a value" >&2; exit 2; }
      install_dir="$2"
      shift 2
      ;;
    --no-setup)
      run_setup=0
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      printf 'Unknown option: %s\n' "$1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

case "$(uname -s)" in
  Linux) os="linux" ;;
  Darwin) os="darwin" ;;
  *) printf 'Unsupported operating system: %s\n' "$(uname -s)" >&2; exit 1 ;;
esac

case "$(uname -m)" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *) printf 'Unsupported CPU architecture: %s\n' "$(uname -m)" >&2; exit 1 ;;
esac

asset="beeapi_${os}_${arch}.tar.gz"
if [ -n "${BEEAPI_DOWNLOAD_BASE:-}" ]; then
  base="${BEEAPI_DOWNLOAD_BASE%/}"
elif [ "$version" = "latest" ]; then
  base="https://github.com/BeeAPI-AI/beeapi/releases/latest/download"
else
  base="https://github.com/BeeAPI-AI/beeapi/releases/download/$version"
fi

case "$base" in
  https://*) ;;
  *) printf '%s\n' "Download base must use HTTPS" >&2; exit 1 ;;
esac

tmp_dir="$(mktemp -d 2>/dev/null || mktemp -d -t getbeeapi)"
trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM

download() {
  url="$1"
  destination="$2"
  if command -v curl >/dev/null 2>&1; then
    curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --tlsv1.2 "$url" --output "$destination"
  elif command -v wget >/dev/null 2>&1; then
    wget --https-only --quiet "$url" --output-document "$destination"
  else
    printf '%s\n' "curl or wget is required" >&2
    exit 1
  fi
}

printf 'Downloading %s…\n' "$asset"
download "$base/$asset" "$tmp_dir/$asset"
download "$base/$asset.sha256" "$tmp_dir/$asset.sha256"
expected="$(tr -d '[:space:]' < "$tmp_dir/$asset.sha256")"

if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$tmp_dir/$asset" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  actual="$(shasum -a 256 "$tmp_dir/$asset" | awk '{print $1}')"
else
  actual="$(openssl dgst -sha256 "$tmp_dir/$asset" | awk '{print $NF}')"
fi

if [ "$expected" != "$actual" ]; then
  printf '%s\n' "SHA-256 verification failed" >&2
  exit 1
fi

tar -xzf "$tmp_dir/$asset" -C "$tmp_dir" beeapi
mkdir -p "$install_dir"
if command -v install >/dev/null 2>&1; then
  install -m 0755 "$tmp_dir/beeapi" "$install_dir/beeapi"
else
  cp "$tmp_dir/beeapi" "$install_dir/beeapi"
  chmod 0755 "$install_dir/beeapi"
fi

printf '\nInstalled beeapi to %s\n' "$install_dir/beeapi"
case ":$PATH:" in
  *":$install_dir:"*) ;;
  *)
    printf 'Add this directory to PATH:\n  export PATH="%s:$PATH"\n' "$install_dir"
    ;;
esac

if [ "$run_setup" -eq 1 ]; then
  if [ -r /dev/tty ]; then
    exec "$install_dir/beeapi" </dev/tty
  else
    printf '\nRun %s/beeapi to start the setup guide.\n' "$install_dir"
  fi
fi

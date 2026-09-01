#!/bin/sh
set -eu

version="latest"
install_dir="${BEEAPI_INSTALL_DIR:-$HOME/.local/bin}"
run_setup=1

usage() {
  printf '%s\n' "GetBeeAPI installer"
  printf '%s\n' "Usage: install.sh [--version vX.Y.Z] [--install-dir PATH] [--no-setup]"
}

configure_shell_path() {
  case ":$PATH:" in
    *":$install_dir:"*)
      printf '%s\n' "The beeapi command is already available in PATH."
      return 0
      ;;
  esac

  shell_name="${SHELL:-}"
  shell_name="${shell_name##*/}"
  profile=""
  path_line=""
  case "$shell_name" in
    zsh)
      profile="$HOME/.zshrc"
      ;;
    bash)
      if [ "$os" = "darwin" ]; then
        profile="$HOME/.bash_profile"
      else
        profile="$HOME/.bashrc"
      fi
      ;;
    sh|dash|ash|ksh)
      profile="$HOME/.profile"
      ;;
    fish)
      profile="$HOME/.config/fish/conf.d/getbeeapi.fish"
      mkdir -p "$(dirname "$profile")"
      if [ "$install_dir" = "$HOME/.local/bin" ]; then
        path_line='fish_add_path -g $HOME/.local/bin'
      else
        case "$install_dir" in
          *:*)
            printf '%s\n' "Install directory contains ':'; PATH was not edited automatically." >&2
            profile=""
            ;;
          *)
            escaped_dir="$(printf '%s' "$install_dir" | sed "s/'/'\\\\''/g")"
            path_line="fish_add_path -g '$escaped_dir'"
            ;;
        esac
      fi
      ;;
    *)
      profile="$HOME/.profile"
      ;;
  esac

  if [ -n "$profile" ] && [ -z "$path_line" ]; then
    if [ "$install_dir" = "$HOME/.local/bin" ]; then
      path_line='export PATH="$HOME/.local/bin:$PATH"'
    else
      case "$install_dir" in
        *:*)
          printf '%s\n' "Install directory contains ':'; PATH was not edited automatically." >&2
          profile=""
          ;;
        *)
          escaped_dir="$(printf '%s' "$install_dir" | sed "s/'/'\\\\''/g")"
          path_line="export PATH='$escaped_dir':\"\$PATH\""
          ;;
      esac
    fi
  fi

  if [ -n "$profile" ] && [ -n "$path_line" ]; then
    if [ ! -f "$profile" ] || ! grep -Fq '# >>> getbeeapi PATH >>>' "$profile"; then
      {
        printf '\n%s\n' '# >>> getbeeapi PATH >>>'
        printf '%s\n' "$path_line"
        printf '%s\n' '# <<< getbeeapi PATH <<<'
      } >> "$profile"
    fi
    printf 'Added the beeapi command to %s\n' "$profile"
    if [ "$shell_name" = "fish" ]; then
      printf 'Open a new terminal or run:  source %s\n' "$profile"
    else
      printf 'Open a new terminal or run:  . %s\n' "$profile"
    fi
    return 0
  fi

  printf 'Add this directory to PATH for future shells:\n  export PATH="%s:$PATH"\n' "$install_dir"
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
  bases="${BEEAPI_DOWNLOAD_BASE%/}"
elif [ "$version" = "latest" ]; then
  bases="https://getbeeapi.com/releases/latest/download https://github.com/BeeAPI-AI/beeapi/releases/latest/download"
else
  bases="https://getbeeapi.com/releases/$version/download https://github.com/BeeAPI-AI/beeapi/releases/download/$version"
fi

for candidate_base in $bases; do
  case "$candidate_base" in
    https://*) ;;
    *) printf '%s\n' "Download base must use HTTPS" >&2; exit 1 ;;
  esac
done

tmp_dir="$(mktemp -d 2>/dev/null || mktemp -d -t getbeeapi)"
trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM

download() {
  url="$1"
  destination="$2"
  if command -v curl >/dev/null 2>&1; then
    curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --tlsv1.2 "$url" --output "$destination"
  elif command -v wget >/dev/null 2>&1; then
    if wget --help 2>&1 | grep -q 'https-only'; then
      wget --https-only --quiet "$url" --output-document "$destination"
    else
      printf '%s\n' "This wget cannot enforce HTTPS-only redirects; install curl or GNU Wget" >&2
      return 1
    fi
  else
    printf '%s\n' "curl or wget is required" >&2
    return 1
  fi
}

sha256_file() {
  target="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$target" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$target" | awk '{print $1}'
  else
    openssl dgst -sha256 "$target" | awk '{print $NF}'
  fi
}

install_source_for() {
  case "$1" in
    https://getbeeapi.com/*) printf '%s' "getbeeapi" ;;
    https://github.com/*) printf '%s' "github" ;;
    *) printf '%s' "custom" ;;
  esac
}

report_verified_install() {
  if [ "${BEEAPI_DISABLE_INSTALL_STATS:-0}" = "1" ] || \
     [ "${DO_NOT_TRACK:-0}" = "1" ]; then
    return 0
  fi

  stats_url="${BEEAPI_INSTALL_STATS_URL:-https://getbeeapi.com/api/install-events}"
  case "$stats_url" in
    https://*) ;;
    *) return 0 ;;
  esac

  event_id=""
  if command -v od >/dev/null 2>&1 && [ -r /dev/urandom ]; then
    event_id="$(od -An -N16 -tx1 /dev/urandom 2>/dev/null | tr -d ' \n')"
  fi
  case "$event_id" in
    *[!0-9a-f]*|'') return 0 ;;
  esac
  [ "${#event_id}" -eq 32 ] || return 0

  payload="{\"event_id\":\"$event_id\",\"version\":\"$installed_version\",\"os\":\"$os\",\"arch\":\"$arch\",\"source\":\"$install_source\",\"installer\":\"shell\"}"
  if command -v curl >/dev/null 2>&1; then
    curl --fail --silent --location --proto '=https' --proto-redir '=https' --tlsv1.2 \
      --connect-timeout 2 --max-time 3 --request POST \
      --header 'Content-Type: application/json' --data "$payload" "$stats_url" \
      >/dev/null 2>&1 || true
  elif command -v wget >/dev/null 2>&1 && wget --help 2>&1 | grep -q 'https-only'; then
    wget --https-only --quiet --timeout=3 --tries=1 \
      --header='Content-Type: application/json' --post-data="$payload" \
      "$stats_url" --output-document=/dev/null >/dev/null 2>&1 || true
  fi
}

downloaded=0
verified_base=""
for candidate_base in $bases; do
  printf 'Downloading %s from %s…\n' "$asset" "$candidate_base"
  if download "$candidate_base/$asset" "$tmp_dir/$asset" && \
     download "$candidate_base/$asset.sha256" "$tmp_dir/$asset.sha256"; then
    expected="$(tr -d '[:space:]' < "$tmp_dir/$asset.sha256")"
    actual="$(sha256_file "$tmp_dir/$asset")"
    case "$expected" in
      ''|*[!0-9a-fA-F]*) checksum_valid=0 ;;
      *)
        if [ "${#expected}" -eq 64 ] && [ "$expected" = "$actual" ]; then
          checksum_valid=1
        else
          checksum_valid=0
        fi
        ;;
    esac
    if [ "$checksum_valid" -eq 1 ]; then
      downloaded=1
      verified_base="$candidate_base"
      break
    fi
    printf '%s\n' "SHA-256 verification failed for this source; trying the next source." >&2
    continue
  fi
  printf '%s\n' "This source is unavailable; trying the next verified source." >&2
done
if [ "$downloaded" -ne 1 ]; then
  printf '%s\n' "Unable to download the BeeAPI release from any source." >&2
  exit 1
fi

install_source="$(install_source_for "$verified_base")"

tar -xzf "$tmp_dir/$asset" -C "$tmp_dir" beeapi
mkdir -p "$install_dir"
if command -v install >/dev/null 2>&1; then
  install -m 0755 "$tmp_dir/beeapi" "$install_dir/beeapi"
else
  cp "$tmp_dir/beeapi" "$install_dir/beeapi"
  chmod 0755 "$install_dir/beeapi"
fi

if ! "$install_dir/beeapi" --version > "$tmp_dir/installed-version" 2>/dev/null; then
  printf '%s\n' "Installed binary could not complete its version check." >&2
  exit 1
fi
installed_version="$(sed -n '1p' "$tmp_dir/installed-version" | tr -d '\r\n')"
case "$installed_version" in
  ''|*[!0-9A-Za-z._+-]*)
    printf '%s\n' "Installed binary returned an invalid version." >&2
    exit 1
    ;;
esac

printf '\nInstalled beeapi to %s\n' "$install_dir/beeapi"
configure_shell_path
report_verified_install

if [ "$run_setup" -eq 1 ]; then
  if [ -r /dev/tty ] && ( : </dev/tty ) 2>/dev/null; then
    "$install_dir/beeapi" </dev/tty
  else
    printf '\nRun beeapi in a new terminal to start the setup guide.\n'
  fi
fi

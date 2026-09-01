#!/bin/sh
set -eu

repo_dir="$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)"
work_dir="$(mktemp -d "$repo_dir/.tmp-cli-smoke.XXXXXX")"
trap 'rm -rf "$work_dir"' EXIT HUP INT TERM

case "$(uname -m)" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *) printf 'Unsupported test architecture\n' >&2; exit 1 ;;
esac

mkdir -p "$work_dir/assets" "$work_dir/archive" "$work_dir/home" "$work_dir/go-tmp"
env GOCACHE="$work_dir/go-cache" GOTMPDIR="$work_dir/go-tmp" go build -buildvcs=false -o "$work_dir/archive/beeapi" "$repo_dir/cmd/beeapi"
tar -czf "$work_dir/assets/beeapi_linux_${arch}.tar.gz" -C "$work_dir/archive" beeapi
sha256sum "$work_dir/assets/beeapi_linux_${arch}.tar.gz" | awk '{print $1}' > "$work_dir/assets/beeapi_linux_${arch}.tar.gz.sha256"

test_path="$repo_dir/tests/fixtures/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
env \
  HOME="$work_dir/home" \
  SHELL=/bin/sh \
  PATH="$test_path" \
  BEEAPI_TEST_ASSET_DIR="$work_dir/assets" \
  BEEAPI_TEST_EVENT_FILE="$work_dir/install-event.json" \
  BEEAPI_INSTALL_STATS_URL=https://stats.fixture.invalid/api/install-events \
  sh "$repo_dir/public/install.sh" --no-setup

test -x "$work_dir/home/.local/bin/beeapi"
grep -Fq '"version":"dev"' "$work_dir/install-event.json"
grep -Fq '"source":"getbeeapi"' "$work_dir/install-event.json"
grep -Fq '"installer":"shell"' "$work_dir/install-event.json"
grep -Fq '# >>> getbeeapi PATH >>>' "$work_dir/home/.profile"
env HOME="$work_dir/home" PATH="$test_path" sh -c '. "$HOME/.profile"; beeapi version' | grep -Fq 'dev'

# A repeated installation must not append duplicate PATH blocks.
env \
  HOME="$work_dir/home" \
  SHELL=/bin/sh \
  PATH="$test_path" \
  BEEAPI_TEST_ASSET_DIR="$work_dir/assets" \
  BEEAPI_DISABLE_INSTALL_STATS=1 \
  sh "$repo_dir/public/install.sh" --no-setup >/dev/null
test "$(grep -Fc '# >>> getbeeapi PATH >>>' "$work_dir/home/.profile")" -eq 1

# The most common interactive shells receive their own startup-file syntax.
mkdir -p "$work_dir/zsh-home" "$work_dir/fish-home"
env \
  HOME="$work_dir/zsh-home" \
  SHELL=/bin/zsh \
  PATH="$test_path" \
  BEEAPI_TEST_ASSET_DIR="$work_dir/assets" \
  BEEAPI_DOWNLOAD_BASE=https://fixture.invalid \
  BEEAPI_DISABLE_INSTALL_STATS=1 \
  sh "$repo_dir/public/install.sh" --no-setup >/dev/null
grep -Fq 'export PATH="$HOME/.local/bin:$PATH"' "$work_dir/zsh-home/.zshrc"
env HOME="$work_dir/zsh-home" PATH="$test_path" sh -c '. "$HOME/.zshrc"; beeapi version' | grep -Fq 'dev'

env \
  HOME="$work_dir/fish-home" \
  SHELL=/usr/bin/fish \
  PATH="$test_path" \
  BEEAPI_TEST_ASSET_DIR="$work_dir/assets" \
  BEEAPI_DOWNLOAD_BASE=https://fixture.invalid \
  BEEAPI_DISABLE_INSTALL_STATS=1 \
  sh "$repo_dir/public/install.sh" --no-setup >/dev/null
grep -Fq 'fish_add_path -g $HOME/.local/bin' "$work_dir/fish-home/.config/fish/conf.d/getbeeapi.fish"

# If the edge cache is temporarily unavailable, the installer falls back to
# GitHub and still verifies the exact same checksum.
mkdir -p "$work_dir/fallback-home"
env \
  HOME="$work_dir/fallback-home" \
  SHELL=/bin/sh \
  PATH="$test_path" \
  BEEAPI_TEST_ASSET_DIR="$work_dir/assets" \
  BEEAPI_TEST_FAIL_MIRROR=1 \
  BEEAPI_DISABLE_INSTALL_STATS=1 \
  sh "$repo_dir/public/install.sh" --no-setup >/dev/null
test -x "$work_dir/fallback-home/.local/bin/beeapi"

# A cache or catch-all page returning HTTP 200 is not trusted unless the
# checksum matches; the installer must continue to the GitHub source.
mkdir -p "$work_dir/checksum-fallback-home"
env \
  HOME="$work_dir/checksum-fallback-home" \
  SHELL=/bin/sh \
  PATH="$test_path" \
  BEEAPI_TEST_ASSET_DIR="$work_dir/assets" \
  BEEAPI_TEST_CORRUPT_MIRROR=1 \
  BEEAPI_DISABLE_INSTALL_STATS=1 \
  sh "$repo_dir/public/install.sh" --no-setup >/dev/null
test -x "$work_dir/checksum-fallback-home/.local/bin/beeapi"

config_home="$work_dir/config"
mkdir -p "$config_home"
printf '%s\n' '{"schema_version":1,"endpoint":"https://beeapi.dev","key_name":"smoke","agents":["codex"],"credential_backend":"protected-file"}' > "$config_home/config.json"
printf '0\n' | env BEEAPI_LANG=zh-CN GETBEE_HOME="$config_home" "$work_dir/home/.local/bin/beeapi" > "$work_dir/home-output.txt"
grep -Fq 'BeeAPI CLI' "$work_dir/home-output.txt"
grep -Fq '当前方案  默认配置' "$work_dir/home-output.txt"
grep -Fq '快速切换配置方案' "$work_dir/home-output.txt"
if grep -Fq 'AI 工具配置中心' "$work_dir/home-output.txt"; then
  printf 'Legacy shell title unexpectedly remains\n' >&2
  exit 1
fi
if grep -Fq '[1/3] 检测 BeeAPI' "$work_dir/home-output.txt"; then
  printf 'Returning user unexpectedly entered first setup\n' >&2
  exit 1
fi

english_config_home="$work_dir/config-en"
mkdir -p "$english_config_home"
printf '%s\n' '{"schema_version":4,"language":"en","endpoint":"https://beeapi.dev","key_name":"smoke","agents":["codex"],"credential_backend":"protected-file"}' > "$english_config_home/config.json"
printf '0\n' | env GETBEE_HOME="$english_config_home" "$work_dir/home/.local/bin/beeapi" > "$work_dir/home-output-en.txt"
grep -Fq 'Quick-switch profile' "$work_dir/home-output-en.txt"
grep -Fq 'Exited BeeAPI CLI' "$work_dir/home-output-en.txt"
if grep -Fq 'Choose your language / 选择语言' "$work_dir/home-output-en.txt"; then
  printf 'Returning English user unexpectedly entered language selection\n' >&2
  exit 1
fi

printf 'Linux CLI and installer smoke test passed.\n'

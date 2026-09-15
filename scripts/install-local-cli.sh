#!/usr/bin/env bash
set -euo pipefail

# install-local-cli.sh — one command from a fresh clone to a working
# `caveman claude`:
#   1. builds the CLI if dist/ is missing (pnpm)
#   2. writes the bin/cave shim and links it as BOTH `cave` and `caveman`
#   3. builds all six Go binaries into ~/.caveman/bin when a Go
#      toolchain is present — the CLI resolves that directory automatically, so
#      no PATH edit is needed for compression/metering to work
# Steps degrade honestly: no Go toolchain → the CLI still installs and wrap
# says exactly what is missing.

# Package layout: source monorepo uses public/cli; public repo uses
# packages/cli. Keep legacy cli/ fallback for older clones.
if [[ -d public/cli ]]; then
  cli_dir=public/cli
  stack_root=public
elif [[ -d packages/cli ]]; then
  cli_dir=packages/cli
  stack_root=.
elif [[ -d cli ]]; then
  cli_dir=cli
  stack_root=.
else
  echo "✗ Caveman CLI source not found" >&2
  exit 1
fi

if [[ ! -f "$cli_dir/dist/index.js" ]]; then
  if command -v pnpm >/dev/null 2>&1; then
    echo "→ building the CLI ($cli_dir)"
    (cd "$cli_dir" && pnpm install --silent && pnpm build)
  else
    echo "✗ $cli_dir/dist/index.js missing and pnpm not found — install pnpm and re-run" >&2
    exit 1
  fi
fi

mkdir -p bin
cat > bin/cave <<'SH'
#!/usr/bin/env bash
set -euo pipefail
source_path="${BASH_SOURCE[0]}"
while [[ -L "$source_path" ]]; do
  dir="$(cd "$(dirname "$source_path")" && pwd)"
  source_path="$(readlink "$source_path")"
  [[ "$source_path" != /* ]] && source_path="$dir/$source_path"
done
repo="$(cd "$(dirname "$source_path")/.." && pwd)"
if [[ -f "$repo/public/cli/dist/index.js" ]]; then
  exec node "$repo/public/cli/dist/index.js" "$@"
fi
if [[ -f "$repo/packages/cli/dist/index.js" ]]; then
  exec node "$repo/packages/cli/dist/index.js" "$@"
fi
exec node "$repo/cli/dist/index.js" "$@"
SH
chmod +x bin/cave

link_shim() {
  local dir="$1"
  ln -sf "$PWD/bin/cave" "$dir/cave"
  ln -sf "$PWD/bin/cave" "$dir/caveman"
  echo "→ linked cave + caveman into $dir"
}
if [[ -d /usr/local/bin && -w /usr/local/bin ]]; then
  link_shim /usr/local/bin
elif [[ -d "$HOME/.local/bin" && -w "$HOME/.local/bin" ]]; then
  link_shim "$HOME/.local/bin"
else
  echo "⚠ no writable /usr/local/bin or ~/.local/bin — add $PWD/bin to PATH yourself" >&2
fi

if command -v go >/dev/null 2>&1; then
  cave_bin="${CAVEMAN_HOME:-$HOME/.caveman}/bin"
  mkdir -p "$cave_bin"
  echo "→ building Go binaries into $cave_bin"
  go build -o "$cave_bin/caveman-proxy" "$stack_root/proxy/cmd/caveman-proxy"
  go build -o "$cave_bin/caveman-engine" "$stack_root/engine/cmd/caveman-engine"
  go build -o "$cave_bin/caveman-mcp" "$stack_root/mcp/cmd/caveman-mcp"
  go build -o "$cave_bin/cavemem" "$stack_root/mem/cmd/cavemem"
  go build -o "$cave_bin/caveman-browse" "$stack_root/browse/cmd/caveman-browse"
  go build -o "$cave_bin/caveman-shrink" "$stack_root/shrink/cmd/caveman-shrink"
  echo "✓ done — try: caveman claude   (wraps Claude Code with local compression + metering)"
else
  echo "⚠ Go toolchain not found — skipped six runtime companions." >&2
  echo "  The CLI works, but wrap runs without local compression/metering until you build them:" >&2
  echo "    go build -o ~/.caveman/bin/caveman-proxy $stack_root/proxy/cmd/caveman-proxy" >&2
fi

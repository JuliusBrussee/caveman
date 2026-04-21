#!/usr/bin/env bash
set -euo pipefail

SKILLS=(caveman caveman-commit caveman-review compress)
PLUGIN_NAME="caveman.js"

has_skill() {
  [ -d "$HOME/.agents/skills/$1" ] && return 0
  [ -d "$HOME/.config/opencode/skills/$1" ] && return 0
  [ -d ".opencode/skills/$1" ] && return 0
  return 1
}

usage() {
  echo "Usage: $0 {install|uninstall} [--global|--local|-y|--skills]"
  echo ""
  echo "install    Install skills (via npx skills add if missing) + plugin"
  echo "uninstall  Remove plugin (default); add --skills to also remove scoped skills"
  echo ""
  echo "--global   Install to ~/.config/opencode/ (default)"
  echo "--local    Install to .opencode/ (current dir)"
  echo "-y         Skip prompts"
  echo "--skills   On uninstall, also remove skills from the active scope"
  exit 0
}

ACTION="${1:-install}"
shift || true

SCOPE=""
CONFIRM=false
REMOVE_SKILLS=false
while [ $# -gt 0 ]; do
  case "$1" in
    --global) SCOPE="global" ;;
    --local)  SCOPE="local" ;;
    -y)       CONFIRM=true ;;
    --skills) REMOVE_SKILLS=true ;;
    -h|--help) usage ;;
  esac
  shift
done

if [ -z "$SCOPE" ]; then
  if [ -f "$HOME/.config/opencode/plugins/$PLUGIN_NAME" ]; then
    SCOPE="global"
  elif [ -f ".opencode/plugins/$PLUGIN_NAME" ]; then
    SCOPE="local"
  else
    SCOPE="global"
  fi
fi

if [ "$SCOPE" = "global" ]; then
  PLUGINS_TARGET="$HOME/.config/opencode/plugins"
else
  PLUGINS_TARGET="$(pwd)/.opencode/plugins"
fi

if [ "$ACTION" = "uninstall" ]; then
  PLUGIN_PATH="$PLUGINS_TARGET/$PLUGIN_NAME"

  if [ ! -f "$PLUGIN_PATH" ]; then
    echo "Plugin not found at $PLUGIN_PATH"
  else
    rm -f "$PLUGIN_PATH"
    echo "Removed: $PLUGIN_PATH"
  fi

  if [ "$REMOVE_SKILLS" = true ]; then
    # Only delete from the active scope; never touch $HOME/.agents/skills
    # ($HOME/.agents/skills is managed by npx skills add, shared across projects)
    if [ "$SCOPE" = "global" ]; then
      SKILL_DIR="$HOME/.config/opencode/skills"
    else
      SKILL_DIR="$(pwd)/.opencode/skills"
    fi
    for skill in "${SKILLS[@]}"; do
      if [ -d "$SKILL_DIR/$skill" ]; then
        rm -rf "$SKILL_DIR/$skill"
        echo "Removed: $SKILL_DIR/$skill"
      fi
    done
    rm -f "$SKILL_DIR/.caveman-manifest"
  fi

  rm -f "$HOME/.config/opencode/.caveman-active"

  echo ""
  echo "Done."
  exit 0
fi

echo "Installing caveman for OpenCode"
echo ""

SKILLS_MISSING=false
for skill in "${SKILLS[@]}"; do
  if ! has_skill "$skill"; then
    SKILLS_MISSING=true
    break
  fi
done

if [ "$SKILLS_MISSING" = true ]; then
  if command -v npx >/dev/null 2>&1; then
    echo "  Installing skills via npx..."
    npx skills add JuliusBrussee/caveman -a opencode
    echo "  Done."
  else
    echo "  npx not found. Skipping skills — install manually with:"
    echo "    npx skills add JuliusBrussee/caveman -a opencode"
  fi
else
  echo "  Skills: already present"
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

mkdir -p "$PLUGINS_TARGET"
cp "$REPO_ROOT/plugins/opencode/caveman/plugin.js" "$PLUGINS_TARGET/$PLUGIN_NAME"
echo "  Plugin: $PLUGINS_TARGET/$PLUGIN_NAME"

if [ "$SCOPE" = "global" ]; then
  MANIFEST_DIR="$HOME/.config/opencode/skills"
else
  MANIFEST_DIR="$(pwd)/.opencode/skills"
fi
mkdir -p "$MANIFEST_DIR"
printf '%s\n' "${SKILLS[@]}" > "$MANIFEST_DIR/.caveman-manifest"

echo ""
echo "Done. /caveman [lite|full|ultra|wenyan|wenyan-lite|wenyan-ultra]"
echo "Stop: 'stop caveman' or 'normal mode'"

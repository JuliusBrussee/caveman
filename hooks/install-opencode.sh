#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

DEST_DIR="${1:-$(pwd)}"
GLOBAL_SKILLS="$HOME/.config/opencode/skills"
GLOBAL_PLUGINS="$HOME/.config/opencode/plugins"
SKILLS_DEST="$DEST_DIR/.opencode/skills"
HOME_SKILLS="$HOME/.agents/skills/caveman"
SKILLS=(caveman caveman-commit caveman-review compress)

echo "Installing caveman for OpenCode"
echo ""

echo " Install scope:"
echo "  g) Global (all projects, recommended — matches npx skills add)"
echo "  l) Local (current project/directory only)"
echo ""
read -p " Choose [g/l]: " scope
scope="${scope:-g}"

if [ "$scope" = "g" ]; then
  SKILLS_TARGET="$GLOBAL_SKILLS"
  PLUGINS_TARGET="$GLOBAL_PLUGINS"
else
  SKILLS_TARGET="$SKILLS_DEST"
  PLUGINS_TARGET="$DEST_DIR/.opencode/plugins"
fi

mkdir -p "$SKILLS_TARGET"
mkdir -p "$PLUGINS_TARGET"

if [ -d "$HOME_SKILLS" ]; then
  echo "Skills: already in ~/.agents/skills, skipping"
elif [ -d "$SKILLS_DEST/caveman" ] && [ "$scope" = "l" ]; then
  echo "Skills: already in .opencode/skills, skipping"
elif [ -d "$GLOBAL_SKILLS/caveman" ] && [ "$scope" = "g" ]; then
  echo "Skills: already in ~/.config/opencode/skills, skipping"
else
  for skill in "${SKILLS[@]}"; do
    src="$REPO_ROOT/skills/$skill"
    if [ -d "$src" ]; then
      cp -r "$src" "$SKILLS_TARGET/$skill"
      echo "  skill: $skill"
    fi
  done
fi

cp "$REPO_ROOT/plugins/opencode/caveman/plugin.js" "$PLUGINS_TARGET/caveman.js"
echo " Plugin installed at: $PLUGINS_TARGET/caveman.js"
echo " Done. /caveman [lite|full|ultra|wenyan|wenyan-lite|wenyan-ultra]"
echo " Stop: 'stop caveman' or 'normal mode'"

#!/usr/bin/env bash

set -euo pipefail

REPO_URL="https://github.com/JuliusBrussee/caveman"
TARGET_SKILLS_DIR="${HOME}/.codex/skills"
SKILLS=("caveman" "compress" "caveman-commit" "caveman-help" "caveman-review")
TMP_DIR=""
INSTALL_COMPLETE=0
INSTALLED_SKILLS=()

cleanup() {
  if [ "${INSTALL_COMPLETE}" -eq 0 ]; then
    for skill in "${INSTALLED_SKILLS[@]}"; do
      rm -rf "${TARGET_SKILLS_DIR}/${skill}"
    done
  fi

  if [ -n "${TMP_DIR}" ]; then
    rm -rf "${TMP_DIR}"
  fi
}

trap cleanup EXIT

fail() {
  echo "Error: $*" >&2
  exit 1
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

need_cmd git
need_cmd mv
need_cmd mkdir
need_cmd mktemp
need_cmd rm

for skill in "${SKILLS[@]}"; do
  if [ -e "${TARGET_SKILLS_DIR}/${skill}" ]; then
    fail "${TARGET_SKILLS_DIR}/${skill} already exists. Stop before overwrite."
  fi
done

echo "Clone caveman skill subtrees..."
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/caveman-skill-only.XXXXXX")"
git clone --depth 1 --filter=blob:none --sparse "${REPO_URL}" "${TMP_DIR}/repo"
git -C "${TMP_DIR}/repo" sparse-checkout set \
  "skills/caveman" \
  "skills/compress" \
  "skills/caveman-commit" \
  "skills/caveman-help" \
  "skills/caveman-review"

for skill in "${SKILLS[@]}"; do
  [ -f "${TMP_DIR}/repo/skills/${skill}/SKILL.md" ] || fail "missing skill: ${skill}"
done

echo "Ensure Codex skills directory exists..."
mkdir -p "${TARGET_SKILLS_DIR}"

echo "Move skills into Codex skills directory..."
for skill in "${SKILLS[@]}"; do
  mv "${TMP_DIR}/repo/skills/${skill}" "${TARGET_SKILLS_DIR}/${skill}"
  INSTALLED_SKILLS+=("${skill}")
done

for skill in "${SKILLS[@]}"; do
  [ -f "${TARGET_SKILLS_DIR}/${skill}/SKILL.md" ] || fail "installed skill missing: ${skill}"
done

INSTALL_COMPLETE=1

echo
echo "Install complete."
echo "Next:"
echo "1. Restart Codex."

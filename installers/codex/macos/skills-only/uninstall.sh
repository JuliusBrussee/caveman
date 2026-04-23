#!/usr/bin/env bash

set -euo pipefail

TARGET_SKILLS_DIR="${HOME}/.codex/skills"
SKILLS=("caveman" "compress" "caveman-commit" "caveman-help" "caveman-review")
ASSUME_YES=0

fail() {
  echo "Error: $*" >&2
  exit 1
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

confirm() {
  if [ "${ASSUME_YES}" -eq 1 ]; then
    return 0
  fi

  printf "Remove Caveman skill-only install from Codex? [y/N] "
  read -r reply || reply=""
  case "${reply}" in
    y|Y|yes|YES)
      return 0
      ;;
    *)
      echo "Cancelled."
      exit 0
      ;;
  esac
}

for arg in "$@"; do
  case "${arg}" in
    --yes)
      ASSUME_YES=1
      ;;
    *)
      fail "unknown argument: ${arg}"
      ;;
  esac
done

need_cmd rm

FOUND=0
for skill in "${SKILLS[@]}"; do
  if [ -e "${TARGET_SKILLS_DIR}/${skill}" ]; then
    FOUND=1
  fi
done

if [ "${FOUND}" -eq 0 ]; then
  echo "Caveman skill-only install not found. Nothing to do."
  exit 0
fi

confirm

for skill in "${SKILLS[@]}"; do
  if [ -e "${TARGET_SKILLS_DIR}/${skill}" ]; then
    echo "Remove skill ${skill}..."
    rm -rf "${TARGET_SKILLS_DIR}/${skill}"
  fi
done

for skill in "${SKILLS[@]}"; do
  if [ -e "${TARGET_SKILLS_DIR}/${skill}" ]; then
    fail "skill still exists: ${TARGET_SKILLS_DIR}/${skill}"
  fi
done

echo
echo "Uninstall complete."
echo "Restart Codex if skills still appear in current session."

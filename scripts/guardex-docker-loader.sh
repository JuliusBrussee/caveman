#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
compose_file="${GUARDEX_DOCKER_COMPOSE_FILE:-}"
service="${GUARDEX_DOCKER_SERVICE:-}"
mode="${GUARDEX_DOCKER_MODE:-auto}"
workdir_override="${GUARDEX_DOCKER_WORKDIR:-}"

usage() {
  cat >&2 <<'EOF'
Usage: bash scripts/guardex-docker-loader.sh [--] <command...>

Environment:
  GUARDEX_DOCKER_SERVICE=<compose-service>   required unless compose defines exactly one service
  GUARDEX_DOCKER_COMPOSE_FILE=<path>         optional docker compose file override
  GUARDEX_DOCKER_MODE=auto|exec|run          default: auto
  GUARDEX_DOCKER_WORKDIR=<path>              optional working directory override inside the container
EOF
}

choose_compose_cmd() {
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    printf 'docker compose'
    return 0
  fi
  if command -v docker-compose >/dev/null 2>&1; then
    printf 'docker-compose'
    return 0
  fi
  return 1
}

mapfile_from_lines() {
  local raw="$1"
  local -n out_ref="$2"
  out_ref=()
  while IFS= read -r line; do
    [[ -n "$line" ]] || continue
    out_ref+=("$line")
  done <<<"$raw"
}

if [[ "${1:-}" == "--" ]]; then
  shift
fi

if [[ $# -eq 0 ]]; then
  usage
  exit 1
fi

if [[ "$mode" != "auto" && "$mode" != "exec" && "$mode" != "run" ]]; then
  echo "[guardex-docker-loader] Invalid GUARDEX_DOCKER_MODE: $mode" >&2
  usage
  exit 1
fi

compose_cmd_raw="$(choose_compose_cmd)" || {
  echo "[guardex-docker-loader] Docker Compose is not available. Install docker compose or docker-compose first." >&2
  exit 1
}
IFS=' ' read -r -a compose_cmd <<<"$compose_cmd_raw"
compose_args=()
if [[ -n "$compose_file" ]]; then
  compose_args=(-f "$compose_file")
fi

cd "$repo_root"

services_raw="$("${compose_cmd[@]}" "${compose_args[@]}" config --services 2>/dev/null || true)"
declare -a services
mapfile_from_lines "$services_raw" services
if [[ ${#services[@]} -eq 0 ]]; then
  echo "[guardex-docker-loader] No Docker Compose services found. Add a compose file or set GUARDEX_DOCKER_COMPOSE_FILE." >&2
  exit 1
fi

if [[ -z "$service" ]]; then
  if [[ ${#services[@]} -eq 1 ]]; then
    service="${services[0]}"
  else
    echo "[guardex-docker-loader] Multiple services found (${services[*]}). Set GUARDEX_DOCKER_SERVICE." >&2
    exit 1
  fi
fi

service_known=0
for candidate in "${services[@]}"; do
  if [[ "$candidate" == "$service" ]]; then
    service_known=1
    break
  fi
done
if [[ $service_known -ne 1 ]]; then
  echo "[guardex-docker-loader] Compose service not found: $service" >&2
  exit 1
fi

run_mode="$mode"
if [[ "$run_mode" == "auto" ]]; then
  run_mode="run"
  running_raw="$("${compose_cmd[@]}" "${compose_args[@]}" ps --status running --services 2>/dev/null || true)"
  declare -a running_services
  mapfile_from_lines "$running_raw" running_services
  for candidate in "${running_services[@]}"; do
    if [[ "$candidate" == "$service" ]]; then
      run_mode="exec"
      break
    fi
  done
fi

workdir_args=()
if [[ -n "$workdir_override" ]]; then
  workdir_args=(-w "$workdir_override")
fi

if [[ "$run_mode" == "exec" ]]; then
  exec "${compose_cmd[@]}" "${compose_args[@]}" exec -T "${workdir_args[@]}" "$service" "$@"
fi

exec "${compose_cmd[@]}" "${compose_args[@]}" run --rm -T "${workdir_args[@]}" "$service" "$@"

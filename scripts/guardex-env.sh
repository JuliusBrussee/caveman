#!/usr/bin/env bash

guardex_normalize_bool() {
  local raw="${1:-}"
  local fallback="${2:-}"
  local lowered
  lowered="$(printf '%s' "$raw" | tr '[:upper:]' '[:lower:]')"
  case "$lowered" in
    1|true|yes|on) printf '1' ;;
    0|false|no|off) printf '0' ;;
    '') printf '%s' "$fallback" ;;
    *) printf '%s' "$fallback" ;;
  esac
}

guardex_read_repo_dotenv_var() {
  local repo_root="$1"
  local key="${2:-GUARDEX_ON}"
  local env_file="${repo_root}/.env"
  local line value

  [[ -f "$env_file" ]] || return 1

  while IFS= read -r line || [[ -n "$line" ]]; do
    [[ "$line" =~ ^[[:space:]]*# ]] && continue
    if [[ "$line" =~ ^[[:space:]]*(export[[:space:]]+)?${key}[[:space:]]*=(.*)$ ]]; then
      value="${BASH_REMATCH[2]}"
      value="$(printf '%s' "$value" | sed -E 's/[[:space:]]+#.*$//; s/^[[:space:]]+//; s/[[:space:]]+$//')"
      if [[ "$value" == \"*\" && "$value" == *\" ]]; then
        value="${value:1:${#value}-2}"
      elif [[ "$value" == \'*\' && "$value" == *\' ]]; then
        value="${value:1:${#value}-2}"
      fi
      printf '%s' "$value"
      return 0
    fi
  done < "$env_file"

  return 1
}

guardex_repo_toggle_raw() {
  local repo_root="$1"
  if [[ -n "${GUARDEX_ON:-}" ]]; then
    printf '%s' "$GUARDEX_ON"
    return 0
  fi
  guardex_read_repo_dotenv_var "$repo_root" "GUARDEX_ON"
}

guardex_repo_toggle_source() {
  local repo_root="$1"
  if [[ -n "${GUARDEX_ON:-}" ]]; then
    printf 'process environment'
    return 0
  fi
  if guardex_read_repo_dotenv_var "$repo_root" "GUARDEX_ON" >/dev/null; then
    printf 'repo .env'
    return 0
  fi
  return 1
}

guardex_repo_is_enabled() {
  local repo_root="$1"
  local raw normalized
  if raw="$(guardex_repo_toggle_raw "$repo_root")"; then
    normalized="$(guardex_normalize_bool "$raw" "")"
    if [[ "$normalized" == "0" ]]; then
      return 1
    fi
  fi
  return 0
}

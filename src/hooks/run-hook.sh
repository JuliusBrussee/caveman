#!/usr/bin/env sh

command -v node >/dev/null 2>&1 || exit 0
exec node "$@"

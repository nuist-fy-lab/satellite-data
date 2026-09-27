#!/usr/bin/env bash
set +x
set -euo pipefail
umask 077
target="${1:-${XDG_CONFIG_HOME:-$HOME/.config}/satellite-data/cds-token}"
if [[ -e "$target" ]]; then
  printf 'Token file already exists; refusing to overwrite it.\n' >&2
  exit 1
fi
mkdir -p -- "$(dirname -- "$target")"
temporary="$(mktemp "$(dirname -- "$target")/.cds-token.XXXXXX")"
trap 'unset token; rm -f -- "$temporary"' EXIT
IFS= read -r -s -p 'CDS Personal Access Token: ' token
printf '\n' >&2
if [[ -z "$token" || "$token" == *[[:space:]]* ]]; then
  printf 'Invalid token input.\n' >&2
  exit 1
fi
printf '%s\n' "$token" > "$temporary"
chmod 0600 "$temporary"
ln -- "$temporary" "$target"
unset token
printf 'Private token file created: %s\n' "$target"

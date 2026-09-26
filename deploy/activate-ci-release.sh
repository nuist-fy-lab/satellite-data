#!/usr/bin/env bash
set -euo pipefail

[[ $# == 3 && "$1" =~ ^[0-9a-f]{40}$ && "$2" =~ ^[0-9a-f]{64}$ && "$3" =~ ^[0-9a-f]{64}$ ]] || {
  echo 'Invalid deployment provenance' >&2
  exit 1
}
sha="$1"
satellite_hash="$2"
satellited_hash="$3"
base="$HOME/.local/opt/satellite-data"
release="$base/releases/ci-${sha:0:12}"
previous="$(readlink -f "$base/current")"
[[ "$previous" == "$base/releases/"* && -x "$previous/satellited" ]] || exit 1
[[ -x "$release/satellite" && -x "$release/satellited" ]] || exit 1
[[ "$(sha256sum "$release/satellite" | cut -d' ' -f1)" == "$satellite_hash" ]] || exit 1
[[ "$(sha256sum "$release/satellited" | cut -d' ' -f1)" == "$satellited_hash" ]] || exit 1

export XDG_RUNTIME_DIR="/run/user/$(id -u)"
export DBUS_SESSION_BUS_ADDRESS="unix:path=$XDG_RUNTIME_DIR/bus"
link="$base/.current-deploy.$BASHPID"
switched=0
rollback() {
  code=$?
  trap - ERR
  if (( switched )); then
    ln -s "$previous" "$link.rollback"
    mv -Tf "$link.rollback" "$base/current"
    systemctl --user restart satellited.service || true
    echo "Deployment failed; restored $previous" >&2
  fi
  exit "$code"
}
trap rollback ERR
trap 'rm -f "$link" "$link.rollback"' EXIT

if [[ "$release" != "$previous" ]]; then
  ln -s "$release" "$link"
  mv -Tf "$link" "$base/current"
  switched=1
fi
systemctl --user restart satellited.service
for attempt in {1..12}; do
  if systemctl --user is-active --quiet satellited.service &&
    curl -fsS --max-time 4 http://127.0.0.1:8787/healthz >/dev/null 2>&1 &&
    curl -fsS --max-time 6 https://satellite.rs.fyzure.fyi/healthz >/dev/null 2>&1; then
    break
  fi
  sleep 2
done
systemctl --user is-active --quiet satellited.service
curl -fsS --max-time 4 http://127.0.0.1:8787/healthz >/dev/null
curl -fsS --max-time 6 https://satellite.rs.fyzure.fyi/healthz >/dev/null
pid="$(systemctl --user show satellited.service -p MainPID --value)"
[[ "$pid" =~ ^[1-9][0-9]*$ ]]
kill -0 "$pid"
[[ "$(sha256sum "/proc/$pid/exe" | cut -d' ' -f1)" == "$satellited_hash" ]]
netstat -lntp 2>/dev/null | awk -v pid="$pid" '$4=="127.0.0.1:8787" && $6=="LISTEN" && index($7,pid"/")==1 {found=1} END {exit !found}'
[[ "$(readlink -f "$base/current")" == "$release" ]]
trap - ERR
echo "Activated $sha; rollback release: $previous"

#!/usr/bin/env bash
set -euo pipefail
umask 077

repo=nuist-fy-lab/satellite-data
host=fengziru2025@100.97.137.1
ssh_opts=(-o BatchMode=yes -o ConnectTimeout=10 -o ConnectionAttempts=1 -o StrictHostKeyChecking=yes)
exec 9>/run/lock/satellite-data-auto-deploy.lock
flock -n 9 || exit 0

sha="$(gh api "repos/$repo/branches/main" --jq .commit.sha)"
[[ "$sha" =~ ^[0-9a-f]{40}$ ]] || exit 1
release="$(gh api "repos/$repo/releases/tags/ci-latest")"
target="$(jq -r .target_commitish <<<"$release")"
if [[ "$target" != "$sha" ]]; then
  echo "Waiting for ci-latest to match main ($sha)"
  exit 0
fi

runs="$(gh api "repos/$repo/actions/runs?branch=main&head_sha=$sha&per_page=20")"
run_id="$(jq -r --arg sha "$sha" '[.workflow_runs[] | select(.head_sha == $sha and .name == "CI" and .conclusion == "success" and .event == "push") | .id][0] // empty' <<<"$runs")"
if [[ ! "$run_id" =~ ^[1-9][0-9]*$ ]]; then
  echo "Waiting for a successful main CI run for $sha"
  exit 0
fi

satellite_hash="$(jq -r '.assets[] | select(.name == "satellite") | .digest' <<<"$release")"
satellited_hash="$(jq -r '.assets[] | select(.name == "satellited") | .digest' <<<"$release")"
[[ "$satellite_hash" =~ ^sha256:[0-9a-f]{64}$ && "$satellited_hash" =~ ^sha256:[0-9a-f]{64}$ ]] || exit 1
short="${sha:0:12}"
remote_dir=".local/opt/satellite-data/releases/ci-$short"
if ssh -n "${ssh_opts[@]}" "$host" "export XDG_RUNTIME_DIR=/run/user/3012 DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/3012/bus; test \"\$(basename \"\$(readlink -f ~/.local/opt/satellite-data/current)\")\" = ci-$short && test \"\$(sha256sum /proc/\$(systemctl --user show satellited.service -p MainPID --value)/exe 2>/dev/null | cut -d' ' -f1)\" = ${satellited_hash#sha256:} && curl -fsS --max-time 4 http://127.0.0.1:8787/healthz >/dev/null" 2>/dev/null; then
  echo "Already running $sha"
  exit 0
fi

tmp="$(mktemp -d /tmp/satellite-auto-deploy.XXXXXX)"
trap 'rm -rf "$tmp"' EXIT
gh release download ci-latest --repo "$repo" --dir "$tmp"
[[ "sha256:$(sha256sum "$tmp/satellite" | cut -d' ' -f1)" == "$satellite_hash" ]]
[[ "sha256:$(sha256sum "$tmp/satellited" | cut -d' ' -f1)" == "$satellited_hash" ]]
[[ "$(gh api "repos/$repo/branches/main" --jq .commit.sha)" == "$sha" ]]
[[ "$(gh api "repos/$repo/releases/tags/ci-latest" --jq .target_commitish)" == "$sha" ]]

if ! ssh -n "${ssh_opts[@]}" "$host" "test \"\$(sha256sum $remote_dir/satellite 2>/dev/null | cut -d' ' -f1)\" = ${satellite_hash#sha256:} && test \"\$(sha256sum $remote_dir/satellited 2>/dev/null | cut -d' ' -f1)\" = ${satellited_hash#sha256:}" 2>/dev/null; then
  ssh -n "${ssh_opts[@]}" "$host" "test \"\$(readlink -f ~/.local/opt/satellite-data/current)\" != \"\$HOME/$remote_dir\" && install -d -m 700 '$remote_dir'"
  scp -q "${ssh_opts[@]}" "$tmp/satellite" "$tmp/satellited" "$host:$remote_dir/"
fi
ssh -n "${ssh_opts[@]}" "$host" "chmod 700 '$remote_dir/satellite' '$remote_dir/satellited' && ~/.local/libexec/satellite-data/activate-ci-release.sh '$sha' '${satellite_hash#sha256:}' '${satellited_hash#sha256:}'"
echo "Deployed $sha from successful CI run $run_id"

#!/usr/bin/env bash
set -euo pipefail

repo="${REPO:-${GITHUB_REPOSITORY:-}}"
target="${TARGET_SHA:-${GITHUB_SHA:-}}"
dist="${DIST_DIR:-dist}"
[[ "$repo" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || { echo "Missing or invalid repository" >&2; exit 1; }
[[ "$target" =~ ^[0-9a-f]{40}$ ]] || { echo "Missing or invalid target SHA" >&2; exit 1; }
all_binaries=(satellite satellited satellite-data-downloader satellite-checksum-sidecar)
for name in "${all_binaries[@]}"; do
  [[ -f "$dist/$name" ]] || { echo "Missing cloud binary: $name" >&2; exit 1; }
done

head="$(gh api "repos/$repo/branches/main" --jq '.commit.sha')"
if [[ "$head" != "$target" ]]; then
  echo "Skip stale CI release for $target; main is $head"
  exit 0
fi
previous="$(gh api "repos/$repo/releases/tags/ci-latest" --jq '.target_commitish' 2>/dev/null || true)"
if [[ "$previous" == "$target" ]]; then
  echo "ci-latest already points to this commit"
  exit 0
fi

binaries=(
  "$dist/satellited"
  "$dist/satellite"
  "$dist/satellite-data-downloader"
  "$dist/satellite-checksum-sidecar"
)
backup=()
if [[ -n "$previous" ]]; then
  [[ "$previous" =~ ^[0-9a-f]{40}$ ]] || { echo "Unexpected ci-latest target" >&2; exit 1; }
  scratch="$(mktemp -d)"
  mkdir -p "$scratch/old" "$scratch/archive"
  gh release download ci-latest --repo "$repo" --dir "$scratch/old"
  assets="$(gh api "repos/$repo/releases/tags/ci-latest" --jq '.assets[] | [.name, .digest] | @tsv')"
  while IFS=$'\t' read -r name expected; do
    [[ "$name" =~ ^(satellite|satellited|satellite-data-downloader|satellite-checksum-sidecar)(-prev[12]-[0-9a-f]{12})?$ ]] || { echo "Unexpected release asset: $name" >&2; exit 1; }
    [[ "$expected" == "sha256:$(sha256sum "$scratch/old/$name" | cut -d' ' -f1)" ]] || { echo "Invalid $name release digest" >&2; exit 1; }
  done <<< "$assets"
  backup=("$scratch/old"/*)
  for prefix in "${all_binaries[@]}"; do
    for archived in "$scratch/old"/"${prefix}"-prev1-*; do
      [[ -f "$archived" ]] || continue
      older="$scratch/archive/$(basename "${archived/-prev1-/-prev2-}")"
      cp "$archived" "$older"
      binaries+=("$older")
    done
  done
  for name in satellite satellited; do
    [[ -f "$scratch/old/$name" ]] || { echo "Missing $name release asset" >&2; exit 1; }
    archived="$scratch/archive/${name}-prev1-${previous:0:12}"
    cp "$scratch/old/$name" "$archived"
    binaries+=("$archived")
  done
  has_dl=0
  has_sc=0
  [[ -f "$scratch/old/satellite-data-downloader" ]] && has_dl=1
  [[ -f "$scratch/old/satellite-checksum-sidecar" ]] && has_sc=1
  [[ "$has_dl" == "$has_sc" ]] || { echo "Incomplete dataplane release pair in previous ci-latest" >&2; exit 1; }
  if [[ "$has_dl" == 1 ]]; then
    for name in satellite-data-downloader satellite-checksum-sidecar; do
      archived="$scratch/archive/${name}-prev1-${previous:0:12}"
      cp "$scratch/old/$name" "$archived"
      binaries+=("$archived")
    done
  fi
  for binary in "${binaries[@]}"; do
    [[ -f "$binary" ]] || { echo "Missing release binary: $binary" >&2; exit 1; }
  done
  gh release delete ci-latest --repo "$repo" --yes --cleanup-tag
fi

if gh release create ci-latest "${binaries[@]}" --repo "$repo" --target "$target" \
    --title "CI latest" --notes "Rolling CI build for $target (latest plus two earlier builds)" --prerelease; then
  exit 0
fi
if [[ -n "$previous" ]]; then
  gh release delete ci-latest --repo "$repo" --yes --cleanup-tag || true
  gh release create ci-latest "${backup[@]}" --repo "$repo" --target "$previous" \
    --title "CI latest" --notes "Restored CI build for $previous" --prerelease
fi
exit 1

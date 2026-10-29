#!/usr/bin/env python3
"""Publish complete rolling bundles while reusing verified, unaffected binaries."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile

import plan

MANAGED = tuple(plan.BINARIES.values()) + ("deployment.tar.gz",)
SHA = re.compile(r"[0-9a-f]{40}")
HISTORY = re.compile(r"(satellite|satellited|satellite-data-downloader|satellite-checksum-sidecar)-prev([12])-([0-9a-f]{12})")


def digest(path):
    checksum = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1 << 20), b""):
            checksum.update(chunk)
    return checksum.hexdigest()


def gh(*args):
    return subprocess.check_output(["gh", *args], text=True, stderr=subprocess.PIPE)


def metadata(repo, tag):
    try:
        return json.loads(gh("api", "repos/" + repo + "/releases/tags/" + tag))
    except subprocess.CalledProcessError as error:
        if "HTTP 404" in (error.stderr or ""):
            return None
        raise


def valid_name(name):
    return name in MANAGED + ("build-manifest.json", "SHA256SUMS") or HISTORY.fullmatch(name) or re.fullmatch(r"[A-Za-z0-9_-]+\.png", name)


def verify_download(meta, directory):
    assets = meta["assets"]
    names = [asset["name"] for asset in assets]
    if not names or len(names) != len(set(names)):
        raise ValueError("Missing or duplicate release assets")
    for asset in assets:
        name = asset["name"]
        if not valid_name(name) or not (directory / name).is_file():
            raise ValueError("Unexpected or missing release asset: " + name)
        if asset.get("digest") != "sha256:" + digest(directory / name):
            raise ValueError("Invalid GitHub release digest: " + name)
    manifest_path = directory / "build-manifest.json"
    manifest = None
    if manifest_path.exists():
        manifest = json.loads(manifest_path.read_text())
        if manifest.get("schema") != 1 or not isinstance(manifest.get("partial"), bool):
            raise ValueError("Unsupported build manifest")
        if manifest["target_sha"] != meta["target_commitish"]:
            raise ValueError("Manifest target does not match release")
        expected = {name for name in names if name in MANAGED}
        if set(manifest["assets"]) != expected:
            raise ValueError("Manifest asset set does not match release")
        for name, entry in manifest["assets"].items():
            if not SHA.fullmatch(entry["source_sha"]) or entry["sha256"] != digest(directory / name):
                raise ValueError("Invalid asset provenance: " + name)
        checksums = {}
        for line in (directory / "SHA256SUMS").read_text().splitlines():
            checksum, name = line.split("  ", 1)
            if name in checksums:
                raise ValueError("Duplicate checksum: " + name)
            checksums[name] = checksum
        if set(checksums) != set(names) - {"SHA256SUMS"}:
            raise ValueError("Incomplete release checksums")
        for name, checksum in checksums.items():
            if checksum != digest(directory / name):
                raise ValueError("Invalid release checksum: " + name)
    slots = {0: set(), 1: set(), 2: set()}
    commits = {}
    for name in names:
        match = HISTORY.fullmatch(name)
        if match:
            slot = int(match[2])
            if slot in commits and commits[slot] != match[3]:
                raise ValueError("Mixed bundle commits in history slot")
            commits[slot] = match[3]
            slots[slot].add(match[1])
        elif name in plan.BINARIES.values():
            slots[0].add(name)
    for slot, binaries in slots.items():
        if slot == 0 and manifest and manifest["partial"]:
            continue
        control = {"satellite", "satellited"} & binaries
        dataplane = {"satellite-data-downloader", "satellite-checksum-sidecar"} & binaries
        if len(control) == 1 or len(dataplane) == 1 or (dataplane and not control):
            raise ValueError("Incomplete binary pairs in bundle slot " + str(slot))
    return manifest


def assemble(dist, target, previous_dir=None, previous_meta=None):
    previous = previous_meta["target_commitish"] if previous_meta else ""
    selection = plan.plan(previous, target)
    required = {m["binary"] for m in selection["matrix"]["include"] if m["binary"]}
    if selection["deployment"]:
        required.add("deployment.tar.gz")
    old_manifest = verify_download(previous_meta, previous_dir) if previous_meta else None
    entries = {}
    for name in MANAGED:
        if (dist / name).is_file():
            source = target
        else:
            if name in required or previous_dir is None or not (previous_dir / name).is_file():
                raise ValueError("Missing required cloud build: " + name)
            source = old_manifest["assets"][name]["source_sha"] if old_manifest else previous
            shutil.copyfile(previous_dir / name, dist / name)
        entries[name] = {"source_sha": source, "sha256": digest(dist / name)}
    # History slots identify bundle targets, not individual build sources.
    if previous_meta:
        for name in plan.BINARIES.values():
            if (previous_dir / name).exists():
                shutil.copyfile(previous_dir / name, dist / (name + "-prev1-" + previous[:12]))
        for old in previous_dir.iterdir():
            match = HISTORY.fullmatch(old.name)
            if match and match[2] == "1":
                shutil.copyfile(old, dist / old.name.replace("-prev1-", "-prev2-"))
    write_manifest(dist, target, entries, partial=False)


def write_manifest(dist, target, entries, partial):
    (dist / "build-manifest.json").write_text(json.dumps({
        "schema": 1, "target_sha": target, "partial": partial, "assets": entries,
    }, indent=2, sort_keys=True) + "\n")
    files = sorted(p for p in dist.iterdir() if p.is_file() and p.name != "SHA256SUMS")
    (dist / "SHA256SUMS").write_text("".join(digest(p) + "  " + p.name + "\n" for p in files))


def create(repo, tag, target, directory, title, notes):
    files = [str(p) for p in sorted(directory.iterdir()) if p.is_file()]
    gh("release", "create", tag, *files, "--repo", repo, "--target", target,
       "--title", title, "--notes", notes, "--prerelease")


def download_verified(repo, tag, directory):
    meta = metadata(repo, tag)
    if meta is None:
        raise ValueError("Release disappeared: " + tag)
    directory.mkdir()
    gh("release", "download", tag, "--repo", repo, "--dir", str(directory))
    verify_download(meta, directory)
    return meta


def verify(repo, tag, expected, directory):
    meta = download_verified(repo, tag, directory)
    target = meta["target_commitish"]
    if not SHA.fullmatch(target) or (expected and target != expected):
        raise ValueError("Release targets unexpected commit")
    names = {a["name"] for a in meta["assets"]}
    manifest_path = directory / "build-manifest.json"
    if manifest_path.exists():
        manifest = json.loads(manifest_path.read_text())
        if tag == "ci-latest" and (manifest["partial"] or not set(MANAGED) <= names):
            raise ValueError("Incomplete rolling bundle")
        # Inherited assets must still match all their inputs at this target.
        for name, entry in manifest["assets"].items():
            selection = plan.plan(entry["source_sha"], target)
            affected = {m["binary"] for m in selection["matrix"]["include"]}
            if name in affected or (name == "deployment.tar.gz" and selection["deployment"]):
                raise ValueError("Inherited asset has changed inputs: " + name)
    elif not set(plan.BINARIES.values()) <= names:
        raise ValueError("Missing latest monorepo binaries")
    print("Verified", tag, "target", target, "assets", len(names))


def publish(repo, target, dist, pr):
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repo) or not SHA.fullmatch(target):
        raise ValueError("Missing or invalid repository / target SHA")
    with tempfile.TemporaryDirectory(prefix="satellite-release-") as temporary:
        scratch = Path(temporary)
        if pr:
            if not pr.isdigit():
                raise ValueError("Invalid PR number")
            tag = "ci-pr-" + pr + "-" + target[:12]
            entries = {p.name: {"source_sha": target, "sha256": digest(p)}
                       for p in dist.iterdir() if p.is_file() and p.name in MANAGED}
            if not entries:
                raise ValueError("No changed cloud artifacts")
            write_manifest(dist, target, entries, partial=True)
            if metadata(repo, tag):
                verify(repo, tag, target, scratch / "verify")
                return
            create(repo, tag, target, dist, "CI PR " + pr + " " + target[:12],
                   "Cloud-verified changed modules for " + target + ". Partial bundle; see build-manifest.json.")
            verify(repo, tag, target, scratch / "verify")
            return

        head = json.loads(gh("api", "repos/" + repo + "/branches/main"))["commit"]["sha"]
        if head != target:
            print("Skip stale CI release for", target, "; main is", head)
            return
        previous_meta = metadata(repo, "ci-latest")
        if previous_meta and previous_meta["target_commitish"] == target:
            verify(repo, "ci-latest", target, scratch / "verify")
            return
        previous_dir = scratch / "previous" if previous_meta else None
        if previous_meta:
            previous_meta = download_verified(repo, "ci-latest", previous_dir)
        assemble(dist, target, previous_dir, previous_meta)
        # Concurrent pushes are cumulative; check main again after assembly.
        head = json.loads(gh("api", "repos/" + repo + "/branches/main"))["commit"]["sha"]
        if head != target:
            print("Skip stale CI release after assembly; main is", head)
            return
        try:
            if previous_meta:
                gh("release", "delete", "ci-latest", "--repo", repo, "--yes", "--cleanup-tag")
            create(repo, "ci-latest", target, dist, "CI latest",
                   "Cloud-verified bundle for " + target + ". Unchanged modules retain their source SHA in build-manifest.json; latest plus two earlier bundles.")
            verify(repo, "ci-latest", target, scratch / "verify")
        except Exception:
            # Restore the exact earlier bundle on creation OR read-back failure.
            if metadata(repo, "ci-latest"):
                gh("release", "delete", "ci-latest", "--repo", repo, "--yes", "--cleanup-tag")
            if previous_meta:
                create(repo, "ci-latest", previous_meta["target_commitish"], previous_dir,
                       previous_meta["name"], previous_meta.get("body") or "Restored CI bundle")
                verify(repo, "ci-latest", previous_meta["target_commitish"], scratch / "restore-verify")
            raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("publish", "verify"))
    parser.add_argument("--tag", default="ci-latest")
    parser.add_argument("--expected-sha", default="")
    args = parser.parse_args()
    repo = os.environ.get("REPO", os.environ.get("GITHUB_REPOSITORY", ""))
    if args.command == "publish":
        publish(repo, os.environ.get("TARGET_SHA", os.environ.get("GITHUB_SHA", "")),
                Path(os.environ.get("DIST_DIR", "dist")), os.environ.get("PR_NUMBER", ""))
    else:
        with tempfile.TemporaryDirectory(prefix="satellite-verify-") as temporary:
            verify(repo, args.tag, args.expected_sha, Path(temporary) / "release")


if __name__ == "__main__":
    main()

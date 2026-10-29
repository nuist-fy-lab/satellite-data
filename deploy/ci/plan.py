#!/usr/bin/env python3
"""Select CI modules from local Go imports and the complete unpublished diff."""

import argparse
from functools import lru_cache
import json
import os
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parents[2]
BINARIES = {
    "control": "satellited",
    "cli": "satellite",
    "downloader": "satellite-data-downloader",
    "sidecar": "satellite-checksum-sidecar",
}
RACE = {
    "internal/state", "internal/downloadcoord", "internal/httpapi",
    "internal/central", "internal/dataplane/access", "internal/dataplane/store",
    "internal/dataplane/server", "internal/dataplane/transfer",
    "internal/dataplane/pipeline",
}
MIGRATION = {
    "deploy/migrate-satellite-state.py", "deploy/plan-normalization.py",
    "deploy/prepare-satellite-copy.py", "deploy/migrate-satellite-bulk-shadow.py",
}


def git(*args):
    return subprocess.check_output(["git", *args], cwd=ROOT)


def graph_from_sources(sources):
    module = re.search(r"^module\s+(\S+)", sources["go.mod"], re.M)[1]
    imports = re.compile(r'["`]' + re.escape(module) + r'/([^"`\s]+)["`]')
    graph = {}
    # Include tests and every build tag: selection is conservative across platforms.
    for name, source in sources.items():
        if name.endswith(".go"):
            package = Path(name).parent.as_posix()
            graph.setdefault(package, set()).update(imports.findall(source))
    return graph


def dependency_graph(root):
    sources = {"go.mod": (root / "go.mod").read_text()}
    for directory in ("cmd", "internal", "web"):
        for path in (root / directory).rglob("*.go"):
            sources[path.relative_to(root).as_posix()] = path.read_text()
    return graph_from_sources(sources)


@lru_cache(maxsize=8)
def revision_graph(root, revision):
    # Read object IDs in one batch: this also works for an old release target
    # after main has changed its imports, without modifying the checkout.
    tree = subprocess.check_output([
        "git", "ls-tree", "-r", "-z", revision, "--", "go.mod", "cmd", "internal", "web",
    ], cwd=root)
    files = []
    for record in tree.split(b"\0"):
        if record:
            metadata, name = record.split(b"\t", 1)
            if name == b"go.mod" or name.endswith(b".go"):
                files.append((name.decode(), metadata.split()[2]))
    objects = subprocess.check_output(
        ["git", "cat-file", "--batch"], cwd=root,
        input=b"".join(object_id + b"\n" for _, object_id in files),
    )
    sources, offset = {}, 0
    for name, _ in files:
        end = objects.index(b"\n", offset)
        size = int(objects[offset:end].split()[2])
        sources[name] = objects[end + 1:end + 1 + size].decode()
        offset = end + size + 2
    return graph_from_sources(sources)


def closure(graph, package):
    found, pending = set(), [package]
    while pending:
        current = pending.pop()
        if current not in found:
            found.add(current)
            pending.extend(graph.get(current, ()))
    return found


def select(paths, graph, force=False):
    changed = set()
    full = force
    web = deploy = deployment = postgres = False
    for name in paths:
        path = Path(name)
        if name.startswith(".github/") or name.startswith("deploy/ci/") or name in (
            "go.mod", "go.sum", "deploy/publish-ci-release.sh",
        ):
            full = True
        elif name.startswith("web/"):
            web = True
            if not (name.endswith(".test.mjs") or name == "web/visual-check.mjs"):
                changed.add("web")
        elif name.startswith(("internal/", "cmd/")):
            parents = [p.as_posix() for p in path.parents]
            package = next((p for p in parents if p in graph), None)
            if package:
                changed.add(package)
            elif path.suffix == ".go":
                # Deleted packages must still invalidate their consumers.
                changed.add(path.parent.as_posix())
            else:
                full = True
        elif name.startswith("deploy/"):
            if path.suffix != ".md":
                deploy = True
                deployment |= not name.startswith("deploy/tests/")
                postgres |= name in MIGRATION
        elif name.startswith("docs/") or path.suffix == ".md" or name == "LICENSE":
            continue
        else:
            # Unknown build/configuration inputs fail safe to the full pipeline.
            full = True

    matrix, covered = [], set()
    for module, binary in BINARIES.items():
        packages = closure(graph, "cmd/" + binary)
        covered.update(packages)
        if full or changed & packages:
            matrix.append({
                "module": module,
                "binary": binary,
                "packages": ["./" + p for p in sorted(packages)],
                "race_packages": ["./" + p for p in sorted(packages & RACE)],
            })
    # New standalone libraries must not silently miss CI before they are wired in.
    orphaned = set(graph) - covered
    affected = {p for p in orphaned if full or changed & closure(graph, p)}
    if affected:
        matrix.append({"module": "libraries", "binary": "",
                       "packages": ["./" + p for p in sorted(affected)],
                       "race_packages": ["./" + p for p in sorted(affected & RACE)]})
    postgres |= full or bool(changed & (closure(graph, "internal/state") | {"cmd/satellited"}))
    return {
        "matrix": {"include": matrix},
        "go": bool(matrix),
        "web": full or web,
        "deploy": full or deploy,
        "deployment": full or deployment,
        "postgres": postgres,
        "release": full or deployment or any(m["binary"] for m in matrix),
    }


def plan(base, head, force=False):
    # A missing/unrelated base requires a complete bootstrap build.
    if not base or subprocess.call(
        ["git", "merge-base", "--is-ancestor", base, head], cwd=ROOT,
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    ) != 0:
        force = True
        paths = []
    else:
        paths = git("diff", "--no-renames", "--name-only", "-z", base, head).decode().split("\0")
        paths = [p for p in paths if p]
    # Resolve symbolic refs before caching the dependency graph.
    revision = git("rev-parse", "--verify", head + "^{commit}").decode().strip()
    result = select(paths, revision_graph(ROOT, revision), force)
    result.update(base=base, head=head, paths=paths)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", default="")
    parser.add_argument("--head", default="HEAD")
    parser.add_argument("--full", action="store_true")
    args = parser.parse_args()
    result = plan(args.base, args.head, args.full)
    print(json.dumps(result, indent=2))
    if os.environ.get("GITHUB_OUTPUT"):
        with open(os.environ["GITHUB_OUTPUT"], "a") as output:
            for key in ("matrix", "go", "web", "deploy", "deployment", "postgres", "release"):
                output.write(key + "=" + json.dumps(result[key], separators=(",", ":")) + "\n")
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as summary:
            summary.write("### CI module selection\n\n")
            summary.write("Comparison base: `" + (result["base"] or "bootstrap (full)") + "`\n\n")
            for module in result["matrix"]["include"]:
                summary.write("- " + module["module"] + " (tests and build)\n")
            for key in ("web", "deploy", "postgres"):
                summary.write("- " + key + ": " + ("run" if result[key] else "skip") + "\n")


if __name__ == "__main__":
    main()

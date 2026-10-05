#!/usr/bin/env python3
"""Normalize extensionless EarthCARE ZIPs without overwriting existing payloads."""

import argparse
import collections
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import shutil
import stat
import tempfile
import zipfile


HDF5 = b"\x89HDF\r\n\x1a\n"


def digest(path):
    checksum = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024**2), b""):
            checksum.update(block)
    return checksum.hexdigest()


def record(journal, event):
    journal.write(json.dumps(event, sort_keys=True) + "\n")
    journal.flush()
    os.fsync(journal.fileno())


def normalize_package(package, root, rollback, journal):
    before = package.lstat()
    if not stat.S_ISREG(before.st_mode):
        raise ValueError("package is not a regular file")
    destination = rollback / package.relative_to(root)
    destination = destination.with_name(destination.name + ".zip")
    if os.path.lexists(destination):
        raise FileExistsError("rollback destination already exists")
    staged = []
    try:
        with zipfile.ZipFile(package) as archive:
            members = archive.infolist()
            targets = set()
            if not members or len(members) > 8:
                raise ValueError("unexpected package members")
            h5_count = 0
            for member in members:
                path = PurePosixPath(member.filename)
                mode = member.external_attr >> 16
                if (member.is_dir() or path.is_absolute() or ".." in path.parts or
                        "\\" in member.filename or stat.S_ISLNK(mode) or member.flag_bits & 1):
                    raise ValueError("unsafe ZIP member")
                if path.parts != (package.name, path.name):
                    raise ValueError("unexpected ZIP member directory")
                suffix = Path(path.name).suffix.lower()
                if suffix not in {".h5", ".hdr", ".eof"} or path.name[:-len(suffix)] != package.name:
                    raise ValueError("unexpected ZIP member filename")
                if path.name in targets or not 0 < member.file_size <= 2 * 1024**3:
                    raise ValueError("duplicate or oversized ZIP member")
                targets.add(path.name)
                h5_count += suffix == ".h5"
            if h5_count != 1:
                raise ValueError("package must contain exactly one HDF5 file")
            required = sum(member.file_size for member in members)
            if shutil.disk_usage(package.parent).free < required + 1024**3:
                raise OSError("insufficient free space")
            for member in members:
                target = package.parent / PurePosixPath(member.filename).name
                fd, temporary = tempfile.mkstemp(prefix=".earthcare-unpack-", suffix=".tmp", dir=package.parent)
                temporary = Path(temporary)
                staged.append({"temporary": temporary, "target": target})
                with os.fdopen(fd, "wb") as output, archive.open(member) as source:
                    written = 0
                    for block in iter(lambda: source.read(1024**2), b""):
                        written += len(block)
                        if written > member.file_size:
                            raise ValueError("ZIP member exceeds declared size")
                        output.write(block)
                    output.flush()
                    os.fsync(output.fileno())
                if written != member.file_size:
                    raise ValueError("ZIP member size mismatch")
                with temporary.open("rb") as output:
                    header = output.read(8)
                if target.suffix.lower() == ".h5" and header != HDF5:
                    raise ValueError("invalid HDF5 member header")
                if target.suffix.lower() in {".hdr", ".eof"} and not header.startswith(b"<?xml"):
                    raise ValueError("invalid XML member header")
                item = staged[-1]
                item["sha256"] = digest(temporary)
                item["exists"] = os.path.lexists(target)
                if item["exists"]:
                    info = target.lstat()
                    if not stat.S_ISREG(info.st_mode) or info.st_size != written or digest(target) != item["sha256"]:
                        raise ValueError("existing payload differs from ZIP member")
        after = package.lstat()
        if (before.st_ino, before.st_size, before.st_mtime_ns) != (after.st_ino, after.st_size, after.st_mtime_ns):
            raise ValueError("package changed during extraction")
        event = {"package": str(package), "rollback": str(destination), "size": before.st_size,
                 "mtime_ns": before.st_mtime_ns, "uid": before.st_uid,
                 "members": [{"path": str(item["target"]), "sha256": item["sha256"],
                              "created": not item["exists"]} for item in staged]}
        record(journal, dict(event, phase="planned"))
        for item in staged:
            if not item["exists"]:
                os.chmod(item["temporary"], 0o664)
                # A hard link publishes atomically and refuses concurrent overwrite.
                os.link(item["temporary"], item["target"])
            elif digest(item["target"]) != item["sha256"]:
                raise ValueError("existing payload changed before publication")
        destination.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        os.rename(package, destination)
        record(journal, dict(event, phase="complete"))
        return sum(not item["exists"] for item in staged)
    finally:
        for item in staged:
            try:
                item["temporary"].unlink()
            except FileNotFoundError:
                pass


def packages(root):
    for directory, dirs, files in os.walk(root, followlinks=False):
        dirs[:] = [name for name in dirs if not name.startswith((".", "_partial_quarantine"))
                   and name not in {"support", "manifests", "transfer_logs", "quarantine"}]
        for name in sorted(files):
            if name.startswith("ECA_") and "." not in name:
                yield Path(directory) / name


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", required=True, type=Path)
    parser.add_argument("--rollback", required=True, type=Path)
    parser.add_argument("--journal", required=True, type=Path)
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    if not args.apply:
        print(json.dumps({"packages": sum(1 for _ in packages(args.root)), "apply": False}))
        return
    os.umask(0o077)
    args.rollback.mkdir(parents=True, exist_ok=False, mode=0o700)
    args.journal.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    counts = collections.Counter()
    with args.journal.open("x", encoding="utf-8") as journal:
        for package in packages(args.root):
            try:
                counts["created_members"] += normalize_package(package, args.root, args.rollback, journal)
                counts["normalized"] += 1
            except Exception as error:
                counts["errors"] += 1
                record(journal, {"package": str(package), "phase": "error", "error": str(error)})
            if (counts["normalized"] + counts["errors"]) % 50 == 0:
                print(json.dumps(dict(counts)), flush=True)
    print(json.dumps(dict(counts)), flush=True)
    if counts["errors"]:
        raise SystemExit(1)


if __name__ == "__main__":
    main()

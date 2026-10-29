import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).parents[1]))
import release

OLD = "a" * 40
NEW = "b" * 40


def selection(*binaries, deployment=False):
    return {"matrix": {"include": [{"binary": name} for name in binaries]},
            "deployment": deployment}


def meta(directory, target=OLD):
    return {"target_commitish": target, "name": "CI latest", "body": "prior release",
            "assets": [{"name": p.name, "digest": "sha256:" + release.digest(p)}
                       for p in directory.iterdir() if p.is_file()]}


class BundleTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.old = self.root / "old"
        self.dist = self.root / "dist"
        self.old.mkdir()
        self.dist.mkdir()
        for name in release.MANAGED:
            (self.old / name).write_text("old " + name)
        self.old_meta = meta(self.old)

    def assemble(self, selected):
        with patch.object(release.plan, "plan", return_value=selected):
            release.assemble(self.dist, NEW, self.old, self.old_meta)

    def test_partial_build_keeps_all_current_assets_with_actual_sources(self):
        (self.dist / "satellite").write_text("new cli")
        self.assemble(selection("satellite"))
        manifest = release.verify_download(meta(self.dist, NEW), self.dist)
        self.assertFalse(manifest["partial"])
        self.assertEqual(set(manifest["assets"]), set(release.MANAGED))
        self.assertEqual(manifest["assets"]["satellite"]["source_sha"], NEW)
        self.assertEqual(manifest["assets"]["satellited"]["source_sha"], OLD)
        self.assertEqual((self.dist / "satellite").read_text(), "new cli")
        self.assertEqual((self.dist / "satellited").read_text(), "old satellited")
        self.assertTrue((self.dist / ("satellite-prev1-" + OLD[:12])).exists())

    def test_inherited_source_is_preserved_across_multiple_bundles(self):
        entries = {name: {"source_sha": "c" * 40, "sha256": release.digest(self.old / name)}
                   for name in release.MANAGED}
        release.write_manifest(self.old, OLD, entries, False)
        self.old_meta = meta(self.old)
        (self.dist / "satellite").write_text("new cli")
        self.assemble(selection("satellite"))
        manifest = release.verify_download(meta(self.dist, NEW), self.dist)
        self.assertEqual(manifest["assets"]["satellited"]["source_sha"], "c" * 40)

    def test_required_changed_binary_cannot_be_reused(self):
        with self.assertRaisesRegex(ValueError, "Missing required cloud build"):
            self.assemble(selection("satellited"))

    def test_required_changed_deployment_cannot_be_reused(self):
        with self.assertRaisesRegex(ValueError, "deployment.tar.gz"):
            self.assemble(selection(deployment=True))

    def test_bootstrap_requires_complete_build(self):
        with patch.object(release.plan, "plan", return_value=selection(*release.MANAGED, deployment=True)):
            with self.assertRaisesRegex(ValueError, "Missing required cloud build"):
                release.assemble(self.dist, NEW)

    def test_legacy_binary_only_release_can_upgrade_with_full_build(self):
        (self.old / "deployment.tar.gz").unlink()
        self.old_meta = meta(self.old)
        for name in release.MANAGED:
            (self.dist / name).write_text("new " + name)
        self.assemble(selection(*release.plan.BINARIES.values(), deployment=True))
        release.verify_download(meta(self.dist, NEW), self.dist)

    def test_history_retains_only_two_previous_slots(self):
        for name in ("satellite", "satellited"):
            (self.old / (name + "-prev1-" + "c" * 12)).write_text("older")
            (self.old / (name + "-prev2-" + "d" * 12)).write_text("oldest")
        self.old_meta = meta(self.old)
        self.assemble(selection())
        self.assertTrue((self.dist / ("satellite-prev2-" + "c" * 12)).exists())
        self.assertFalse((self.dist / ("satellite-prev2-" + "d" * 12)).exists())

    def test_mixed_and_incomplete_history_slots_are_rejected(self):
        first = self.old / ("satellite-prev1-" + "c" * 12)
        first.write_text("older cli")
        with self.assertRaisesRegex(ValueError, "Incomplete binary pairs"):
            release.verify_download(meta(self.old), self.old)
        (self.old / ("satellited-prev1-" + "d" * 12)).write_text("different bundle")
        with self.assertRaisesRegex(ValueError, "Mixed bundle commits"):
            release.verify_download(meta(self.old), self.old)

    def test_partial_pr_bundle_allows_one_rebuilt_binary(self):
        (self.dist / "satellite").write_text("new cli")
        entries = {"satellite": {"source_sha": NEW, "sha256": release.digest(self.dist / "satellite")}}
        release.write_manifest(self.dist, NEW, entries, True)
        manifest = release.verify_download(meta(self.dist, NEW), self.dist)
        self.assertTrue(manifest["partial"])

    def test_corrupted_previous_asset_blocks_reuse(self):
        (self.old / "satellited").write_text("corruption")
        with self.assertRaisesRegex(ValueError, "Invalid GitHub release digest"):
            self.assemble(selection())

    def test_manifest_checksums_and_target_are_verified(self):
        self.assemble(selection())
        current = meta(self.dist, NEW)
        current["target_commitish"] = OLD
        with self.assertRaisesRegex(ValueError, "Manifest target"):
            release.verify_download(current, self.dist)
        (self.dist / "SHA256SUMS").write_text("")
        with self.assertRaisesRegex(ValueError, "Incomplete release checksums"):
            release.verify_download(meta(self.dist, NEW), self.dist)

    def test_unexpected_paths_are_rejected_before_reading(self):
        bad = {"assets": [{"name": "../outside", "digest": "sha256:0"}]}
        with self.assertRaisesRegex(ValueError, "Unexpected or missing"):
            release.verify_download(bad, self.old)

    def test_readback_rejects_inherited_assets_with_changed_inputs(self):
        self.assemble(selection())
        with patch.object(release, "download_verified", return_value=meta(self.dist, NEW)), \
             patch.object(release.plan, "plan", return_value=selection("satellited")):
            with self.assertRaisesRegex(ValueError, "Inherited asset has changed inputs"):
                release.verify("owner/repo", "ci-latest", NEW, self.dist)

    def test_api_errors_are_not_mistaken_for_missing_release(self):
        for message in ("HTTP 403", "network failure"):
            with patch.object(release, "gh", side_effect=subprocess.CalledProcessError(1, "gh", stderr=message)):
                with self.assertRaises(subprocess.CalledProcessError):
                    release.metadata("owner/repo", "ci-latest")
        with patch.object(release, "gh", side_effect=subprocess.CalledProcessError(1, "gh", stderr="HTTP 404")):
            self.assertIsNone(release.metadata("owner/repo", "ci-latest"))

    def test_stale_run_does_not_mutate_release(self):
        with patch.object(release, "gh", return_value=json.dumps({"commit": {"sha": OLD}})) as gh:
            release.publish("owner/repo", NEW, self.dist, "")
        self.assertEqual(gh.call_count, 1)

    def test_publish_failure_and_readback_failure_restore_previous_bundle(self):
        for failure in ("create", "verify"):
            with self.subTest(failure=failure):
                remote = self.root / ("remote-" + failure)
                shutil.copytree(self.old, remote)
                restored = []

                def api(*args):
                    if args[0] == "api":
                        return json.dumps({"commit": {"sha": NEW}})
                    if args[:2] == ("release", "delete"):
                        shutil.rmtree(remote)
                        return ""
                    self.fail("Unexpected gh call: " + str(args))

                def get_meta(*args):
                    return meta(remote, OLD) if remote.exists() else None

                def download(repo, tag, directory):
                    shutil.copytree(remote, directory)
                    return meta(directory)

                def create(repo, tag, target, directory, title, notes):
                    if target == NEW and failure == "create":
                        raise RuntimeError("simulated create failure")
                    shutil.copytree(directory, remote)
                    restored.append(target)

                def verify(repo, tag, expected, directory):
                    if expected == NEW and failure == "verify":
                        raise RuntimeError("simulated readback failure")

                with patch.object(release, "gh", side_effect=api), \
                     patch.object(release, "metadata", side_effect=get_meta), \
                     patch.object(release, "download_verified", side_effect=download), \
                     patch.object(release, "create", side_effect=create), \
                     patch.object(release, "verify", side_effect=verify), \
                     patch.object(release.plan, "plan", return_value=selection()):
                    with self.assertRaisesRegex(RuntimeError, "simulated"):
                        release.publish("owner/repo", NEW, self.dist, "")
                self.assertEqual(restored[-1], OLD)
                self.assertEqual({p.name: p.read_bytes() for p in remote.iterdir()},
                                 {p.name: p.read_bytes() for p in self.old.iterdir()})


if __name__ == "__main__":
    unittest.main()

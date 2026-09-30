import importlib.util
from pathlib import Path
import tempfile
import unittest
import zipfile

spec = importlib.util.spec_from_file_location("normalize_packages", Path(__file__).parents[1] / "normalize-earthcare-packages.py")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class NormalizePackagesTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name) / "EarthCare"
        self.root.mkdir()
        self.rollback = self.root / ".rollback"
        self.rollback.mkdir()
        self.package = self.root / "ECA_TEST_20260930"
        self.journal = tempfile.TemporaryFile(mode="w+", encoding="utf-8")
        self.addCleanup(self.journal.close)
        self.payload = module.HDF5 + b"payload"

    def create_zip(self, member=None, payload=None):
        with zipfile.ZipFile(self.package, "w", compression=zipfile.ZIP_DEFLATED) as archive:
            archive.writestr(member or self.package.name + "/" + self.package.name + ".h5",
                             self.payload if payload is None else payload)
            archive.writestr(self.package.name + "/" + self.package.name + ".HDR", b'<?xml version="1.0"?>')

    def normalize(self):
        return module.normalize_package(self.package, self.root, self.rollback, self.journal)

    def test_extracts_members_and_preserves_original_zip(self):
        self.create_zip()
        original = self.package.read_bytes()
        self.assertEqual(self.normalize(), 2)
        self.assertEqual(self.package.with_suffix(".h5").read_bytes(), self.payload)
        self.assertEqual(self.package.with_suffix(".h5").stat().st_mode & 0o777, 0o664)
        self.assertEqual((self.rollback / (self.package.name + ".zip")).read_bytes(), original)
        self.assertFalse(self.package.exists())
        self.assertEqual(list(self.root.glob(".earthcare-unpack-*")), [])

    def test_matching_existing_payload_is_not_replaced(self):
        self.create_zip()
        target = self.package.with_suffix(".h5")
        target.write_bytes(self.payload)
        inode = target.stat().st_ino
        self.assertEqual(self.normalize(), 1)
        self.assertEqual(target.stat().st_ino, inode)

    def test_conflict_preserves_both_original_files(self):
        self.create_zip()
        target = self.package.with_suffix(".h5")
        target.write_bytes(module.HDF5 + b"different")
        with self.assertRaises(ValueError):
            self.normalize()
        self.assertTrue(self.package.exists())
        self.assertEqual(target.read_bytes(), module.HDF5 + b"different")
        self.assertFalse(self.package.with_suffix(".HDR").exists())

    def test_unsafe_members_and_invalid_headers_rejected(self):
        for member, payload in [("../escape.h5", self.payload),
                                (self.package.name + "/wrong.h5", self.payload),
                                (None, b"not HDF5")]:
            with self.subTest(member=member):
                self.create_zip(member, payload)
                with self.assertRaises(ValueError):
                    self.normalize()
                self.assertTrue(self.package.exists())
                self.assertEqual(list(self.root.glob(".earthcare-unpack-*")), [])

    def test_symlink_target_is_not_followed(self):
        self.create_zip()
        outside = Path(self.temp.name) / "outside"
        outside.write_bytes(self.payload)
        self.package.with_suffix(".h5").symlink_to(outside)
        with self.assertRaises(ValueError):
            self.normalize()
        self.assertTrue(self.package.exists())

    def test_existing_rollback_is_not_overwritten(self):
        self.create_zip()
        (self.rollback / (self.package.name + ".zip")).write_bytes(b"previous")
        with self.assertRaises(FileExistsError):
            self.normalize()
        self.assertTrue(self.package.exists())


if __name__ == "__main__":
    unittest.main()

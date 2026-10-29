import importlib.util
from pathlib import Path
import unittest
import subprocess
import tempfile
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("plan", Path(__file__).parents[1] / "plan.py")
plan = importlib.util.module_from_spec(spec)
spec.loader.exec_module(plan)


class SelectionTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.graph = plan.dependency_graph(plan.ROOT)

    def select(self, *paths):
        return plan.select(paths, self.graph)

    def modules(self, result):
        return {m["module"] for m in result["matrix"]["include"]}

    def test_documentation_skips_expensive_jobs(self):
        result = self.select("README.zh-CN.md", "docs/operations.md")
        self.assertEqual(self.modules(result), set())
        for job in ("web", "postgres", "deploy", "release"):
            self.assertFalse(result[job])

    def test_cli_only(self):
        result = self.select("cmd/satellite/config.go")
        self.assertEqual(self.modules(result), {"cli"})
        self.assertFalse(result["postgres"])
        self.assertFalse(result["web"])

    def test_embedded_web_rebuilds_control_only(self):
        result = self.select("web/static/styles.css")
        self.assertEqual(self.modules(result), {"control"})
        self.assertTrue(result["web"])
        self.assertFalse(result["postgres"])

    def test_web_tests_do_not_rebuild_binaries(self):
        result = self.select("web/era5.test.mjs")
        self.assertEqual(self.modules(result), set())
        self.assertTrue(result["web"])
        self.assertFalse(result["release"])

    def test_future_embedded_web_resources_rebuild_control(self):
        result = self.select("web/templates/new.html")
        self.assertEqual(self.modules(result), {"control"})

    def test_state_runs_postgres_without_dataplane_or_browser(self):
        result = self.select("internal/state/store.go")
        self.assertEqual(self.modules(result), {"control"})
        self.assertTrue(result["postgres"])
        self.assertFalse(result["web"])

    def test_shared_transfer_runs_downloader_and_sidecar(self):
        result = self.select("internal/dataplane/transfer/official.go")
        self.assertEqual(self.modules(result), {"downloader", "sidecar"})

    def test_ipc_contract_reaches_control_and_dataplane(self):
        result = self.select("internal/dataplane/model/task.go")
        self.assertEqual(self.modules(result), {"control", "downloader", "sidecar"})

    def test_embedded_configuration_reaches_consumers(self):
        result = self.select("internal/downloadfilter/profiles.json")
        self.assertEqual(self.modules(result), {"control", "cli"})
        self.assertTrue(result["postgres"])

    def test_migrations_run_database_and_deployment_checks(self):
        result = self.select("deploy/migrate-satellite-state.py")
        self.assertEqual(self.modules(result), set())
        self.assertTrue(result["postgres"])
        self.assertTrue(result["deployment"])

    def test_units_do_not_rebuild_go(self):
        result = self.select("deploy/systemd/c2ln1/satellited.conf")
        self.assertEqual(self.modules(result), set())
        self.assertTrue(result["deploy"])
        self.assertTrue(result["release"])

    def test_deploy_tests_do_not_publish(self):
        result = self.select("deploy/tests/test_verify_earthcare_archive.py")
        self.assertTrue(result["deploy"])
        self.assertFalse(result["release"])

    def test_dependency_and_ci_changes_force_all(self):
        for path in ("go.mod", "go.sum", ".github/workflows/ci.yml", "deploy/ci/plan.py", "new-build-input"):
            with self.subTest(path=path):
                result = self.select(path)
                self.assertEqual(self.modules(result), set(plan.BINARIES))
                self.assertTrue(all(result[k] for k in ("web", "postgres", "deploy", "deployment")))

    def test_deleted_package_and_transitive_test_dependencies(self):
        graph = {"cmd/satellited": {"internal/deleted"}, "internal/deleted": set()}
        result = plan.select(["internal/deleted/old.go"], graph)
        self.assertEqual(self.modules(result), {"control"})

    def test_new_unwired_libraries_are_tested(self):
        graph = dict(self.graph, **{"internal/new": {"internal/model"}})
        result = plan.select(["internal/model/model.go"], graph)
        self.assertIn("libraries", self.modules(result))


class DiffTests(unittest.TestCase):
    def test_unpublished_code_is_not_lost_after_a_documentation_push(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)

            def git(*args):
                return subprocess.check_output(["git", *args], cwd=root, stderr=subprocess.DEVNULL).decode().strip()

            git("init", "-q")
            git("config", "user.name", "CI fixture")
            git("config", "user.email", "ci@example.invalid")
            git("config", "commit.gpgsign", "false")
            (root / "go.mod").write_text("module example.invalid/satellite\n")
            (root / "cmd/satellited").mkdir(parents=True)
            source = root / "cmd/satellited/main.go"
            source.write_text("package main\n")
            git("add", ".")
            git("commit", "-qm", "published")
            published = git("rev-parse", "HEAD")
            source.write_text("package main\n// unpublished change\n")
            git("commit", "-qam", "unpublished control change")
            code = git("rev-parse", "HEAD")
            (root / "README.md").write_text("docs only\n")
            git("add", ".")
            git("commit", "-qm", "docs")
            with patch.object(plan, "ROOT", root):
                cumulative = plan.plan(published, "HEAD")
                event_only = plan.plan(code, "HEAD")
                fallback = plan.plan("0" * 40, "HEAD")
            self.assertEqual({m["module"] for m in cumulative["matrix"]["include"]}, {"control"})
            self.assertFalse(event_only["release"])
            self.assertTrue(all(fallback[k] for k in ("go", "web", "deploy", "postgres", "release")))

    def test_graph_uses_requested_commit_even_after_imports_change(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)

            def git(*args):
                return subprocess.check_output(["git", *args], cwd=root, stderr=subprocess.DEVNULL).decode().strip()

            git("init", "-q")
            git("config", "user.name", "CI fixture")
            git("config", "user.email", "ci@example.invalid")
            git("config", "commit.gpgsign", "false")
            (root / "go.mod").write_text("module example.invalid/satellite\n")
            (root / "cmd/satellited").mkdir(parents=True)
            (root / "internal/helper").mkdir(parents=True)
            source = root / "cmd/satellited/main.go"
            helper = root / "internal/helper/helper.go"
            source.write_text('package main\nimport `example.invalid/satellite/internal/helper`\n')
            helper.write_text("package helper\n")
            git("add", ".")
            git("commit", "-qm", "initial")
            base = git("rev-parse", "HEAD")
            helper.write_text("package helper\n// change\n")
            git("commit", "-qam", "helper change")
            target = git("rev-parse", "HEAD")
            source.write_text("package main\n// no longer imports helper\n")
            git("commit", "-qam", "later graph change")
            with patch.object(plan, "ROOT", root):
                result = plan.plan(base, target)
            self.assertEqual({m["module"] for m in result["matrix"]["include"]}, {"control"})


if __name__ == "__main__":
    unittest.main()

#!/usr/bin/env python3
"""Check dependency sharing without mutating a checkout or downloading modules."""
import pathlib
import subprocess
import sys
import tempfile
import unittest


class ProjectionTest(unittest.TestCase):
    def fixture(self, directory, version):
        root = pathlib.Path(directory)
        module = root / "resources"
        core = root / "reconcile"
        package = "example.com/shared"
        for owner, selected in [(module, version), (core, "v1.0.0")]:
            vendor = owner / "vendor"
            source = vendor / package
            source.mkdir(parents=True)
            (vendor / "modules.txt").write_text(f"# {package} {selected}\n")
            (source / "BUCK").write_text('dependency_library(name = "shared")\n')
            (source / "shared.go").write_text("package shared\n")
            (source / "LICENSE").write_text("upstream license\n")
        extra = module / "vendor/example.com/extra"
        extra.mkdir(parents=True)
        (extra / "BUCK").write_text(
            'dependency_library(name = "extra", deps = [\n'
            '"//packages/resources/vendor/example.com/shared:shared",\n'
            '"//packages/resources/vendor/github.com/p5/sir-robs-a-bot/packages/reconcile/datastore:datastore"\n'
            '])\n'
        )
        return module, core

    def run_projection(self, module, core):
        return subprocess.run(
            [sys.executable, str(pathlib.Path(__file__).with_name("project.py")), str(module), str(core)],
            capture_output=True,
            text=True,
            check=False,
        )

    def test_reuses_matching_package_and_local_module(self):
        with tempfile.TemporaryDirectory() as directory:
            module, core = self.fixture(directory, "v1.0.0")
            result = self.run_projection(module, core)
            self.assertEqual(result.returncode, 0, result.stderr)
            generated = (module / "vendor/example.com/extra/BUCK").read_text()
            self.assertIn('"//packages/reconcile/vendor/example.com/shared:shared"', generated)
            self.assertIn('"//packages/reconcile/datastore:datastore"', generated)
            self.assertFalse((module / "vendor/example.com/shared/shared.go").exists())
            self.assertTrue((module / "vendor/example.com/shared/LICENSE").exists())
            self.assertTrue((core / "vendor/example.com/shared/shared.go").exists())

    def test_rejects_different_module_version(self):
        with tempfile.TemporaryDirectory() as directory:
            module, core = self.fixture(directory, "v2.0.0")
            result = self.run_projection(module, core)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("matching module versions", result.stderr)
            self.assertTrue((module / "vendor/example.com/shared/shared.go").exists())

    def test_service_reuses_two_owners_without_rewriting_its_own_prefix(self):
        with tempfile.TemporaryDirectory() as directory:
            module, core = self.fixture(directory, "v1.0.0")
            shared = pathlib.Path(directory) / "shared-resources"
            package = "example.com/content"
            source = shared / "vendor" / package
            source.mkdir(parents=True)
            (shared / "vendor/modules.txt").write_text(f"# {package} v1.0.0\n")
            (source / "BUCK").write_text('dependency_library(name = "content")\n')
            own = module / "vendor" / package
            own.mkdir(parents=True)
            (own / "BUCK").write_text('dependency_library(name = "content")\n')
            (own / "content.go").write_text("package content\n")
            with (module / "vendor/modules.txt").open("a") as manifest:
                manifest.write(f"# {package} v1.0.0\n")
            target = module / "vendor/example.com/extra/BUCK"
            target.write_text(target.read_text().replace("packages/resources", "services/intake") +
                              '"//services/intake/vendor/example.com/content:content"\n' +
                              '//services/intake/vendor/github.com/p5/sir-robs-a-bot/packages/resources/content:content\n')
            result = subprocess.run(
                [sys.executable, str(pathlib.Path(__file__).with_name("project.py")),
                 str(module), str(core), "services/intake", str(shared)],
                capture_output=True, text=True, check=False,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            generated = target.read_text()
            self.assertIn("//packages/reconcile/vendor/example.com/shared:shared", generated)
            self.assertIn("//packages/resources/vendor/example.com/content:content", generated)
            self.assertIn("//packages/resources/content:content", generated)
            self.assertFalse((own / "content.go").exists())
            self.assertTrue((source / "BUCK").exists())


if __name__ == "__main__":
    unittest.main()

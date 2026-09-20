#!/usr/bin/env python3
"""Offline install contract: real keygen, generated mounts, permissions, Compose parsing."""
import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SETUP = ROOT / "deploy/compose/setup.py"


class InstallTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.build = tempfile.TemporaryDirectory(prefix="edgelab-install-test-")
        cls.binary = Path(cls.build.name) / "edgelab"
        subprocess.run(["go", "build", "-o", str(cls.binary), "./cmd/edgelab"], cwd=ROOT, check=True)

    @classmethod
    def tearDownClass(cls):
        cls.build.cleanup()

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name) / "installation with spaces"

    def run_setup(self, *extra):
        return subprocess.run([sys.executable, str(SETUP), "--directory", str(self.directory),
                               "--registry", "https://registry.example.net", "--repository", "team/app",
                               "--allow", r"^v[0-9]+$", "--binary", str(self.binary), *extra],
                              text=True, capture_output=True)

    def test_install_and_refuse_overwrite(self):
        result = self.run_setup()
        self.assertEqual(result.returncode, 0, result.stderr)
        compose = json.loads((self.directory / "compose.yaml").read_text())
        publisher, hub = (compose["services"][n] for n in ("publisher", "hub"))
        self.assertEqual(publisher["command"][0], "watch-registry")
        self.assertEqual(hub["ports"], ["127.0.0.1:8080:8080"])
        self.assertEqual(publisher["user"], f"100:{os.getgid()}")
        for service in (publisher, hub):
            self.assertEqual(service["build"]["target"], "daemon")
            self.assertEqual(service["build"]["context"], str(ROOT).replace("$", "$$"))
            self.assertNotIn("image", service)
            for mount in service["volumes"]:
                self.assertTrue((self.directory / mount["source"]).exists())
                self.assertFalse(mount["bind"]["create_host_path"])
        self.assertFalse(any("key" in m["source"] for m in hub["volumes"]))
        self.assertTrue(next(m for m in hub["volumes"] if m["target"] == "/origin")["read_only"])
        self.assertFalse(next(m for m in publisher["volumes"] if m["target"] == "/origin").get("read_only", False))
        config = json.loads((self.directory / "watcher.yaml").read_text())
        self.assertEqual(config["repos"], [{"name": "team/app", "allow": r"^v[0-9]+$"}])
        self.assertEqual(config["publish"]["sequence_file"], "/state/sequence")
        for path, mode in (("keys/publisher.key", 0o640), ("watcher-state", 0o770), ("origin", 0o770)):
            self.assertEqual(stat.S_IMODE((self.directory / path).stat().st_mode), mode)
        private = (self.directory / "keys/publisher.key").read_bytes()
        self.assertTrue(private)
        result = self.run_setup()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("refusing to overwrite", result.stderr)
        self.assertEqual((self.directory / "keys/publisher.key").read_bytes(), private)
        if shutil.which("docker"):
            subprocess.run(["docker", "compose", "-f", str(self.directory / "compose.yaml"), "config", "--quiet"], check=True)

    def test_auth_only_publisher(self):
        result = self.run_setup("--username", "alice", "--port", "18080")
        self.assertEqual(result.returncode, 0, result.stderr)
        compose = json.loads((self.directory / "compose.yaml").read_text())
        self.assertIn("REGISTRY_PASSWORD", compose["services"]["publisher"]["environment"])
        self.assertNotIn("environment", compose["services"]["hub"])
        self.assertEqual(compose["services"]["hub"]["ports"], ["127.0.0.1:18080:8080"])

    def test_invalid_input_does_not_create_directory(self):
        for extra in (("--registry", "https://u:p@registry.example.net"), ("--directory", "relative"), ("--port", "0")):
            with self.subTest(extra=extra):
                self.assertNotEqual(self.run_setup(*extra).returncode, 0)
                self.assertFalse(self.directory.exists())


if __name__ == "__main__":
    unittest.main()

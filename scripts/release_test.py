"""Packaging contract tests: real Helm, stubbed Go compilation; no network needed."""
import hashlib
import os
from pathlib import Path
import posixpath
import re
import shutil
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch
from urllib.parse import urlsplit

import release


class DocumentationTest(unittest.TestCase):
    def test_bundled_guides_assets_and_versioned_source_links(self):
        root = Path(__file__).resolve().parents[1]
        entries = {name: data for name, data, _ in release.install_entries(root, 'v0.1.1')}
        required = {'TESTING.md', 'GO_EMBEDDING.md', 'docs/ARCHITECTURE.md',
                    'docs/DOCKER_RUN.md', 'docs/GRAFANA.md',
                    'docs/diagrams/architecture.html', 'docs/diagrams/architecture.svg',
                    'docs/images/edge-delta-grafana-dashboard.png',
                    'examples/go-embedding/go.mod', 'examples/go-embedding/go.sum',
                    'examples/go-embedding/publisher/main.go',
                    'examples/go-embedding/receiver/main.go', 'examples/flaky.json'}
        self.assertTrue(required <= entries.keys())
        for name, data in entries.items():
            if not name.endswith('.md'):
                self.assertEqual(data, (root / release.INSTALL_FILES[name][0]).read_bytes())
                continue
            for target in re.findall(r'\]\(([^)]+)\)', data.decode()):
                parsed = urlsplit(target)
                if parsed.scheme or parsed.netloc or not parsed.path:
                    continue
                resolved = posixpath.normpath(posixpath.join(posixpath.dirname(name), parsed.path))
                self.assertIn(resolved, entries, (name, target))
            # Do not silently turn a source typo into a broken versioned web link.
            for target in re.findall(r'\]\(([^)]+)\)', (root / name).read_text()):
                parsed = urlsplit(target)
                if not parsed.scheme and not parsed.netloc and parsed.path:
                    self.assertTrue((root / name).parent.joinpath(parsed.path).exists(), (name, target))
        self.assertIn(b'](docs/diagrams/architecture.svg)', entries['README.md'])
        self.assertIn(b'](examples/go-embedding/publisher/main.go)', entries['GO_EMBEDDING.md'])
        self.assertIn(b'https://github.com/syscode-labs/edge-delta-lab/tree/v0.1.1/evidence/v0.1.1-install/README.md', entries['TESTING.md'])
        self.assertIn(b'https://github.com/syscode-labs/edge-delta-lab/tree/v0.1.1/internal/registry/publish.go', entries['docs/ARCHITECTURE.md'])
        self.assertIn(b'../TESTING.md#retained-proof', entries['docs/DOCKER_RUN.md'])


@unittest.skipUnless(shutil.which("helm"), "Helm is required for packaging tests")
class ReleaseTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.chart = self.root / "deploy/helm/edgelab-hub"
        source_root = Path(__file__).resolve().parents[1]
        for source, _ in release.INSTALL_FILES.values():
            destination = self.root / source
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(source_root / source, destination)
        shutil.copytree(source_root / "deploy/helm/edgelab-hub", self.chart, dirs_exist_ok=True)
        self.dist = self.root / "dist"
        self.dist.mkdir()
        (self.dist / "stale").write_text("remove on successful validation")
        self.real_run = subprocess.run
        self.builds = []

    def run_command(self, args, **kwargs):
        if args[0] != "go":
            return self.real_run(args, **kwargs)
        self.builds.append((args, kwargs))
        env = kwargs["env"]
        self.assertEqual(env["CGO_ENABLED"], "0")
        self.assertIn("-trimpath", args)
        self.assertIn("-buildvcs=false", args)
        self.assertIn("-ldflags=-s -w -buildid=", args)
        Path(args[args.index("-o") + 1]).write_bytes(
            f"{env['GOOS']}/{env['GOARCH']}:{args[-1]}".encode())
        return subprocess.CompletedProcess(args, 0)

    def package(self, epoch=0):
        with patch.object(release.subprocess, "run", side_effect=self.run_command):
            release.release(self.root, "v0.1.1", epoch)

    def test_version_mismatch_fails_before_clear_or_build(self):
        chart_file = self.chart / "Chart.yaml"
        original = chart_file.read_text()
        for old, new, field in (("version: 0.1.1", "version: 0.2.0", "version"),
                                ('appVersion: "v0.1.1"', 'appVersion: "v3"', "appVersion")):
            with self.subTest(field=field):
                chart_file.write_text(original.replace(old, new))
                with self.assertRaisesRegex(ValueError, f"Chart.yaml {field} must equal"):
                    self.package()
                self.assertTrue((self.dist / "stale").exists())
                self.assertEqual(self.builds, [])

    def test_invalid_version_and_epoch(self):
        for version in ("", "0.1.1", "v01.1.0", "v0.1.1/../bad"):
            with self.subTest(version=version), self.assertRaises(ValueError):
                release.release(self.root, version)
        for epoch in (-1, 2**32):
            with self.subTest(epoch=epoch), self.assertRaises(ValueError):
                release.release(self.root, "v0.1.1", epoch)
        self.assertTrue((self.dist / "stale").exists())

    def test_artifacts_members_and_checksums(self):
        self.package(epoch=1234567890)
        expected = {"edgelab-v0.1.1-linux-amd64.tar.gz", "edgelab-v0.1.1-linux-arm64.tar.gz",
                    "edgelab-v0.1.1-darwin-arm64.tar.gz", "edgelab-hub-0.1.1.tgz"}
        self.assertEqual({p.name for p in self.dist.iterdir()}, expected | {"SHA256SUMS"})
        self.assertEqual(len(self.builds), 6)
        sums = (self.dist / "SHA256SUMS").read_text().splitlines()
        self.assertEqual([line.split("  ")[1] for line in sums], sorted(expected))
        for line in sums:
            digest, name = line.split("  ")
            self.assertEqual(digest, hashlib.sha256((self.dist / name).read_bytes()).hexdigest())
            with tarfile.open(self.dist / name) as tar:
                members = tar.getmembers()
                self.assertEqual(tar.getnames(), sorted(tar.getnames()))
                for member in members:
                    self.assertTrue(member.isfile())
                    self.assertEqual((member.uid, member.gid, member.uname, member.gname), (0, 0, "", ""))
                    self.assertEqual(member.mtime, 1234567890)
                    expected_mode = release.INSTALL_FILES.get(member.name, (None, 0o755))[1]
                    self.assertEqual(member.mode, 0o644 if name.endswith(".tgz") else expected_mode)
                if name.endswith(".tar.gz"):
                    extras = list(release.INSTALL_FILES) if "-linux-" in name else []
                    self.assertEqual(tar.getnames(), sorted(["edgelab", "edgelab-exporter"] + extras))
                    platform = name.removeprefix("edgelab-v0.1.1-").removesuffix(".tar.gz").replace("-", "/")
                    for binary in release.BINARIES:
                        self.assertEqual(tar.extractfile(binary).read(), f"{platform}:./cmd/{binary}".encode())
                else:
                    self.assertIn("edgelab-hub/templates/deployment.yaml", tar.getnames())
                    self.assertIn("edgelab-hub/values.yaml", tar.getnames())
                    metadata = tar.extractfile("edgelab-hub/Chart.yaml").read().decode()
                    self.assertIn("version: 0.1.1", metadata)
                    self.assertIn("appVersion: v0.1.1", metadata)
        # Independent checksum reader accepts the manifest and rejects modified bytes.
        checker = shutil.which("shasum") or shutil.which("sha256sum")
        if checker:
            cmd = [checker] + (["-a", "256"] if Path(checker).name == "shasum" else []) + ["-c", "SHA256SUMS"]
            self.real_run(cmd, cwd=self.dist, check=True, capture_output=True)
            with (self.dist / sorted(expected)[0]).open("ab") as artifact:
                artifact.write(b"tampered")
            self.assertNotEqual(self.real_run(cmd, cwd=self.dist, capture_output=True).returncode, 0)

    def test_deterministic_despite_source_mtime_and_stale_outputs(self):
        self.package()
        first = {p.name: p.read_bytes() for p in self.dist.iterdir()}
        for path in self.chart.rglob("*"):
            os.utime(path, (1700000000, 1700000000))
        (self.dist / "stale").write_text("not distributable")
        self.package()
        self.assertEqual(first, {p.name: p.read_bytes() for p in self.dist.iterdir()})

    def test_extracted_linux_installer_needs_no_checkout(self):
        self.package()
        directory = self.root / 'extracted'
        directory.mkdir()
        with tarfile.open(self.dist / 'edgelab-v0.1.1-linux-amd64.tar.gz') as tar:
            # Contents are generated in this test, not an untrusted archive.
            for member in tar.getmembers():
                path = directory / member.name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(tar.extractfile(member).read())
                path.chmod(member.mode)
        result = subprocess.run([str(directory / 'install'), 'receiver', '--help'],
                                cwd='/', text=True, capture_output=True, check=True)
        self.assertIn('--public-key', result.stdout)
        self.assertIn('--allow-http', result.stdout)
        self.assertIn('--docker-load', result.stdout)
        for name, data, _ in release.install_entries(self.root, 'v0.1.1'):
            self.assertEqual((directory / name).read_bytes(), data)
        default = subprocess.run(['make', '-n'], cwd=directory,
                                 text=True, capture_output=True, check=True)
        self.assertIn('go build', default.stdout)
        self.assertNotIn('lifecycle.py', default.stdout)

    def test_failed_build_has_no_checksum_manifest(self):
        def fail_build(args, **kwargs):
            if args[0] == "go":
                raise subprocess.CalledProcessError(1, args)
            return self.real_run(args, **kwargs)
        with patch.object(release.subprocess, "run", side_effect=fail_build):
            with self.assertRaises(subprocess.CalledProcessError):
                release.release(self.root, "v0.1.1")
        self.assertFalse((self.dist / "SHA256SUMS").exists())


if __name__ == "__main__":
    unittest.main()

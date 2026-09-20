#!/usr/bin/env python3
"""Keep the source-build COPY list and context allowlist in sync with Go imports.

No Docker required: resolve the real transitive dependency graph for each release
architecture. Actual Docker builds remain the integration gate. The deliberately
small parser fails closed if the source/context layout changes.
"""
import os
from pathlib import Path
import re
import shlex
import subprocess
import unittest

ROOT = Path(__file__).resolve().parents[1]


def missing_source_roots(dockerfile, dockerignore, required):
    # Contract: context denies everything, then admits literal directories/files.
    rules = [line.strip() for line in dockerignore.splitlines()
             if line.strip() and not line.lstrip().startswith("#")]
    if not rules or rules[0] != "*" or any(
            not re.fullmatch(r"![\w./-]+", rule) for rule in rules[1:]):
        raise AssertionError("update context contract for new .dockerignore syntax")
    admitted = {rule[1:] for rule in rules[1:]}
    copied = set()
    in_build = False
    for line in dockerfile.splitlines():
        words = shlex.split(line) if line.startswith(("FROM ", "COPY ")) else []
        if not words:
            continue
        if words[0] == "FROM":
            in_build = words[-2:] == ["AS", "build"]
        elif in_build:
            sources, destination = words[1:-1], words[-1]
            for source in sources:
                if destination not in ("./", source):
                    raise AssertionError("update COPY contract for relocated source")
                copied.add(source)
    return {root for root in required if root not in copied or root not in admitted}


class DockerfileContractTest(unittest.TestCase):
    def test_all_transitive_cli_packages_reach_build_stage(self):
        dockerfile = (ROOT / "Dockerfile").read_text()
        dockerignore = (ROOT / ".dockerignore").read_text()
        commands = sorted(set(re.findall(r"\./cmd/[\w-]+", dockerfile)))
        self.assertTrue(commands, "no Go build targets found in Dockerfile")
        # Only main-module dependencies matter: external modules come from go.mod.
        template = '{{if .Module}}{{if .Module.Main}}{{.Dir}}{{end}}{{end}}'
        for arch in ("amd64", "arm64"):
            with self.subTest(arch=arch):
                result = subprocess.run(
                    [os.environ.get("GO", "go"), "list", "-mod=readonly", "-deps",
                     "-f", template, *commands], cwd=ROOT,
                    env={**os.environ, "GOOS": "linux", "GOARCH": arch,
                         "CGO_ENABLED": "0", "GOWORK": "off"},
                    text=True, capture_output=True, timeout=120)
                self.assertEqual(result.returncode, 0, result.stderr)
                directories = [Path(line).relative_to(ROOT) for line in
                               result.stdout.splitlines() if line.strip()]
                self.assertTrue(directories)
                required = {path.parts[0] + "/" for path in directories}
                required.update(("go.mod", "go.sum"))
                self.assertEqual(missing_source_roots(
                    dockerfile, dockerignore, required), set(),
                    "transitive CLI source missing from COPY or .dockerignore")

    def test_future_public_dependency_requires_copy_and_context_admission(self):
        dockerfile = "FROM golang:1.25 AS build\nCOPY cmd/ cmd/\n"
        dockerignore = "*\n!cmd/\n"
        required = {"cmd/", "futurepublic/"}
        self.assertEqual(missing_source_roots(dockerfile, dockerignore, required),
                         {"futurepublic/"})
        copied = dockerfile + "COPY futurepublic/ futurepublic/\n"
        admitted = dockerignore + "!futurepublic/\n"
        self.assertEqual(missing_source_roots(copied, dockerignore, required),
                         {"futurepublic/"})
        self.assertEqual(missing_source_roots(dockerfile, admitted, required),
                         {"futurepublic/"})
        self.assertEqual(missing_source_roots(copied, admitted, required), set())


if __name__ == "__main__":
    unittest.main()

#!/usr/bin/env python3
"""Focused Helm contracts. Requires helm and PyYAML; builds/runs Docker if available.

Run: python3 scripts/helm_contract_test.py [--render-only]
No Kubernetes cluster, published ports, host mounts or Docker socket mounts used.
"""
import argparse
import itertools
import json
from pathlib import Path
import shutil
import subprocess
import time
import uuid
import unittest

try:
    import yaml
except ImportError:
    yaml = None


@unittest.skipUnless(shutil.which("helm") and yaml, "requires helm and PyYAML")
class HelmContractTest(unittest.TestCase):
    def test_chart_contracts(self):
        render_contracts()


class DashboardContractTest(unittest.TestCase):
    def test_byte_totals_and_rates_are_separate(self):
        dashboard = json.loads((CHART / "dashboards/edgelab-delivery-cache.json").read_text())
        panels = {panel["id"]: panel for panel in dashboard["panels"]}
        self.assertEqual(len(panels), 10)
        totals = panels[1]
        self.assertEqual(totals["fieldConfig"]["defaults"]["unit"], "bytes")
        self.assertEqual(len(totals["targets"]), 1)
        self.assertTrue(totals["targets"][0]["expr"].startswith("edgelab_client_last_sync_download_bytes{"))
        throughput = panels[3]
        self.assertEqual(throughput["fieldConfig"]["defaults"]["unit"], "Bps")
        self.assertTrue(throughput["targets"][0]["expr"].startswith("rate(edgelab_client_verified_download_bytes_total{"))

ROOT = Path(__file__).resolve().parents[1]
CHART = ROOT / "deploy/helm/edgelab-hub"


def run(*args, check=True, timeout=60):
    result = subprocess.run(args, cwd=ROOT, text=True, capture_output=True, timeout=timeout)
    if check and result.returncode:
        raise RuntimeError(f"{' '.join(args)}\n{result.stdout}{result.stderr}")
    return result


def value(args, flag):
    assert args.count(flag) == 1, (flag, args)
    return args[args.index(flag) + 1]


def render_contracts():
    exporter = None
    # Include untouched defaults as well as every combination of these controls.
    cases = [(False, True, 0, [])]
    for enabled, events, rate in itertools.product((False, True), (False, True), (0, 7500)):
        settings = ["--set", f"exporter.enabled={str(enabled).lower()},"
                    f"hub.events={str(events).lower()},hub.rateKbit={rate}"]
        cases.append((enabled, events, rate, settings))
    for enabled, events, rate, settings in cases:
        run("helm", "lint", str(CHART), *settings)
        rendered = run("helm", "template", "contract", str(CHART), *settings).stdout
        docs = list(yaml.safe_load_all(rendered))
        deployment, = [doc for doc in docs if doc and doc.get("kind") == "Deployment"]
        containers = deployment["spec"]["template"]["spec"]["containers"]
        hub, = [c for c in containers if c["name"] == "hub"]
        args = hub["args"]
        assert all(isinstance(arg, str) for arg in args), args
        assert args[0] == "serve", args
        assert args.count("--hub") == 1, args
        # Go flag booleans must use =false, not a separate positional 'false'.
        assert args.count(f"--events={str(events).lower()}") == 1, args
        assert "--events" not in args, args
        assert value(args, "--rate-kbit") == str(rate), args
        assert value(args, "--cache-mib") == "128", args
        exporters = [c for c in containers if c["name"] == "exporter"]
        assert len(exporters) == int(enabled), containers
        if enabled:
            exporter, = exporters
            assert exporter["command"] == ["/usr/local/bin/edgelab-exporter"], exporter
            assert exporter["args"][0] == "--socket", exporter
            assert all(isinstance(arg, str) for arg in exporter["args"]), exporter
            assert value(exporter["args"], "--socket") == value(args, "--admin-socket")
            assert value(exporter["args"], "--listen") == "0.0.0.0:9108"
            assert value(exporter["args"], "--role") == "hub"
            assert value(exporter["args"], "--instance") == "edgelab-hub"
        print(f"PASS helm lint/render: exporter={enabled} events={events} rate={rate}"
              + (" (defaults)" if not settings else ""), flush=True)
    return exporter


def runtime_contract(exporter):
    if not shutil.which("docker"):
        print("SKIP Docker runtime: docker not installed")
        return
    available = run("docker", "info", check=False, timeout=30)
    if available.returncode:
        print(f"SKIP Docker runtime: daemon unavailable: {available.stderr.strip()}")
        return
    token = uuid.uuid4().hex[:12]
    image = f"edgelab-helm-contract:{token}"
    container = f"edgelab-helm-contract-{token}"
    try:
        print("Building actual Dockerfile daemon target...", flush=True)
        run("docker", "build", "--target", "daemon", "-t", image, ".", timeout=600)
        config = json.loads(run("docker", "image", "inspect", image).stdout)[0]["Config"]
        assert config["Entrypoint"] == ["/usr/local/bin/edgelab"], config
        # Kubernetes command replaces ENTRYPOINT; args replaces CMD. Exercise
        # exactly the rendered exporter command/args, not a hand-written shortcut.
        run("docker", "run", "-d", "--name", container, "--network", "none",
            "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
            "--entrypoint", exporter["command"][0], image,
            *exporter["command"][1:], *exporter["args"])
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            response = run("docker", "exec", container, "wget", "-qO-",
                           "http://127.0.0.1:9108/metrics", check=False, timeout=10)
            if response.returncode == 0:
                samples = [line for line in response.stdout.splitlines()
                           if line.startswith("edgelab_source_up{")]
                assert len(samples) == 1 and samples[0].endswith(" 0"), response.stdout
                assert 'role="hub"' in samples[0], samples
                assert 'instance="edgelab-hub"' in samples[0], samples
                print("PASS Docker daemon image: rendered exporter invocation serves /metrics; "
                      "missing admin socket reports source_up=0", flush=True)
                return
            if run("docker", "inspect", "-f", "{{.State.Running}}", container).stdout.strip() != "true":
                break
            time.sleep(0.2)
        logs = run("docker", "logs", container, check=False)
        raise AssertionError(f"Exporter did not serve metrics: {logs.stdout}{logs.stderr}")
    finally:
        run("docker", "rm", "-fv", container, check=False)
        run("docker", "image", "rm", image, check=False)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--render-only", action="store_true", help="skip optional Docker build/run")
    options = parser.parse_args()
    if not shutil.which("helm") or yaml is None:
        parser.error("requires helm and PyYAML (python3 -m pip install PyYAML)")
    exporter = render_contracts()
    if not options.render_only:
        runtime_contract(exporter)


if __name__ == "__main__":
    main()

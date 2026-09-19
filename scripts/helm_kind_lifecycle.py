#!/usr/bin/env python3
"""Real, disposable Kind lifecycle test; requires Docker, kind, helm and kubectl.

Builds the daemon from source. Uses a private kubeconfig and unique owned cluster
and image; never changes the user's Kubernetes context or prunes Docker. Evidence
contains compact commands/output, not kubeconfig credentials. Publishes a signed
synthetic archive on the host, loads the origin PVC, and verifies an in-cluster
watch client. This does not test Docker import or production storage durability.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
CHART = ROOT / "deploy/helm/edgelab-hub"


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def proof_pod(name, image, command, volumes, mounts):
    """No service-account credentials, signing key, or Docker access."""
    return {"apiVersion": "v1", "kind": "Pod", "metadata": {"name": name},
            "spec": {"restartPolicy": "Never", "automountServiceAccountToken": False,
                     "securityContext": {"runAsNonRoot": True, "runAsUser": 100, "fsGroup": 101},
                     "containers": [{"name": "proof", "image": image, "imagePullPolicy": "Never",
                                     "command": command, "volumeMounts": mounts,
                                     "securityContext": {"allowPrivilegeEscalation": False,
                                                         "capabilities": {"drop": ["ALL"]}}}],
                     "volumes": volumes}}


def validate_staged(state, release):
    assert state["phase"] == "staged" and state["release"] == release
    assert state["accepted_sequence"] == 1
    artifact = state["artifact"]
    assert artifact.startswith("/state/") and ".." not in Path(artifact).parts
    return artifact


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--evidence", type=Path, required=True,
                        help="new directory for transcript and result.json")
    options = parser.parse_args()
    options.evidence.mkdir(parents=True, exist_ok=False)
    token = uuid.uuid4().hex[:10]
    cluster = f"edgelab-helm-{token}"
    image = f"edgelab-helm-lifecycle:{token}"
    name = "lifecycle-edgelab-hub"
    result = {"cluster": cluster, "image": image, "checks": [], "passed": False}
    with tempfile.TemporaryDirectory(prefix="edgelab-helm-") as temp, \
            (options.evidence / "transcript.log").open("w") as log:
        env = dict(os.environ, KUBECONFIG=str(Path(temp) / "kubeconfig"))

        def redact(text):
            return text.replace(temp, "<owned-temp>").replace(str(ROOT), "<repo>").replace(str(Path.home()), "<home>")

        def run(*args, timeout=120, check=True):
            command = " ".join(shlex.quote(str(arg)) for arg in args)
            print(f"$ {redact(command)}", flush=True)
            log.write(f"$ {redact(command)}\n")
            log.flush()
            proc = subprocess.run(args, cwd=ROOT, env=env, text=True,
                                  stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                  timeout=timeout)
            lines = redact(proc.stdout).splitlines()
            if len(lines) > 40:
                lines = lines[:15] + [f"... {len(lines) - 30} lines omitted ..."] + lines[-15:]
            log.write("\n".join(lines) + f"\n[exit {proc.returncode}]\n")
            log.flush()
            if check and proc.returncode:
                raise RuntimeError(f"{command}\n{proc.stdout}")
            return proc

        def k(*args, **kwargs):
            return run("kubectl", "--context", f"kind-{cluster}", "-n", "lifecycle", *args, **kwargs)

        def h(*args):
            return run("helm", *args, "--kube-context", f"kind-{cluster}", "-n", "lifecycle")

        def execute(*args, check=True):
            return k("exec", f"deployment/{name}", "-c", "hub", "--", *args, check=check).stdout

        def passed(label):
            result["checks"].append(label)
            log.write(f"PASS {label}\n")
            log.flush()
            print(f"PASS {label}", flush=True)

        def claims():
            items = json.loads(k("get", "pvc", "-o", "json").stdout)["items"]
            assert len(items) == 2 and all(p["status"]["phase"] == "Bound" for p in items)
            return {p["metadata"]["name"]: p["metadata"]["uid"] for p in items}

        def healthy():
            k("rollout", "status", f"deployment/{name}", "--timeout=90s")
            assert "ok" in execute("wget", "-qO-", f"http://{name}:8080/healthz").lower()
            execute("test", "-S", "/state/admin.sock")
            # The daemon emits its first stats-snapshot after 10 seconds, not at startup.
            deadline = time.monotonic() + 30
            while k("exec", f"deployment/{name}", "-c", "hub", "--", "test", "-s", "/state/events.jsonl", check=False).returncode:
                if time.monotonic() >= deadline:
                    raise AssertionError("no durable event within 30 seconds")
                time.sleep(1)
            assert execute("id", "-u").strip() == "100"

        def persisted(original_claims):
            assert claims() == original_claims
            assert execute("cat", "/state/lifecycle-marker").strip() == token
            assert execute("cat", "/state/events.jsonl").startswith(event_snapshot)
            origin_proof()

        def origin_proof():
            # HTTP bytes must match every host-published object, not just health.
            for relative, expected in origin_hashes.items():
                url = f"http://{name}:8080/{relative}"
                actual = execute("sh", "-ec", f"wget -qO /state/proof-object {shlex.quote(url)}; sha256sum /state/proof-object")
                assert actual.split()[0] == expected, relative
            passed("PVC origin: desired signed manifest and all content objects match host SHA-256 via Service")

        def create_pod(spec):
            path = Path(temp) / (spec["metadata"]["name"] + ".json")
            path.write_text(json.dumps(spec))
            k("create", "-f", str(path))
            k("wait", "--for=condition=Ready", "pod/" + spec["metadata"]["name"], "--timeout=60s")

        attempted_cluster = False
        try:
            binary = Path(temp) / "edgelab"
            keys, fixtures, origin = (Path(temp) / folder for folder in ("keys", "fixtures", "origin"))
            release = "signed-lifecycle"
            run("go", "build", "-trimpath", "-o", str(binary), "./cmd/edgelab", timeout=180)
            run(str(binary), "keygen", "--out", str(keys))
            run(str(binary), "fixtures", "--out", str(fixtures), "--size-mib", "2")
            archive = fixtures / "app-a-v1.tar"
            run(str(binary), "publish", "--input", str(archive), "--root", str(origin),
                "--key", str(keys / "publisher.key"), "--release", release, "--sequence", "1")
            run(str(binary), "promote", "--root", str(origin), "--release", release)
            origin_hashes = {p.relative_to(origin).as_posix(): digest(p) for p in sorted(origin.rglob("*")) if p.is_file()}
            assert "releases/desired.json" in origin_hashes and any(p.startswith("chunks/") for p in origin_hashes)
            assert all(p.startswith(("releases/", "chunks/")) for p in origin_hashes)
            result["signed_delivery"] = {"release": release, "archive_sha256": digest(archive),
                                         "archive_bytes": archive.stat().st_size, "origin_sha256": origin_hashes}
            endpoint = json.loads(run("docker", "context", "inspect").stdout)[0]["Endpoints"]["docker"]["Host"]
            assert endpoint.startswith("unix://"), "requires a local Docker daemon"
            assert not env.get("DOCKER_HOST") or env["DOCKER_HOST"].startswith("unix://")
            run("git", "rev-parse", "HEAD")
            run("git", "diff", "--", "deploy/helm/edgelab-hub")
            run("kind", "version")
            run("helm", "version", "--short")
            run("docker", "version")
            run("docker", "build", "--target", "daemon", "-t", image, ".", timeout=600)
            attempted_cluster = True
            run("kind", "create", "cluster", "--name", cluster, "--kubeconfig", env["KUBECONFIG"],
                "--wait", "120s", timeout=240)
            run("kind", "load", "docker-image", image, "--name", cluster, timeout=180)
            k("version", "-o", "json")
            k("get", "nodes", "-o", "wide")
            k("get", "storageclass")
            settings = ["--set", f"image.repository={image.split(':')[0]},image.tag={token},image.pullPolicy=Never"]
            h("install", "lifecycle", str(CHART), "--create-namespace", *settings,
              "--wait", "--timeout", "90s")
            healthy()
            original_claims = claims()
            event_snapshot = execute("cat", "/state/events.jsonl")
            assert event_snapshot.strip()
            execute("sh", "-c", f"printf '%s\\n' {token} > /state/lifecycle-marker")
            failed_write = k("exec", f"deployment/{name}", "-c", "hub", "--", "touch", "/origin/must-not-write", check=False)
            assert failed_write.returncode != 0 and "Read-only file system" in failed_write.stdout
            passed("install: Ready, both PVCs Bound, service health, non-root UID, admin socket/event log, origin read-only")
            loader = proof_pod("origin-loader", image, ["sleep", "300"],
                               [{"name": "origin", "persistentVolumeClaim": {"claimName": name + "-origin"}}],
                               [{"name": "origin", "mountPath": "/origin", "readOnly": False}])
            create_pod(loader)
            # Copy ONLY published objects; the private key remains in the host temp dir.
            k("cp", str(origin) + "/.", "origin-loader:/origin")
            k("delete", "pod", "origin-loader", "--wait=true")
            origin_proof()
            k("create", "configmap", "publisher-public", "--from-file=publisher.pub=" + str(keys / "publisher.pub"))
            client = proof_pod("signed-client", image,
                               ["edgelab", "watch", "--manifest", f"http://{name}:8080/releases/desired.json",
                                "--base", f"http://{name}:8080", "--state", "/state", "--pub", "/keys/publisher.pub",
                                "--allow-http", "--poll", "1s"],
                               [{"name": "state", "emptyDir": {}}, {"name": "public", "configMap": {"name": "publisher-public"}}],
                               [{"name": "state", "mountPath": "/state"}, {"name": "public", "mountPath": "/keys", "readOnly": True}])
            create_pod(client)
            deadline = time.monotonic() + 90
            while True:
                response = k("exec", "signed-client", "--", "cat", "/state/state.json", check=False)
                state = json.loads(response.stdout) if response.returncode == 0 else {}
                if state.get("phase") == "staged":
                    break
                if time.monotonic() >= deadline:
                    k("logs", "signed-client")
                    raise AssertionError("watch did not stage signed release")
                time.sleep(1)
            artifact = validate_staged(state, release)
            actual_hash = k("exec", "signed-client", "--", "sha256sum", artifact).stdout.split()[0]
            actual_size = int(k("exec", "signed-client", "--", "stat", "-c", "%s", artifact).stdout.strip())
            assert actual_hash == digest(archive) and actual_size == archive.stat().st_size
            result["signed_delivery"].update(client_state=state, staged_sha256=actual_hash, staged_bytes=actual_size)
            k("logs", "signed-client")
            passed("in-cluster watch: public-key verification, phase=staged, reconstructed archive SHA-256 and size match host")
            k("delete", "pod", "signed-client", "--wait=true")
            k("delete", "configmap", "publisher-public")
            h("upgrade", "lifecycle", str(CHART), *settings, "--set",
              f"exporter.enabled=true,exporter.image.repository={image.split(':')[0]},exporter.image.tag={token},exporter.image.pullPolicy=Never,hub.events=false,hub.rateKbit=7500",
              "--wait", "--timeout", "90s")
            healthy()
            persisted(original_claims)
            deployment = json.loads(k("get", "deployment", name, "-o", "json").stdout)
            containers = deployment["spec"]["template"]["spec"]["containers"]
            assert len(containers) == 2
            assert "--events=false" in containers[0]["args"] and "7500" in containers[0]["args"]
            deadline = time.monotonic() + 30
            while True:
                metrics = execute("wget", "-qO-", f"http://{name}:9108/metrics", check=False)
                samples = [line for line in metrics.splitlines() if line.startswith("edgelab_source_up{")]
                if samples and all(line.endswith(" 1") for line in samples):
                    break
                if time.monotonic() >= deadline:
                    raise AssertionError(f"exporter did not reach source_up=1: {metrics}")
                time.sleep(1)
            passed("upgrade: exporter serves service metrics with source_up=1; events=false/rate=7500; PVC/state retained")
            old_pods = json.loads(k("get", "pods", "-o", "json").stdout)["items"]
            old_uids = {p["metadata"]["uid"] for p in old_pods}
            k("rollout", "restart", f"deployment/{name}")
            healthy()
            new_pods = json.loads(k("get", "pods", "-o", "json").stdout)["items"]
            assert not old_uids.intersection(p["metadata"]["uid"] for p in new_pods)
            persisted(original_claims)
            passed("restart: new pod UID, same PVC UIDs and persisted state/event log")
            h("rollback", "lifecycle", "1", "--wait", "--timeout", "90s")
            healthy()
            persisted(original_claims)
            deployment = json.loads(k("get", "deployment", name, "-o", "json").stdout)
            containers = deployment["spec"]["template"]["spec"]["containers"]
            assert len(containers) == 1 and "--events=true" in containers[0]["args"]
            assert containers[0]["args"][containers[0]["args"].index("--rate-kbit") + 1] == "0"
            h("history", "lifecycle")
            k("get", "pods,pvc,service", "-o", "wide")
            passed("rollback: revision 1 settings restored, exporter removed, PVC/state retained")
            h("uninstall", "lifecycle", "--wait", "--timeout", "90s")
            remaining = json.loads(k("get", "deployment,pod,service,pvc,secret", "-o", "json").stdout)["items"]
            assert remaining == [], remaining
            passed("uninstall: no release deployments/pods/services/PVCs/Helm secrets remain (PVC deletion is destructive)")
            result["passed"] = True
        except Exception as exc:
            result["error"] = redact(str(exc))
            if attempted_cluster:
                for args in [("get", "pods,pvc", "-o", "wide"), ("get", "events", "--sort-by=.lastTimestamp"),
                             ("logs", f"deployment/{name}", "--all-containers")]:
                    try:
                        k(*args, check=False)
                    except Exception:
                        pass
            raise
        finally:
            if attempted_cluster:
                result["cluster_cleanup"] = run("kind", "delete", "cluster", "--name", cluster, check=False).returncode == 0
            result["image_cleanup"] = run("docker", "image", "rm", image, check=False).returncode == 0
            (options.evidence / "result.json").write_text(json.dumps(result, indent=2) + "\n")
            if result["passed"]:
                assert result.get("cluster_cleanup") and result["image_cleanup"], "cleanup failed"


if __name__ == "__main__":
    main()

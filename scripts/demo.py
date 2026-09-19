#!/usr/bin/env python3
"""Run real HTTP transfers, SIGKILL/restart the agent, and retain measured evidence.

Python is only the experiment orchestrator. Chunking, signing, HTTP, durable cache,
retries and reconstruction are implemented by the Go binary.
"""
from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import time
import urllib.request
from sender import open_db, sample, snapshot, render

ROOT = Path(__file__).resolve().parents[1]


def dump(path: Path, value: object) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_text(json.dumps(value, indent=2) + "\n")
    os.replace(tmp, path)


def get(url: str) -> dict:
    with urllib.request.urlopen(url, timeout=3) as response:
        return json.load(response)


def available_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--size-mib", type=int, default=16)
    parser.add_argument("--patch-kib", type=int, default=32)
    parser.add_argument("--rate-kbit", type=int, default=5000)
    parser.add_argument("--workers", type=int, default=2)
    parser.add_argument("--work", type=Path, default=ROOT / "work" / "demo")
    args = parser.parse_args()
    work = args.work.resolve()
    if work.exists():
        raise SystemExit(f"Refusing to overwrite {work}; choose a new --work directory.")
    work.mkdir(parents=True)
    binary = ROOT / "bin" / "edgelab"
    if not binary.exists():
        subprocess.run(["go", "build", "-o", str(binary), "./cmd/edgelab"], cwd=ROOT, check=True)
    logs = work / "logs"
    logs.mkdir()
    origin, keys, fixtures = work / "origin", work / "keys", work / "fixtures"
    edge, skip_edge = work / "edge", work / "edge-skip"

    def run(*argv: str, name: str) -> dict | None:
        with (logs / f"{name}.out.json").open("w") as out, (logs / f"{name}.events.jsonl").open("w") as err:
            subprocess.run([str(binary), *map(str, argv)], stdout=out, stderr=err, check=True)
        text = (logs / f"{name}.out.json").read_text().strip()
        return json.loads(text) if text else None

    print("Generating high-entropy, layered fixtures and signed releases...", flush=True)
    run("keygen", "--out", keys, name="keygen")
    info = run("fixtures", "--out", fixtures, "--size-mib", args.size_mib,
               "--patch-kib", args.patch_kib, name="fixtures")
    releases = ["app-a-v1", "app-b-v1", "app-a-v2", "app-a-v3"]
    for sequence, release in enumerate(releases, 1):
        run("publish", "--root", origin, "--key", keys / "publisher.key",
            "--input", info[release]["file"], "--release", release,
            "--sequence", sequence, "--kind", "docker-archive",
            "--image-ids", info[release]["image_id"], name=f"publish-{release}")

    port = available_port()
    base = f"http://127.0.0.1:{port}"
    plan_path = work / "faults.json"
    dump(plan_path, {"rate_kbit": args.rate_kbit, "latency_ms": 15,
                     "fail_first": 2, "drop_every": 9, "drop_after_bytes": 8192,
                     "corrupt_first": 1})
    server_log = (logs / "origin.log").open("w")
    server = subprocess.Popen([str(binary), "serve", "--root", str(origin),
                               "--listen", f"127.0.0.1:{port}", "--faults", str(plan_path),
                               "--receipts-dir", str(work / "sender" / "receipts")],
                              stdout=server_log, stderr=server_log)

    sender_db = work / "sender" / "collector.sqlite"
    collector_log = (logs / "collector.log").open("w")
    collector = subprocess.Popen(["python3", str(ROOT / "scripts" / "sender.py"), "collect",
                                  "--origin", base, "--db", str(sender_db), "--interval", "0.25"],
                                  stdout=collector_log, stderr=collector_log)

    def command(release: str, state: Path = edge) -> list[str]:
        return [str(binary), "sync", "--manifest", f"{base}/releases/{release}.json",
                "--base", base, "--state", str(state), "--pub", str(keys / "publisher.pub"),
                "--receipt-url", base + "/receipts", "--device-id", state.name,
                "--allow-http", "--workers", str(args.workers), "--attempts", "30",
                "--backoff", "100ms", "--max-backoff", "2s", "--deadline", "24h"]

    rows: list[dict] = []
    layers_present: set[str] = set()

    def record(name: str, release: str, before: dict, started: float,
               summary: dict, cached_layers: set[str] | None = None) -> dict:
        after = get(base + "/stats")
        differences = {key: after[key] - before[key] for key in before if isinstance(before[key], (int, float))}
        present = layers_present if cached_layers is None else cached_layers
        baseline = sum(layer["gzip_bytes"] for layer in info[release]["layers"]
                       if layer["sha256"] not in present)
        row = {"scenario": name, "release": release, "elapsed_seconds": time.monotonic() - started,
               "server_delta": differences, "agent_summary": summary,
               "modeled_missing_layer_gzip_bytes": baseline}
        db = open_db(sender_db)
        sample(db, base)
        (work / "sender" / (name + "-status.txt")).write_text(render(snapshot(db)) + "\n")
        db.close()
        rows.append(row)
        if cached_layers is None:
            layers_present.update(layer["sha256"] for layer in info[release]["layers"])
        payload = differences["chunk_response_body_bytes"]
        metadata = differences["metadata_response_body_bytes"]
        print(f"{name:34s} chunks={payload:>10,d} B  metadata={metadata:>8,d} B  "
              f"reused={summary['reused_chunks']:>4}  retries={summary['retries']:>3}", flush=True)
        return row

    def transfer(name: str, release: str, state: Path = edge,
                 cached_layers: set[str] | None = None) -> dict:
        before = get(base + "/stats")
        started = time.monotonic()
        with (logs / f"{name}.out.json").open("w") as out, (logs / f"{name}.events.jsonl").open("w") as err:
            subprocess.run(command(release, state), stdout=out, stderr=err, check=True)
        summary = json.loads((logs / f"{name}.out.json").read_text())
        return record(name, release, before, started, summary, cached_layers)

    killed: subprocess.Popen | None = None
    try:
        for _ in range(100):
            try:
                get(base + "/stats")
                break
            except OSError:
                if server.poll() is not None:
                    raise RuntimeError("origin failed; see origin.log")
                time.sleep(0.05)
        else:
            raise RuntimeError("origin did not become ready")

        # Interrupt a genuine cold transfer; never mock the cache contents.
        before_cold = get(base + "/stats")
        started_cold = time.monotonic()
        killed_events = logs / "cold-killed.events.jsonl"
        with (logs / "cold-killed.out.json").open("w") as out, killed_events.open("w") as err:
            killed = subprocess.Popen(command("app-a-v1"), stdout=out, stderr=err)
            deadline = time.monotonic() + 120
            while time.monotonic() < deadline:
                text = killed_events.read_text()
                if text.count('"event":"chunk_committed"') >= 8:
                    killed.kill()  # Actual SIGKILL on Linux/macOS, not graceful cancellation.
                    killed.wait(timeout=10)
                    break
                if killed.poll() is not None:
                    raise RuntimeError("agent ended before the interrupt point")
                time.sleep(0.02)
            else:
                killed.kill()
                killed.wait()
                raise RuntimeError("no durable progress before interrupt deadline")
        time.sleep(0.15)
        before_resume = get(base + "/stats")
        envelope = json.loads((origin / "releases" / "app-a-v1.json").read_text())
        manifest = json.loads(base64.b64decode(envelope["payload"]))
        committed_paths = set()
        for chunk in manifest["chunks"]:
            cached = edge / "cache" / chunk["sha256"][:2] / chunk["sha256"]
            if cached.exists() and hashlib.sha256(cached.read_bytes()).hexdigest() == chunk["sha256"]:
                wire = chunk["encoded_sha256"]
                committed_paths.add(f"chunks/{wire[:2]}/{wire}.gz")
        assert committed_paths, "SIGKILL must leave real verified cache entries"
        with (logs / "cold-resumed.out.json").open("w") as out, (logs / "cold-resumed.events.jsonl").open("w") as err:
            subprocess.run(command("app-a-v1"), stdout=out, stderr=err, check=True)
        resumed = json.loads((logs / "cold-resumed.out.json").read_text())
        after_resume = get(base + "/stats")
        re_requested = [p for p in committed_paths if after_resume["object_requests"].get(p, 0)
                        != before_resume["object_requests"].get(p, 0)]
        assert not re_requested, f"durably committed chunks re-requested: {re_requested}"
        cold_row = record("cold-sigkill-and-resume", "app-a-v1", before_cold, started_cold, resumed)
        cold_row["sigkill_verified_chunks_preserved"] = len(committed_paths)
        cold_row["sigkill_preserved_chunks_re_requested"] = len(re_requested)
        # A second device snapshot at v1, used later to demonstrate skipped releases.
        shutil.copytree(edge, skip_edge)
        skip_layers = set(layer["sha256"] for layer in info["app-a-v1"]["layers"])

        shared = transfer("second-image-shared-base", "app-b-v1")
        assert shared["agent_summary"]["reused_raw_bytes"] > info["app-b-v1"]["artifact_bytes"] * 0.5
        delta = transfer("small-edit-in-large-layer", "app-a-v2")
        assert delta["agent_summary"]["downloaded_raw_bytes"] < info["app-a-v2"]["artifact_bytes"] * 0.15
        repeat = transfer("unchanged-repeat", "app-a-v2")
        assert repeat["server_delta"]["chunk_requests"] == 0
        transfer("inserted-bytes-resynchronize", "app-a-v3")
        transfer("skip-v1-directly-to-v3", "app-a-v3", skip_edge, skip_layers)

        envelope = json.loads((origin / "releases" / "app-a-v3.json").read_text())
        manifest = json.loads(base64.b64decode(envelope["payload"]))
        c = manifest["chunks"][0]
        corrupted = edge / "cache" / c["sha256"][:2] / c["sha256"]
        raw = bytearray(corrupted.read_bytes()); raw[len(raw) // 2] ^= 0x40
        corrupted.write_bytes(raw)
        repair = transfer("repair-one-corrupt-cache-chunk", "app-a-v3")
        assert repair["agent_summary"]["downloaded_chunks"] == 1

        final_stats = get(base + "/stats")
        collector.terminate(); collector.wait(timeout=5)
        subprocess.run(["python3", str(ROOT / "scripts" / "sender.py"), "report", "--db", str(sender_db),
                        "--out", str(work / "sender" / "report")], check=True)
        server.terminate(); server.wait(timeout=5)
        # Remove just the assembled archive. The origin is now genuinely stopped.
        state = json.loads((edge / "state.json").read_text())
        Path(state["artifact"]).unlink()
        offline = run("sync", "--offline", "--state", edge, "--pub", keys / "publisher.pub", name="offline-reassemble")
        assert offline["chunk_requests"] == 0 and offline["metadata_requests"] == 0

        result = {"format_version": 1, "fixture_size_mib": args.size_mib,
                  "patch_kib": args.patch_kib, "rate_kbit": args.rate_kbit,
                  "workers": args.workers, "fault_plan": json.loads(plan_path.read_text()),
                  "environment": {"go_version": subprocess.check_output(["go", "version"], text=True).strip(),
                                  "platform": os.uname().sysname, "docker_in_this_run": False},
                  "measurement_notes": [
                      "All transport is actual HTTP on loopback, paced at the configured aggregate body rate.",
                      "Server byte counters count body bytes accepted by its socket writer, not TCP/IP wire bytes.",
                      "Cold row includes the killed process; its agent_summary covers only the resumed invocation.",
                      "The native baseline is computed gzip size of missing fixture layers, not a measured registry pull.",
                      "Fixtures are high-entropy synthetic layered archives, not a representative production image corpus.",
                      "SIGKILL is tested. Actual device power loss and Docker import are not tested by this script."],
                  "scenarios": rows, "offline_reassembly": offline, "server_totals": final_stats,
                  "assertions": {"committed_chunks_not_refetched_after_sigkill": True,
                                 "second_image_reuses_shared_content": True,
                                 "small_edit_transfers_substantially_less_than_image": True,
                                 "repeat_transfers_zero_chunk_payload": True,
                                 "single_bad_cache_chunk_repaired": True,
                                 "offline_reconstruction_without_origin": True}}
        dump(work / "results.json", result)
        lines = ["# Measured demo results", "", f"Fixture payload: {args.size_mib} MiB per image; edit: {args.patch_kib} KiB; link: {args.rate_kbit} kbit/s.", "",
                 "| Scenario | Chunk response body bytes | Metadata body bytes | Seconds |", "|---|---:|---:|---:|"]
        for row in rows:
            delta = row["server_delta"]
            lines.append(f"| {row['scenario']} | {delta['chunk_response_body_bytes']:,} | {delta['metadata_response_body_bytes']:,} | {row['elapsed_seconds']:.3f} |")
        lines += ["", f"Verified chunks preserved across SIGKILL: {len(committed_paths)}; re-requested: {len(re_requested)}.",
                  "Offline reassembly completed with zero HTTP requests.", "", "## Measurement limits", ""]
        lines += ["- " + note for note in result["measurement_notes"]]
        (work / "RESULTS.md").write_text("\n".join(lines) + "\n")
        print(f"\nAssertions passed. Evidence: {work / 'results.json'}", flush=True)
    finally:
        if collector.poll() is None:
            collector.terminate(); collector.wait(timeout=5)
        collector_log.close()
        if killed is not None and killed.poll() is None:
            killed.kill(); killed.wait()
        if server.poll() is None:
            server.terminate()
            try:
                server.wait(timeout=5)
            except subprocess.TimeoutExpired:
                server.kill(); server.wait()
        server_log.close()


if __name__ == "__main__":
    main()

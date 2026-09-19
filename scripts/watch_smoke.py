#!/usr/bin/env python3
"""Check that a single long-running agent discovers a signed channel promotion."""
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile
import time
from demo import ROOT, available_port, get


def main():
    binary = ROOT / "bin" / "edgelab"
    with tempfile.TemporaryDirectory(prefix="edge-watch-") as temp:
        work = Path(temp)
        origin, keys, edge = work / "origin", work / "keys", work / "edge"
        subprocess.run([str(binary), "keygen", "--out", str(keys)], check=True)
        for sequence in (1, 2):
            body = bytearray(hashlib.shake_256(b"watch-fixture").digest(512 << 10))
            if sequence == 2:
                body[200000:201000] = b"X" * 1000
            source = work / f"v{sequence}.tar"
            source.write_bytes(body)
            subprocess.run([str(binary), "publish", "--input", str(source), "--root", str(origin),
                            "--key", str(keys / "publisher.key"), "--release", f"v{sequence}",
                            "--sequence", str(sequence)], stdout=subprocess.DEVNULL, check=True)
        def promote(release):
            subprocess.run([str(binary), "promote", "--root", str(origin), "--release", release], check=True)
        promote("v1")
        port = available_port(); base = f"http://127.0.0.1:{port}"
        origin_proc = subprocess.Popen([str(binary), "serve", "--root", str(origin), "--listen", f"127.0.0.1:{port}", "--rate-kbit", "10000"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        agent = None
        try:
            for _ in range(100):
                try:
                    get(base + "/stats"); break
                except OSError:
                    time.sleep(0.02)
            agent = subprocess.Popen([str(binary), "watch", "--manifest", base + "/releases/desired.json", "--base", base,
                                      "--state", str(edge), "--pub", str(keys / "publisher.pub"), "--allow-http",
                                      "--poll", "100ms"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            for release in ("v1", "v2"):
                if release == "v2":
                    promote(release)
                limit = time.monotonic() + 15
                while time.monotonic() < limit:
                    if agent.poll() is not None:
                        raise RuntimeError("watch exited")
                    try:
                        state = json.loads((edge / "state.json").read_text())
                        if state["release"] == release and state["phase"] == "staged":
                            break
                    except FileNotFoundError:
                        pass
                    time.sleep(0.02)
                else:
                    raise RuntimeError(f"watch failed to converge to {release}")
            print(json.dumps({"watch_promotion": "PASS", "same_agent_pid": agent.pid,
                              "observed_staged_releases": ["v1", "v2"]}))
        finally:
            for process in (agent, origin_proc):
                if process is not None and process.poll() is None:
                    process.terminate(); process.wait(timeout=5)


if __name__ == "__main__":
    main()

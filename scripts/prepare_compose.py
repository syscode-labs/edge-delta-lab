#!/usr/bin/env python3
"""Prepare the Compose simulation; no Docker socket is mounted into its services."""
import json
from pathlib import Path
import subprocess
from demo import ROOT, dump

work = ROOT / "work" / "compose"
if work.exists():
    raise SystemExit(f"Refusing to overwrite {work}")
work.mkdir(parents=True)
binary = ROOT / "bin" / "edgelab"
subprocess.run([str(binary), "keygen", "--out", str(work / "keys")], check=True)
info = json.loads(subprocess.check_output([str(binary), "fixtures", "--out", str(work / "fixtures"), "--size-mib", "16"]))
for seq, release in enumerate(["app-a-v1", "app-b-v1", "app-a-v2", "app-a-v3"], 1):
    subprocess.run([str(binary), "publish", "--root", str(work / "origin"), "--key", str(work / "keys" / "publisher.key"),
                    "--input", info[release]["file"], "--release", release, "--sequence", str(seq)], stdout=subprocess.DEVNULL, check=True)
subprocess.run([str(binary), "promote", "--root", str(work / "origin"), "--release", "app-a-v1"], check=True)
dump(work / "faults.json", {"rate_kbit": 5000, "latency_ms": 50, "drop_every": 7, "drop_after_bytes": 8192})
print(f"Prepared {work}")

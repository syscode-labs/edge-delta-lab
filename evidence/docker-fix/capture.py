#!/usr/bin/env python3
"""Bounded, exclusive command evidence capture for this repair (not product code)."""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time
p = argparse.ArgumentParser()
p.add_argument('--label', required=True)
p.add_argument('--timeout', type=int, default=600)
p.add_argument('command', nargs=argparse.REMAINDER)
a = p.parse_args()
evidence = Path(__file__).resolve().parent
repo = evidence.parent.parent
cmd = a.command[1:] if a.command[:1] == ['--'] else a.command
paths = {name: evidence / (a.label + suffix) for name, suffix in [('stdout', '.stdout.txt'), ('stderr', '.stderr.txt'), ('command', '.command.json')]}
assert cmd and not any(path.exists() for path in paths.values())
env = dict(os.environ)
# Use the caller's explicitly selected toolchain and Docker context.
env.setdefault('GOTOOLCHAIN', 'local')
for key in list(env):
    if key.startswith('GIT_'): env.pop(key)
start = time.monotonic()
record = dict(command=cmd, cwd=str(repo), started_utc=datetime.datetime.now(datetime.timezone.utc).isoformat(), environment={k: env[k] for k in ('PATH', 'GOROOT', 'GOTOOLCHAIN', 'DOCKER_CONTEXT') if k in env}, git_commit=subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=repo, text=True).strip(), source_sha256={str(f.relative_to(repo)): hashlib.sha256(f.read_bytes()).hexdigest() for pattern in ('scripts/*.py', 'internal/lab/*.go', 'cmd/edgelab/*.go', 'Makefile', 'go.mod') for f in sorted(repo.glob(pattern))})
paths['command'].write_text(json.dumps(record, indent=2) + '\n')
with paths['stdout'].open('xb') as out, paths['stderr'].open('xb') as err:
    proc = subprocess.Popen(cmd, cwd=repo, env=env, stdout=out, stderr=err, start_new_session=True)
    try: rc = proc.wait(timeout=a.timeout)
    except subprocess.TimeoutExpired:
        os.killpg(proc.pid, signal.SIGTERM)
        try: proc.wait(timeout=10)
        except subprocess.TimeoutExpired: os.killpg(proc.pid, signal.SIGKILL); proc.wait()
        rc = 124
record.update(exit_code=rc, elapsed_seconds=time.monotonic()-start, finished_utc=datetime.datetime.now(datetime.timezone.utc).isoformat(), outputs={k: str(v) for k,v in paths.items()})
paths['command'].write_text(json.dumps(record, indent=2) + '\n')
print(json.dumps({k:v for k,v in record.items() if k != 'source_sha256'}, indent=2))
for key in ('stdout', 'stderr'):
    print('\n== ' + key + ' ==\n' + paths[key].read_text(errors='replace')[-12000:])
sys.exit(rc)

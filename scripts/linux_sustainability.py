#!/usr/bin/env python3
"""Linux-only isolated daemon measurement; run inside an owned NET_ADMIN container.
No Docker socket, host networking, credentials, or persistent service installation.
Only this container's loopback qdisc is modified. Results contain no signing keys.
"""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import platform
import socket
import subprocess
import tempfile
import time
import urllib.request

parser = argparse.ArgumentParser()
parser.add_argument('--binary', default='/tmp/edgelab')
parser.add_argument('--output', default='/tmp/results')
parser.add_argument('--stage', choices=['idle', 'link', 'soak', 'all'], default='all')
a = parser.parse_args()
assert platform.system() == 'Linux', 'Measurements require Linux daemon processes'
B = str(Path(a.binary).resolve())
OUT = Path(a.output); OUT.mkdir(parents=True, exist_ok=True)
W = Path(tempfile.mkdtemp(prefix='edgelab-v31-'))
procs = []

def save(name, value):
    (OUT / (name + '.json')).write_text(json.dumps(value, indent=2) + '\n')

def run(*args):
    p = subprocess.run([B, *map(str, args)], capture_output=True, text=True, check=True)
    return json.loads(p.stdout) if p.stdout.strip() else None

def start(name, *args):
    log = (OUT / (name + '.log')).open('w')
    p = subprocess.Popen([B, *map(str, args)], stdout=log, stderr=log, start_new_session=True)
    log.close(); procs.append(p)
    return p

def stop():
    for p in procs:
        if p.poll() is None: p.terminate()
    for p in procs:
        try: p.wait(timeout=10)
        except subprocess.TimeoutExpired: p.kill(); p.wait()
    procs.clear()

def wait(fn, timeout=300):
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        if any(p.poll() is not None for p in procs):
            raise RuntimeError('daemon exited; inspect logs')
        try:
            v = fn()
            if v: return v
        except (OSError, ValueError): pass
        time.sleep(.2)
    raise TimeoutError('measurement condition did not converge')

def stats():
    with urllib.request.urlopen('http://127.0.0.1:18108/stats', timeout=10) as r:
        return json.load(r)

def admin():
    with socket.socket(socket.AF_UNIX) as s:
        s.settimeout(5); s.connect(str(W / 'admin.sock'))
        s.sendall(b'{"id":1,"method":"stats","params":{}}\n')
        data = b''
        while not data.endswith(b'\n'): data += s.recv(65536)
        response = json.loads(data)
        assert response['ok'], response
        return response['result']

def summary(dev, release):
    path = W / dev / 'summary.json'
    if not path.exists(): return None
    d = json.loads(path.read_text())
    return d if d['release'] == release else None

def promote(release): run('promote', '--root', W / 'origin', '--release', release, '--channel', 'desired')

def faults(value):
    p = W / 'faults.tmp'; p.write_text(json.dumps(value)); p.replace(W / 'faults.json')

def hub(name, plan=None):
    faults(plan or {})
    return start(name, 'serve', '--root', W / 'origin', '--listen', '127.0.0.1:18108',
                 '--hub', '--events', '--rate-kbit', '0', '--faults', W / 'faults.json',
                 '--admin-socket', W / 'admin.sock', '--event-log', OUT / (name + '-events.jsonl'))

def client(dev, push=False, poll='1s'):
    args = ['watch', '--manifest', 'http://127.0.0.1:18108/releases/desired.json',
            '--base', 'http://127.0.0.1:18108', '--state', W / dev, '--pub', W / 'keys/publisher.pub',
            '--device-id', dev, '--allow-http', '--poll', poll,
            '--workers', '2', '--attempts', '12', '--backoff', '200ms', '--max-backoff', '5s',
            '--request-timeout', '10s']
    if push: args += ['--events-url', 'ws://127.0.0.1:18108/events']
    return start(dev, *args)

def delta(before, after):
    return {k: after[k] - v for k, v in before.items() if isinstance(v, (int, float))}

def tc(*args):
    return subprocess.run(['tc', *args], capture_output=True, text=True, check=True).stdout

save('environment', {'kernel': platform.release(), 'os': platform.system(), 'architecture': platform.machine(),
                     'binary_sha256': hashlib.sha256(Path(B).read_bytes()).hexdigest(),
                     'scope': 'local Linux VM/container (OrbStack); not standalone Linux host',
                     'started_utc': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())})
try:
    run('keygen', '--out', W / 'keys')
    fixtures = run('fixtures', '--out', W / 'fixtures', '--size-mib', '16', '--patch-kib', '32')
    releases = ['app-a-v1', 'app-b-v1', 'app-a-v2']
    for seq, release in enumerate(releases, 1):
        run('publish', '--root', W / 'origin', '--key', W / 'keys/publisher.key',
            '--input', fixtures[release]['file'], '--release', release, '--sequence', seq,
            '--kind', 'docker-archive', '--image-ids', fixtures[release]['image_id'])
    if a.stage in ('idle', 'all'):
        promote(releases[0]); hub('idle-hub'); wait(stats)
        devices = ['push-1', 'push-2', 'push-3', 'poll-1']
        for dev in devices: client(dev, push=dev.startswith('push'), poll='1h' if dev.startswith('push') else '30s')
        for dev in devices: wait(lambda: summary(dev, releases[0]))
        before = admin(); pids = [p.pid for p in procs]; t = time.monotonic()
        print('idle window started: 600 seconds', flush=True)
        while time.monotonic() - t < 600:
            assert all(p.poll() is None for p in procs)
            time.sleep(1)
        after = admin(); elapsed = time.monotonic() - t
        assert all(p.poll() is None for p in procs), 'idle daemon exited at window boundary'
        d0 = {x['device']: x for x in before['hub']['devices']}
        d1 = {x['device']: x for x in after['hub']['devices']}
        assert set(devices) <= d0.keys() and set(devices) <= d1.keys(), 'missing device accounting'
        rows = {dev: {k: d1[dev][k] - d0[dev][k] for k in ('requests', 'chunk_requests', 'meta_requests', 'bytes_served')} for dev in devices}
        save('idle', {'window_seconds': elapsed, 'devices': rows, 'before': before, 'after': after,
                      'daemon_pids': pids, 'all_daemons_alive_entire_window': True,
                      'gate_pass': all(0 <= x['bytes_served'] <= 2048 and x['chunk_requests'] == 0
                                       and all(v >= 0 for v in x.values()) for x in rows.values())
                          and 19 <= rows['poll-1']['requests'] <= 21
                          and all(rows[dev]['requests'] == 0 for dev in devices if dev.startswith('push')),
                      'excludes': 'HTTP headers, TCP/IP, websocket keepalives and VPN traffic'})
        stop(); print('idle complete', flush=True)
    if a.stage in ('link', 'all'):
        tc('qdisc', 'add', 'dev', 'lo', 'root', 'netem', 'rate', '5mbit', 'delay', '100ms')
        promote(releases[0]); hub('link-hub'); wait(stats)
        before = stats(); t = time.monotonic(); client('link-client'); rows = []
        for i, release in enumerate(releases):
            if i: before = stats(); t = time.monotonic(); promote(release)
            s = wait(lambda: summary('link-client', release))
            rows.append({'release': release, 'elapsed_seconds': time.monotonic() - t,
                         'server_delta': delta(before, stats()), 'summary': s})
        before = stats(); time.sleep(5); after = stats()
        save('link', {'rows': rows, 'unchanged_5s': delta(before, after),
                      'qdisc': tc('-s', 'qdisc', 'show', 'dev', 'lo'),
                      'delay_scope': '100ms on container loopback egress, both directions; approximate RTT 200ms',
                      'all_daemons_alive': all(p.poll() is None for p in procs)})
        stop(); tc('qdisc', 'del', 'dev', 'lo', 'root'); print('link complete', flush=True)
    if a.stage in ('soak', 'all'):
        manifest = json.loads(base64.b64decode(json.loads((W / 'origin/releases/app-a-v1.json').read_text())['payload']))
        chunks = {c['sha256']: c for c in manifest['chunks']}
        save('soak-chunk-map', chunks)
        tc('qdisc', 'add', 'dev', 'lo', 'root', 'netem', 'rate', '5mbit', 'delay', '100ms')
        promote(releases[0]); hub('soak-hub', {'rate_kbit': 1000}); wait(stats)
        t = time.monotonic(); client('soak-client', poll='2s'); outages = []
        for cycle in range(2):
            def committed():
                files = [p for p in (W / 'soak-client/cache').glob('*/*') if p.is_file() and p.name in chunks]
                return files if len(files) >= 3 + cycle * 3 else None
            files = wait(committed)
            assert not summary('soak-client', releases[0]), 'outage must interrupt an incomplete transfer'
            preserved = []
            for p in files:
                content = p.read_bytes()
                assert hashlib.sha256(content).hexdigest() == p.name and len(content) == chunks[p.name]['size']
                encoded = chunks[p.name]['encoded_sha256']
                preserved.append('chunks/' + encoded[:2] + '/' + encoded + '.gz')
            before = stats(); started = time.monotonic()
            assert preserved and all(before['object_requests'].get(p, 0) >= 1 for p in preserved)
            faults({'rate_kbit': 1000, 'offline': True}); time.sleep(30)
            assert all(p.poll() is None for p in procs)
            faults({'rate_kbit': 1000})
            after_outage = stats()
            assert after_outage['injected_503s'] > before['injected_503s'], 'outage not exercised'
            outages.append({'duration_seconds': time.monotonic() - started, 'verified_committed_paths': preserved,
                            'before_object_requests': before['object_requests'], 'before_503s': before['injected_503s'],
                            'after_outage_stats': after_outage})
            time.sleep(5)
        s = wait(lambda: summary('soak-client', releases[0]), timeout=300); after = stats()
        for row in outages:
            row['redownloaded_committed'] = [key for key in row['verified_committed_paths']
                if row['before_object_requests'][key] != after['object_requests'].get(key, 0)]
        events = []
        for line in (OUT / 'soak-client.log').read_text().splitlines():
            try: events.append(json.loads(line))
            except ValueError: pass
        save('soak', {'elapsed_seconds': time.monotonic() - t, 'summary': s, 'outages': outages,
                      'after': after, 'all_daemons_alive': all(p.poll() is None for p in procs),
                      'retry_policy': {'attempts_per_object':12, 'backoff':'200ms', 'max_backoff':'5s', 'poll':'2s'},
                      'event_types': sorted({x.get('event', '') for x in events}),
                      'qdisc': tc('-s', 'qdisc', 'show', 'dev', 'lo'),
                      'retry_observations': [x for x in events if x.get('event') == 'retry'],
                      'gate_pass': not any(row['redownloaded_committed'] for row in outages) and s['integrity_failures'] == 0
                          and all(p.poll() is None for p in procs)
                          and any(x.get('event') == 'retry' for x in events)
                          and all(x.get('attempt', 0) < 12 and x.get('delay_ms', 0) <= 5000
                                  for x in events if x.get('event') == 'retry')})
        stop(); tc('qdisc', 'del', 'dev', 'lo', 'root'); print('soak complete', flush=True)
finally:
    stop()
    subprocess.run(['tc', 'qdisc', 'del', 'dev', 'lo', 'root'], capture_output=True)
    import shutil
    shutil.rmtree(W)
    save('cleanup', {'daemon_children_reaped': True, 'temporary_workdir_removed': not W.exists(),
                     'qdisc': tc('qdisc', 'show', 'dev', 'lo')})

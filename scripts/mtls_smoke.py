#!/usr/bin/env python3
"""Opt-in real Caddy TLS boundary proof, only owned disposable Docker resources.
No systemd installation, image publication or real registry credentials involved.
"""
import argparse
import json
import os
from pathlib import Path
import socket
import subprocess
import time
import uuid
import mtls

ROOT = Path(__file__).resolve().parents[1]


def run(*args, check=True):
    return subprocess.run(list(map(str, args)), check=check, text=True, capture_output=True)


def port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--work', required=True, type=Path)
    args = parser.parse_args()
    work = args.work.resolve()
    work.mkdir(parents=True, exist_ok=False, mode=0o700)
    os.umask(0o077)
    # Inspection before any Docker mutation is mandatory.
    before = {what: run('docker', *cmd).stdout for what, cmd in {
        'context': ['context', 'show'], 'containers': ['ps', '-a'],
        'networks': ['network', 'ls'], 'volumes': ['volume', 'ls']}.items()}
    (work / 'preflight.json').write_text(json.dumps(before, indent=2))
    token = 'edmtls-' + uuid.uuid4().hex[:10]
    backend, replacement = token + '-backend', token + '-replacement'
    hp, tp = port(), port()
    while hp == tp:
        tp = port()
    directory = work / 'mtls'
    env = work / 'hub.env'
    env.write_text(f'MTLS_DIRECTORY={directory}\nMTLS_HOST=localhost\nMTLS_PORT={tp}\nHUB_PORT={hp}\n')
    def make(action, *extra):
        return run('make', action, 'HUB_ENV=' + str(env), *extra)
    def request(tls=True, identity='edge-01'):
        # Probe inside the Linux host network: does not infer Desktop port forwarding.
        script = '''import ssl,urllib.request
ctx=ssl.create_default_context(cafile='/clients/edge-01/hub-ca.pem')
IDENTITY
with urllib.request.urlopen('URL', context=ctx, timeout=3) as r:
 print(r.status)
'''.replace('URL', f"{'https' if tls else 'http'}://localhost:{tp if tls else hp}/healthz")
        script = script.replace('IDENTITY', f"ctx.load_cert_chain('/clients/{identity}/client.pem','/clients/{identity}/client.key')" if identity else '')
        return run('docker', 'exec', backend, 'python', '-c', script, check=False)
    def wait_ready():
        for _ in range(40):
            result = request()
            if result.returncode == 0 and result.stdout.strip() == '200':
                return
            time.sleep(0.25)
        raise RuntimeError('allowed client failed: ' + result.stderr)
    results = {}
    try:
        make('mtls-init')
        make('mtls-client', 'CLIENT=edge-01')
        # A valid client certificate signed by a different CA must be rejected.
        rogue = work / 'rogue'
        rogue.mkdir(mode=0o700)
        mtls.ca(rogue, 'rogue-ca')
        out = directory / 'clients/rogue'
        out.mkdir(mode=0o700)
        mtls.certificate(out, 'client', rogue / 'rogue-ca', 'rogue',
                         'basicConstraints=critical,CA:FALSE\nextendedKeyUsage=clientAuth')
        run('docker', 'run', '-d', '--name', backend, '--network', 'host',
            '-v', str(directory / 'clients') + ':/clients:ro', 'python:3.12-alpine',
            'python', '-c', 'from http.server import HTTPServer,BaseHTTPRequestHandler\n'
            'class H(BaseHTTPRequestHandler):\n def do_GET(self):\n  self.send_response(200);self.end_headers();self.wfile.write(b"owned-backend")\n'
            f'HTTPServer(("127.0.0.1",{hp}),H).serve_forever()')
        make('mtls-up')
        wait_ready()
        results['allowed_client'] = True
        assert request(identity='').returncode != 0
        results['missing_client_denied'] = True
        assert request(identity='rogue').returncode != 0
        results['wrong_ca_client_denied'] = True
        compose = ['docker', 'compose', '-f', str(directory / 'compose.yaml')]
        run(*compose, 'restart')
        wait_ready()
        results['restart_preserves_access'] = True
        make('mtls-down')
        assert request().returncode != 0
        assert request(tls=False, identity='').returncode == 0
        results['removal_closes_tls_preserves_backend'] = True
        # A standalone replacement proxy, not a member of the wrapper project.
        run('docker', 'run', '-d', '--name', replacement, '--network', 'host',
            '-v', str(directory / 'runtime') + ':/etc/caddy:ro', mtls.IMAGE)
        wait_ready()
        assert request(identity='').returncode != 0
        results['independent_replacement_verified'] = True
        run('docker', 'rm', '-f', replacement)
        make('mtls-up')
        wait_ready()
        results['wrapper_recreated_same_trust'] = True
        results['image_identity'] = json.loads(run('docker', 'image', 'inspect', mtls.IMAGE, '--format', '{{json .RepoDigests}}').stdout)
        results['status'] = 'PASS'
    finally:
        down = run('make', 'mtls-down', 'HUB_ENV=' + str(env), check=False)
        results['proxy_cleanup_exit_code'] = down.returncode
        for name in (backend, replacement):
            run('docker', 'rm', '-f', name, check=False)
        results['remaining_owned_containers'] = run('docker', 'ps', '-a', '--filter', 'name=' + token, '--format', '{{.Names}}').stdout.strip()
        (work / 'result.json').write_text(json.dumps(results, indent=2) + '\n')
    print(json.dumps(results, indent=2))


if __name__ == '__main__':
    main()

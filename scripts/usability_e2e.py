#!/usr/bin/env python3
"""Real packaged Linux/amd64 Make quickstart proof on TWO fresh OrbStack guests.

Read-only discovery (no bundle needed):
  python3 scripts/usability_e2e.py discover --work work/usability-discovery
Execute local release bytes, explicitly allowing disposable guest creation:
  python3 scripts/usability_e2e.py run --bundle dist/edgelab-v0.1.1-linux-amd64.tar.gz \
    --work work/usability-local --create-guests
Execute hosted bytes: anonymously curl the archive + SHA256SUMS, verify checksums,
then supply that DOWNLOADED archive as --bundle and --source-url <public URL>.
--arm64-bundle inspects ELF/archive identity ONLY; never reports arm64 runtime PASS.

Requires macOS orb, Python 3; guest provisioning requires Ubuntu apt network access.
No source build, source checkout, local Docker socket or user daemon is used by run.
Guests use classic vfs Docker (nested LXC compatibility); this is NOT two physical
hosts, independent kernels, WAN evidence or production systemd-hardening evidence.
Only newly created ed-use-* machines can be mutated/deleted. Failure preserves the
machines for debugging by default; --delete-guests deletes owned machines after
retaining nonsecret evidence. Private material remains inside the publisher guest,
except a receiver-specific TLS client key, which is transferred in memory. The
private signing key, registry password and CA keys are NEVER copied to receiver.

The probe fails closed at the first missing gate. result.json distinguishes PASS
from FAILED/BLOCKED; discovery and unit tests alone never constitute runtime proof.
"""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import re
import shlex
import struct
import subprocess
import sys
import tarfile
import time
import uuid


def digest(data):
    return hashlib.sha256(data).hexdigest()


def archive_info(path, architecture):
    data = Path(path).read_bytes()
    with tarfile.open(fileobj=io.BytesIO(data), mode='r:gz') as tf:
        members = tf.getmembers()
        for m in members:
            p = Path(m.name)
            if p.is_absolute() or '..' in p.parts or not (m.isfile() or m.isdir()):
                raise ValueError('unsafe archive member: ' + m.name)
        files = {m.name.removeprefix('./'): m for m in members if m.isfile()}
        for required in ('edgelab', 'Makefile', 'hub.env.example', 'receiver.env.example',
                         'scripts/lifecycle.py', 'scripts/install.py', 'scripts/mtls.py'):
            if required not in files:
                raise ValueError('missing packaged quickstart member: ' + required)
        binary = tf.extractfile(files['edgelab']).read()
        machine = struct.unpack('<H', binary[18:20])[0] if len(binary) >= 20 else 0
        if binary[:6] != b'\x7fELF\x02\x01' or machine != {'amd64': 62, 'arm64': 183}[architecture]:
            raise ValueError('bundle binary architecture does not match ' + architecture)
        if not files['edgelab'].mode & 0o111:
            raise ValueError('packaged binary is not executable')
    return {'archive_sha256': digest(data), 'binary_sha256': digest(binary),
            'architecture': architecture, 'runtime_executed': False, 'members': sorted(files)}


def summaries(text):
    """Decode multiline summaries interleaved with Docker output and JSON events."""
    found = []
    for match in re.finditer(r'^\{', text, re.M):
        try:
            obj, _ = json.JSONDecoder().raw_decode(text[match.start():])
        except ValueError:
            continue
        if isinstance(obj, dict) and obj.get('phase') == 'loaded' and 'downloaded_chunks' in obj:
            found.append(obj)
    return found


class Harness:
    def __init__(self, args):
        self.args = args
        self.work = args.work.resolve()
        self.work.mkdir(parents=True, exist_ok=False, mode=0o700)
        self.owned = []
        self.token = 'ed-use-' + uuid.uuid4().hex[:10]
        self.results = {'status': 'NOT_RUN', 'gates': {}, 'owned_guests': self.owned,
                        'limitations': ['OrbStack LXC guests may share a kernel',
                                        'classic vfs Docker store', 'not WAN or physical-host evidence',
                                        'publisher-to-hub mTLS not a packaged Make option; not exercised']}
        self.counter = 0

    def save(self, name, value):
        (self.work / name).write_text(json.dumps(value, indent=2) + '\n')

    def command(self, argv, *, data=None, check=True, evidence=None, timeout=300):
        p = subprocess.run(list(map(str, argv)), input=data, capture_output=True, timeout=timeout)
        # Only explicitly safe discovery/probe output is retained. No commands,
        # stdin, config dumps, environment, raw Docker inspect, or secret material.
        self.counter += 1
        item = {'step': self.counter, 'probe': evidence or 'private-operation', 'exit_code': p.returncode}
        if evidence:
            item.update(stdout=p.stdout.decode(errors='replace'), stderr=p.stderr.decode(errors='replace'))
        self.save(f'{self.counter:03d}.json', item)
        if check and p.returncode:
            raise RuntimeError(f'step {self.counter} ({evidence or "private-operation"}) failed, exit {p.returncode}; inspect guest locally (sensitive output withheld)')
        return p

    def remote(self, host, script, *, root=False, **kw):
        if host not in self.owned:
            raise ValueError('refusing command on unowned guest')
        cmd = ['orb', '-m', host]
        if root:
            cmd += ['-u', 'root']
        return self.command(cmd + ['bash', '-euc', 'cd "$HOME"; ' + script], **kw)

    def put(self, host, path, data, *, root=False):
        code = 'import pathlib,sys; p=pathlib.Path(sys.argv[1]); p.parent.mkdir(parents=True,exist_ok=True); p.write_bytes(sys.stdin.buffer.read()); p.chmod(0o600)'
        self.remote(host, 'python3 -c ' + shlex.quote(code) + ' ' + shlex.quote(path),
                    data=data if isinstance(data, bytes) else data.encode(), root=root)

    def read(self, host, path, *, root=False):
        return self.remote(host, 'python3 -c ' + shlex.quote('import pathlib,sys; sys.stdout.buffer.write(pathlib.Path(sys.argv[1]).read_bytes())') + ' ' + shlex.quote(path), root=root).stdout

    def gate(self, name, details=True):
        self.results['gates'][name] = details
        self.save('result.json', self.results)
        print('PASS ' + name, flush=True)

    def discover(self):
        self.command(['orb', 'list', '--format', 'json'], evidence='existing-orbstack-machines')
        self.command(['docker', 'context', 'ls'], evidence='host-docker-contexts', check=False)
        self.command(['docker', 'info', '--format', '{{.ID}} {{.Name}} {{.Architecture}} {{.Driver}} {{.ServerVersion}}'],
                     evidence='host-daemon-not-used-by-test', check=False)
        self.command(['orb', 'create', '--help'], evidence='guest-creation-capabilities')
        self.results['status'] = 'DISCOVERY_ONLY'
        self.save('result.json', self.results)

    def create(self, role):
        host = self.token + '-' + role
        existing = json.loads(self.command(['orb', 'list', '--format', 'json']).stdout)
        if host in json.dumps(existing):
            raise ValueError('guest name already exists')
        self.command(['orb', 'create', '-a', 'amd64', 'ubuntu:24.04', host], timeout=600)
        self.owned.append(host)
        self.save('result.json', self.results)
        self.remote(host, 'uname -a; id; systemd-detect-virt; ip -j -4 address; '
                    'systemctl show --property=Version --value; test ! -e /etc/edgelab; '
                    'test ! -e /usr/local/bin/edgelab; test ! -e /etc/docker/daemon.json',
                    evidence=role + '-fresh-guest-before-mutation')
        self.remote(host, 'export DEBIAN_FRONTEND=noninteractive; apt-get update -qq; '
                    'apt-get install -y -qq docker.io docker-compose-v2 python3 make openssl curl apache2-utils',
                    root=True, timeout=900)
        user = self.remote(host, 'id -un').stdout.decode().strip()
        home = self.remote(host, 'printf %s "$HOME"').stdout.decode().strip()
        if not home.startswith('/home/'):
            raise ValueError('expected an unprivileged Linux operator')
        # New guest only. Never reconfigure the user's existing Docker daemon.
        self.put(host, '/etc/docker/daemon.json', json.dumps({'storage-driver': 'vfs',
                 'features': {'containerd-snapshotter': False}}), root=True)
        self.remote(host, f'usermod -aG docker {shlex.quote(user)}; systemctl restart docker', root=True)
        self.remote(host, 'docker info --format "{{.ID}} {{.Architecture}} {{.Driver}}"; '
                    'docker compose version; systemctl --version; systemctl cat docker', evidence=role + '-provisioned')
        return host, home

    def unpack(self, host, home):
        self.put(host, home + '/bundle.tar.gz', self.args.bundle.read_bytes())
        actual = self.remote(host, 'sha256sum bundle.tar.gz').stdout.decode().split()[0]
        if actual != self.results['bundle']['archive_sha256']:
            raise ValueError('transferred archive hash mismatch')
        self.remote(host, 'mkdir bundle; tar xzf bundle.tar.gz -C bundle; cd bundle; '
                    'sha256sum edgelab; ./edgelab watch --help', evidence=host + '-packaged-binary')

    def make(self, role, action):
        host = self.hub if role in ('hub', 'mtls') else self.receiver
        sudo = 'sudo ' if role == 'receiver' else ''
        return self.remote(host, f'cd bundle; {sudo}make {role}-{action}', evidence=f'{role}-{action}', timeout=600)

    def edit_env(self, host, role, values):
        # Exercise exact cp command then edit DATA, without shell evaluation.
        self.remote(host, f'cd bundle; cp {role}.env.example {role}.env', evidence='cp-' + role + '-env')
        path = (self.hh if role == 'hub' else self.rh) + '/bundle/' + role + '.env'
        source = self.read(host, path).decode()
        for key, val in values.items():
            pattern = r'(?m)^' + re.escape(key) + '=.*$'
            replacement = key + '=' + str(val)
            source = re.sub(pattern, lambda m: replacement, source) if re.search(pattern, source) else source + '\n' + replacement + '\n'
        self.put(host, path, source)

    def wait(self, label, test, timeout=300):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            value = test()
            if value:
                return value
            time.sleep(2)
        raise RuntimeError('timed out: ' + label)

    def journal(self, since=None):
        arg = ' --since ' + shlex.quote(since) if since else ''
        text = self.remote(self.receiver, 'sudo journalctl -u edgelab-receiver -o cat --no-pager' + arg).stdout.decode()
        self.save('receiver-journal' + ('-restart' if since else '') + '.json', {'text': text})
        return text

    def first_summary(self, sequence=None, after=0, since=None):
        def probe():
            rows = summaries(self.journal(since))
            return next((s for s in rows if (s['sequence'] == sequence if sequence else s['sequence'] > after)), None)
        row = self.wait('loaded summary', probe, self.args.timeout)
        self.save('summary-' + str(row['sequence']) + ('-restart' if since else '') + '.json', row)
        return row

    def push(self, version):
        # Shared random lower layer gives real cross-release chunk reuse; upper
        # payload differs in bytes, size and mtime. Never mutate/re-push old tags.
        self.put(self.hub, self.hh + '/fixture/version', f'edge-delta-version-{version}\n')
        self.put(self.hub, self.hh + '/fixture/Dockerfile',
                 'FROM busybox:1.37.0\nCOPY shared /shared\nCOPY version /version\nCMD ["cat", "/version"]\n')
        self.remote(self.hub, f'DOCKER_BUILDKIT=0 docker build --no-cache -t {self.ip}:5000/proof/app:v{version} fixture; '
                    f'docker push {self.ip}:5000/proof/app:v{version}', timeout=600)

    def tls_probe(self, identity=True, wrong=False):
        client = 'rogue' if wrong else 'enrollment'
        flags = f' --cert {client}/client.pem --key {client}/client.key' if identity else ''
        return self.remote(self.receiver, f'curl -fsS --max-time 5 --cacert enrollment/hub-ca.pem{flags} https://{self.ip}:8443/healthz', check=False)

    def prove_image(self, summary, version):
        # Signed metadata is stored alongside artifact; locate it by sequence,
        # not a guessed release name. Save config bytes AFTER the receiver load.
        manifest = json.loads(self.read(self.hub, self.hh + '/hub/origin/releases/desired.json'))
        payload = manifest.get('manifest', manifest)
        # The signed envelope can wrap canonical JSON as base64.
        if 'payload' in manifest:
            import base64
            raw = manifest['payload']
            payload = json.loads(base64.b64decode(raw)) if isinstance(raw, str) else raw
        self.save(f'version-{version}-signed-envelope.json', manifest)
        if payload.get('sequence') != summary['sequence']:
            raise ValueError('signed current manifest sequence does not match loaded release')
        ids = payload.get('image_ids', [])
        if len(ids) != 1 or not re.fullmatch(r'sha256:[0-9a-f]{64}', ids[0]):
            raise ValueError('expected one signed config image ID')
        image = ids[0]
        self.remote(self.receiver, 'docker image save ' + image + ' -o loaded.tar')
        code = '''import tarfile,json,hashlib
with tarfile.open('loaded.tar') as t:
 m=json.load(t.extractfile('manifest.json'))
 assert len(m)==1
 c=t.extractfile(m[0]['Config']).read()
 print('sha256:'+hashlib.sha256(c).hexdigest())
'''
        actual = self.remote(self.receiver, 'python3 -c ' + shlex.quote(code)).stdout.decode().strip()
        if actual != image:
            raise ValueError('receiver saved config bytes differ from signed ID')
        output = self.remote(self.receiver, 'docker run --rm --pull never --network none ' + image).stdout.decode()
        if output != f'edge-delta-version-{version}\n':
            raise ValueError('offline payload mismatch')
        self.gate(f'version-{version}-signed-config-and-offline-run', {'image_id': image, 'payload': output.strip()})
        return image

    def run(self):
        if not self.args.create_guests:
            raise ValueError('run requires --create-guests; existing machines are never reused')
        self.discover()
        self.results['status'] = 'RUNNING'
        self.results['bundle'] = archive_info(self.args.bundle, 'amd64')
        self.results['source_url'] = self.args.source_url
        if self.args.arm64_bundle:
            self.results['arm64_inspection_only'] = archive_info(self.args.arm64_bundle, 'arm64')
        self.hub, self.hh = self.create('hub')
        self.receiver, self.rh = self.create('receiver')
        ids = [self.remote(h, "docker info --format '{{.ID}}'").stdout.decode().strip() for h in (self.hub, self.receiver)]
        if not all(ids) or ids[0] == ids[1]:
            raise ValueError('not two independent Docker stores')
        self.gate('two-daemons', ids)
        self.ip = self.remote(self.hub, "ip -j -4 route get 1.1.1.1 | python3 -c 'import json,sys; print(json.load(sys.stdin)[0][\"prefsrc\"])'").stdout.decode().strip()
        import ipaddress
        ipaddress.ip_address(self.ip)
        # Replace broad bootstrap insecure ranges with this sole owned registry.
        for host in (self.hub, self.receiver):
            self.put(host, '/etc/docker/daemon.json', json.dumps({'storage-driver': 'vfs',
                     'features': {'containerd-snapshotter': False}, 'insecure-registries': [self.ip + ':5000']}), root=True)
            self.remote(host, 'systemctl restart docker', root=True)
        for host, home in ((self.hub, self.hh), (self.receiver, self.rh)):
            self.unpack(host, home)
        self.results['bundle']['runtime_executed'] = True
        self.gate('packaged-amd64-executed-on-both')
        # Registry password is generated and consumed only in the publisher guest.
        code = "from pathlib import Path; import secrets,os; os.umask(0o077); Path('registry').mkdir(); Path('registry/password').write_text(secrets.token_hex(24)); Path('fixture').mkdir(); Path('fixture/shared').write_bytes(os.urandom(8*1024*1024))"
        self.remote(self.hub, 'python3 -c ' + shlex.quote(code))
        self.remote(self.hub, 'htpasswd -Bni publisher < registry/password > registry/htpasswd; '
                    'docker run -d --name fixture-registry --network host '
                    '-v "$HOME/registry:/auth:ro" -e REGISTRY_AUTH=htpasswd '
                    '-e REGISTRY_AUTH_HTPASSWD_REALM=proof -e REGISTRY_AUTH_HTPASSWD_PATH=/auth/htpasswd registry:2')
        def registry_ready():
            p = self.remote(self.hub, f'curl -s -o /dev/null -w "%{{http_code}}" http://{self.ip}:5000/v2/', check=False)
            return p.returncode == 0 and p.stdout == b'401'
        self.wait('registry authentication denial', registry_ready)
        self.remote(self.hub, f'docker login {self.ip}:5000 -u publisher --password-stdin < registry/password')
        self.gate('registry-v2-anonymous-denied')
        self.push(1)
        self.edit_env(self.hub, 'hub', {'HUB_DIRECTORY': self.hh + '/hub', 'REGISTRY': 'http://' + self.ip + ':5000',
                     'REGISTRY_ALLOW_HTTP': 'true', 'REPOSITORY': 'proof/app', 'ALLOW': '^v[0-9]+$',
                     'REGISTRY_USERNAME': 'publisher', 'REGISTRY_PASSWORD_FILE': self.hh + '/registry/password',
                     'MTLS_DIRECTORY': self.hh + '/mtls', 'MTLS_HOST': self.ip})
        self.make('hub', 'up')
        self.make('hub', 'status')
        mounts = {}
        for role in ('publisher', 'hub'):
            container = self.remote(self.hub, 'docker ps -q --filter label=com.docker.compose.service=' + role).stdout.decode().strip()
            if not re.fullmatch(r'[0-9a-f]+', container):
                raise ValueError('expected exactly one ' + role + ' container')
            mounts[role] = json.loads(self.remote(self.hub, "docker inspect --format '{{json .Mounts}}' " + container).stdout)
            binary_hash = self.remote(self.hub, 'docker exec ' + container + ' sha256sum /usr/local/bin/edgelab').stdout.decode().split()[0]
            if binary_hash != self.results['bundle']['binary_sha256']:
                raise ValueError('Compose did not execute the packaged binary')
        for mount in mounts['hub']:
            if 'publisher.key' in mount['Source'] or mount['Destination'] == '/keys':
                raise ValueError('hub received signing private key')
            if mount['Destination'] == '/origin' and mount['RW']:
                raise ValueError('hub origin must be read-only')
        if not any(m['Destination'] == '/keys/publisher.key' and not m['RW'] for m in mounts['publisher']):
            raise ValueError('publisher signing key mount missing')
        self.gate('packaged-compose-bytes-and-signing-key-boundary', mounts)
        self.make('mtls', 'init')
        self.remote(self.hub, 'cd bundle; make mtls-client CLIENT=receiver', evidence='make-mtls-client')
        self.make('mtls', 'up')
        for name in ('client.pem', 'client.key', 'hub-ca.pem'):
            self.put(self.receiver, self.rh + '/enrollment/' + name,
                     self.read(self.hub, self.hh + '/mtls/clients/receiver/' + name))
        self.put(self.receiver, self.rh + '/enrollment/publisher.pub', self.read(self.hub, self.hh + '/hub/keys/publisher.pub'))
        self.wait('authorized mTLS', lambda: self.tls_probe().returncode == 0)
        if self.remote(self.receiver, f'curl -fsS --max-time 5 http://{self.ip}:8080/healthz', check=False).returncode == 0:
            raise ValueError('raw HTTP hub is remotely reachable')
        self.gate('remote-raw-http-inaccessible')
        if self.tls_probe(False).returncode == 0:
            raise ValueError('missing client certificate accepted')
        self.remote(self.receiver, 'mkdir rogue; openssl req -x509 -newkey rsa:2048 -nodes -days 1 '
                    '-subj /CN=wrong-ca -keyout rogue/client.key -out rogue/client.pem')
        if self.tls_probe(wrong=True).returncode == 0:
            raise ValueError('untrusted client certificate accepted')
        self.gate('mtls-allowed-missing-and-untrusted-client-boundaries')
        self.edit_env(self.receiver, 'receiver', {'HUB_URL': 'https://' + self.ip + ':8443', 'DEVICE_ID': 'proof-receiver',
                     'PUBLISHER_PUBLIC_KEY': self.rh + '/enrollment/publisher.pub',
                     'HUB_CA': self.rh + '/enrollment/hub-ca.pem', 'HUB_CLIENT_CERT': self.rh + '/enrollment/client.pem',
                     'HUB_CLIENT_KEY': self.rh + '/enrollment/client.key', 'DOCKER_LOAD': 'true'})
        self.make('receiver', 'up')
        self.make('receiver', 'status')
        self.remote(self.receiver, 'systemctl cat edgelab-receiver; systemctl show edgelab-receiver '
                    '-p DropInPaths -p DynamicUser -p ProtectSystem -p ProtectHome -p PrivateTmp -p NoNewPrivileges',
                    evidence='effective-receiver-systemd-including-lxc-overrides')
        one = self.first_summary()
        image_one = self.prove_image(one, 1)
        if one['downloaded_chunks'] <= 0:
            raise ValueError('cold transfer had no downloaded chunks')
        if self.remote(self.receiver, 'docker ps -q').stdout.strip():
            raise ValueError('receiver unexpectedly activated a container')
        self.gate('docker-load-does-not-activate')
        self.make('hub', 'restart')
        self.push(2)
        two = self.first_summary(after=one['sequence'])
        image_two = self.prove_image(two, 2)
        if image_one == image_two or two['reused_chunks'] <= 0 or two['downloaded_chunks'] <= 0:
            raise ValueError('distinct second version / delta reuse not demonstrated')
        self.gate('publisher-restart-then-new-tag-and-receiver-reuse', two)
        # Fresh process + journal time boundary, not a stale summary.json.
        origin_before = json.loads(self.remote(self.hub, 'curl -fsS http://127.0.0.1:8080/stats').stdout)
        since = self.remote(self.receiver, 'date -u +%Y-%m-%dT%H:%M:%S.%NZ').stdout.decode().strip()
        self.make('receiver', 'restart')
        repeat = self.first_summary(sequence=two['sequence'], since=since)
        origin_after = json.loads(self.remote(self.hub, 'curl -fsS http://127.0.0.1:8080/stats').stdout)
        if repeat['downloaded_chunks'] != 0 or origin_before['chunk_requests'] != origin_after['chunk_requests']:
            raise ValueError('receiver restart redownloaded cached chunks or origin served new chunks')
        self.gate('receiver-restart-fresh-loaded-summary-zero-downloads',
                  {'summary': repeat, 'origin_before': origin_before, 'origin_after': origin_after})
        self.make('receiver', 'stop')
        self.make('mtls', 'down')
        if self.tls_probe().returncode == 0:
            raise ValueError('removed wrapper still serves TLS')
        self.remote(self.hub, 'curl -fsS http://127.0.0.1:8080/healthz', evidence='raw-hub-survives-proxy-removal')
        self.remote(self.hub, 'docker run -d --name fixture-replacement --network host '
                    '-v "$HOME/mtls/runtime:/etc/caddy:ro" caddy:2.10.2-alpine')
        self.wait('independent replacement', lambda: self.tls_probe().returncode == 0)
        if self.tls_probe(False).returncode == 0:
            raise ValueError('replacement accepted anonymous client')
        self.gate('removable-and-independent-replacement-proxy')
        self.remote(self.hub, 'docker rm -f fixture-replacement')
        self.make('mtls', 'up')
        self.wait('wrapper recreation', lambda: self.tls_probe().returncode == 0)
        self.make('receiver', 'up')
        self.make('receiver', 'status')
        self.make('receiver', 'stop')
        self.make('hub', 'stop')
        for image, version in ((image_one, 1), (image_two, 2)):
            out = self.remote(self.receiver, f'docker run --rm --pull never --network none {image}').stdout.decode()
            if out != f'edge-delta-version-{version}\n':
                raise ValueError('offline run after hub stop failed')
        self.gate('both-versions-runnable-with-hub-stopped')
        self.make('receiver', 'uninstall')
        self.make('hub', 'uninstall')
        self.make('mtls', 'down')
        self.remote(self.receiver, 'sudo test -s /etc/edgelab/publisher.pub; '
                    'sudo test -s /var/lib/edgelab-receiver/summary.json; '
                    'test ! -e /etc/systemd/system/edgelab-receiver.service; test ! -e /usr/local/bin/edgelab', evidence='receiver-uninstall-retention')
        self.remote(self.hub, 'test -s hub/keys/publisher.key; test -s hub/watcher-state/sequence; '
                    'test -s hub/origin/releases/desired.json; test -s mtls/runtime/server.pem', evidence='hub-uninstall-retention')
        self.gate('uninstall-retains-trust-state-data')
        self.remote(self.hub, 'docker rm -f fixture-registry')
        self.results['status'] = 'PASS'
        self.save('result.json', self.results)


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('action', choices=('discover', 'run'))
    parser.add_argument('--work', required=True, type=Path)
    parser.add_argument('--bundle', type=Path)
    parser.add_argument('--arm64-bundle', type=Path)
    parser.add_argument('--source-url', default='local archive; not hosted-release proof')
    parser.add_argument('--create-guests', action='store_true')
    parser.add_argument('--delete-guests', action='store_true')
    parser.add_argument('--timeout', type=int, default=600)
    args = parser.parse_args()
    if args.action == 'run' and not args.bundle:
        parser.error('run requires --bundle')
    os.umask(0o077)
    h = Harness(args)
    try:
        h.discover() if args.action == 'discover' else h.run()
    except Exception as exc:
        h.results.update(status='FAILED', error=str(exc))
        h.save('result.json', h.results)
        print(str(exc), file=sys.stderr)
        return 1
    finally:
        errors = []
        if args.delete_guests:
            for host in h.owned:
                try:
                    h.command(['orb', 'delete', '-f', host], evidence='owned-guest-delete', timeout=120)
                except Exception as exc:
                    errors.append(str(exc))
        h.results['cleanup_errors'] = errors
        h.results['guests_retained'] = not args.delete_guests and bool(h.owned)
        h.save('result.json', h.results)
    return 0


if __name__ == '__main__':
    sys.exit(main())

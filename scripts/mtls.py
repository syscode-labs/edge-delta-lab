#!/usr/bin/env python3
"""Optional Linux host-network Caddy wrapper; independent of the hub installation."""
import argparse
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
from lifecycle import environment

ROOT = Path(__file__).resolve().parents[1]
IMAGE = 'caddy:2.10.2-alpine'


def run(*args):
    subprocess.run(list(map(str, args)), check=True, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)


def private_write(path, text):
    with path.open('x') as stream:
        stream.write(text)
    path.chmod(0o600)


def ca(directory, name):
    run('openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '365',
        '-subj', '/CN=edgelab-' + name, '-keyout', directory / (name + '.key'),
        '-out', directory / (name + '.pem'), '-addext', 'basicConstraints=critical,CA:TRUE',
        '-addext', 'keyUsage=critical,keyCertSign,cRLSign')


def certificate(directory, name, authority, cn, extensions):
    private_write(directory / (name + '.ext'), extensions + '\n')
    run('openssl', 'req', '-new', '-newkey', 'rsa:2048', '-nodes', '-subj', '/CN=' + cn,
        '-keyout', directory / (name + '.key'), '-out', directory / (name + '.csr'))
    run('openssl', 'x509', '-req', '-in', directory / (name + '.csr'),
        '-CA', str(authority) + '.pem', '-CAkey', str(authority) + '.key',
        '-set_serial', '0x' + os.urandom(16).hex(), '-days', '90',
        '-extfile', directory / (name + '.ext'), '-out', directory / (name + '.pem'))
    for suffix in ('.csr', '.ext'):
        (directory / (name + suffix)).unlink()


def initialize(directory, values):
    host = values['MTLS_HOST']
    if not re.fullmatch(r'[A-Za-z0-9.-]+', host):
        raise ValueError('MTLS_HOST must be one DNS name or IPv4 address')
    port, backend = int(values.get('MTLS_PORT', '8443')), int(values.get('HUB_PORT', '8080'))
    if not 1 <= port <= 65535 or not 1 <= backend <= 65535 or port == backend:
        raise ValueError('TLS and hub ports must be distinct valid ports')
    if directory.exists():
        raise ValueError('mTLS directory already exists; refusing to replace trust material')
    directory.mkdir(parents=True, mode=0o700)
    authority, runtime = directory / 'authority', directory / 'runtime'
    authority.mkdir(mode=0o700)
    runtime.mkdir(mode=0o700)
    ca(authority, 'server-ca')
    ca(authority, 'client-ca')
    try:
        ipaddress.ip_address(host)
        san = 'IP:' + host
    except ValueError:
        san = 'DNS:' + host
    certificate(runtime, 'server', authority / 'server-ca', host,
                'basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\nsubjectAltName=' + san)
    shutil.copyfile(authority / 'client-ca.pem', runtime / 'client-ca.pem')
    template = (ROOT / 'deploy/mtls/Caddyfile.template').read_text()
    private_write(runtime / 'Caddyfile', template.replace('@HOST@', host).replace('@PORT@', str(port)).replace('@HUB_PORT@', str(backend)))
    compose = {'name': 'edgelab-mtls-' + hashlib.sha256(str(directory).encode()).hexdigest()[:12],
               'services': {'proxy': {'image': IMAGE, 'network_mode': 'host', 'restart': 'unless-stopped',
                 # Official Caddy binary carries this file capability; an empty bounding set causes exec EPERM.
                 'user': f'{os.getuid()}:{os.getgid()}', 'read_only': True, 'cap_drop': ['ALL'], 'cap_add': ['NET_BIND_SERVICE'],
                 'security_opt': ['no-new-privileges:true'], 'tmpfs': ['/data', '/config'],
                 'volumes': [{'type': 'bind', 'source': './runtime', 'target': '/etc/caddy', 'read_only': True,
                              'bind': {'create_host_path': False}}]}}}
    private_write(directory / 'compose.yaml', json.dumps(compose, indent=2) + '\n')
    print(f'Created independent server/client CAs in {authority}. Keep both CA private keys offline; never distribute them.')


def client(directory, name):
    if not re.fullmatch(r'[A-Za-z0-9_-]{1,64}', name):
        raise ValueError('CLIENT must be 1-64 letters, digits, underscores or hyphens')
    output = directory / 'clients' / name
    output.mkdir(parents=True, mode=0o700, exist_ok=False)
    certificate(output, 'client', directory / 'authority/client-ca', name,
                'basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=clientAuth')
    shutil.copyfile(directory / 'authority/server-ca.pem', output / 'hub-ca.pem')
    for path in output.iterdir():
        path.chmod(0o600)
    print(f'Distribute ONLY {output} to this client over a trusted channel, plus the hub publisher.pub. Never distribute authority/ or runtime/.')


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['init', 'up', 'down', 'client'])
    parser.add_argument('--env-file', default=str(ROOT / 'hub.env'))
    parser.add_argument('--client', default=os.environ.get('CLIENT', ''))
    args = parser.parse_args()
    values = environment(args.env_file)
    directory = Path(values['MTLS_DIRECTORY']).expanduser().resolve()
    if args.action == 'init':
        initialize(directory, values)
    elif args.action == 'client':
        client(directory, args.client)
    else:
        compose = ['docker', 'compose', '-f', str(directory / 'compose.yaml')]
        if args.action == 'up':
            run(*compose, 'run', '--rm', '--no-deps', 'proxy', 'caddy', 'validate', '--config', '/etc/caddy/Caddyfile')
            subprocess.run(compose + ['up', '-d'], check=True)
        else:
            subprocess.run(compose + ['down'], check=True)
            print('Only the mTLS proxy was removed. Hub and all trust material remain; install another proxy before remote clients reconnect.')


if __name__ == '__main__':
    try:
        main()
    except subprocess.CalledProcessError:
        sys.exit('mtls: command failed (output withheld to protect credentials); check paths, Docker, ports and OpenSSL')
    except (KeyError, ValueError, OSError) as exc:
        sys.exit(f'mtls: {exc}')

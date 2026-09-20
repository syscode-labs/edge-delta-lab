#!/usr/bin/env python3
"""Released bundle installer. Python 3 stdlib; no checkout or compiler required."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import sys
from urllib.parse import urlsplit

BUNDLE = Path(__file__).resolve().parents[1]
CONFIG = Path('/etc/edgelab/receiver.json')
UNIT = Path('/etc/systemd/system/edgelab-receiver.service')
MANAGER = Path('/usr/local/bin/edgelab-manage')
BINARY = Path('/usr/local/bin/edgelab')


def run(*cmd, **kwargs):
    return subprocess.run(list(map(str, cmd)), check=True, **kwargs)


def require(command):
    if not shutil.which(command):
        raise ValueError(f'required command not found: {command}')


def write(path, content, mode):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content)
    path.chmod(mode)


def receiver_config(args):
    url = urlsplit(args.hub)
    if (url.scheme not in ('https', 'http') or not url.hostname or url.username
            or url.password or url.query or url.fragment or url.path not in ('', '/')):
        raise ValueError('--hub must be an HTTPS origin without credentials, path, query or fragment')
    if url.scheme == 'http' and not args.allow_http:
        raise ValueError('HTTP requires explicit --allow-http (isolated networks only)')
    _ = url.port  # Reject malformed/out-of-range ports before installing anything.
    if not re.fullmatch(r'[A-Za-z0-9_.-]{1,128}', args.device_id):
        raise ValueError('--device-id must contain only letters, digits, dot, underscore or hyphen')
    key = Path(args.public_key).read_text().strip()
    try:
        decoded = bytes.fromhex(key)
    except ValueError as exc:
        raise ValueError('--public-key must be a hex Ed25519 public key') from exc
    if len(decoded) != 32:
        raise ValueError('--public-key must contain ONLY the 32-byte public key, never a private key')
    if not re.fullmatch(r'[0-9a-fA-F]{64}', key):
        raise ValueError('--public-key must be exactly 64 hex characters')
    return dict(hub=args.hub.rstrip('/'), device_id=args.device_id,
                allow_http=args.allow_http, docker_load=args.docker_load), key + '\n'


def service(docker_load=False, tls=False):
    # Docker group is root-equivalent and deliberately opt-in. No root receiver.
    return '''[Unit]
Description=Edge Delta signed artifact receiver
Wants=network-online.target
After=network-online.target
StartLimitIntervalSec=0

[Service]
Type=simple
DynamicUser=yes
User=edgelab-receiver
StateDirectory=edgelab-receiver
StateDirectoryMode=0700
ExecStart=/usr/local/bin/edgelab-manage receiver run
Restart=always
RestartSec=10
Environment=PATH=/usr/local/bin:/usr/bin:/bin
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes
UMask=0077
''' + ('SupplementaryGroups=docker\n' if docker_load else '') + (''.join(
        f'LoadCredential={name}:{CONFIG.parent}/tls/{name}\n'
        for name in ('hub-ca.pem', 'client.pem', 'client.key')) if tls else '') + '''
[Install]
WantedBy=multi-user.target
'''


def receiver(args):
    if args.action == 'run':
        cfg = json.loads(CONFIG.read_text())
        hub = cfg['hub']
        cmd = [str(BINARY), 'watch', '--base', hub, '--manifest', hub + '/releases/desired.json',
               '--receipt-url', hub + '/receipts', '--device-id', cfg['device_id'],
               '--pub', str(CONFIG.with_name('publisher.pub')), '--state', '/var/lib/edgelab-receiver']
        if cfg['allow_http']:
            cmd.append('--allow-http')
        if cfg['docker_load']:
            cmd.append('--docker-load')
        if cfg.get('mtls'):
            credentials = Path(os.environ['CREDENTIALS_DIRECTORY'])
            for flag, name in [('hub-ca', 'hub-ca.pem'), ('hub-client-cert', 'client.pem'), ('hub-client-key', 'client.key')]:
                cmd.extend(['--' + flag, str(credentials / name)])
        os.execv(cmd[0], cmd)
    if platform.system() != 'Linux':
        raise ValueError('receiver installation requires Linux with systemd')
    if os.geteuid() != 0:
        raise ValueError('receiver lifecycle requires root; run with sudo')
    require('systemctl')
    if args.action == 'setup':
        if UNIT.exists() or BINARY.exists() or MANAGER.exists():
            raise ValueError('existing installation or binary; refusing overwrite (uninstall first; retain keys/state)')
        retained = CONFIG.exists()
        if retained:
            cfg = json.loads(CONFIG.read_text())
            key = CONFIG.with_name('publisher.pub').read_text()
            args.docker_load = cfg['docker_load']
        else:
            cfg, key = receiver_config(args)
        run('systemctl', 'show', '--property=Version', '--value', stdout=subprocess.DEVNULL)
        binary = BUNDLE / 'edgelab'
        if not binary.is_file():
            binary = BUNDLE / 'bin/edgelab'
        if not binary.is_file():
            raise ValueError('missing bundled edgelab; extract the complete Linux release archive')
        run(binary, 'watch', '--help', stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        tls = [getattr(args, field, None) for field in ('hub_ca', 'hub_client_cert', 'hub_client_key')]
        if retained:
            tls = [str(CONFIG.parent / 'tls' / name) for name in ('hub-ca.pem', 'client.pem', 'client.key')] if cfg.get('mtls') else [None] * 3
        if any(tls) and not all(tls):
            raise ValueError('mTLS requires --hub-ca, --hub-client-cert and --hub-client-key together')
        if any(tls) and cfg['hub'].startswith('http:'):
            raise ValueError('mTLS requires HTTPS')
        tls_data = [Path(path).read_bytes() for path in tls] if all(tls) else []
        cfg['mtls'] = bool(tls_data)
        if tls_data:
            version = run('systemctl', '--version', capture_output=True, text=True).stdout
            match = re.match(r'systemd (\d+)', version)
            if not match or int(match.group(1)) < 247:
                raise ValueError('mTLS receiver requires systemd 247 or newer (LoadCredential)')
        if args.docker_load:
            require('docker')
            import grp
            try:
                grp.getgrnam('docker')
            except KeyError as exc:
                raise ValueError('--docker-load requires a docker group with daemon socket access') from exc
            run('docker', 'info', stdout=subprocess.DEVNULL)
            print('WARNING: Docker import grants root-equivalent daemon access; containers are not activated.', file=sys.stderr)
        BINARY.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(binary, BINARY)
        BINARY.chmod(0o755)
        shutil.copyfile(Path(__file__), MANAGER)
        MANAGER.chmod(0o755)
        write(CONFIG, json.dumps(cfg, indent=2) + '\n', 0o644)
        CONFIG.parent.chmod(0o755)  # DynamicUser must read config even under root's umask 077.
        write(CONFIG.with_name('publisher.pub'), key, 0o644)
        if tls_data:
            directory = CONFIG.parent / 'tls'
            directory.mkdir(mode=0o700, exist_ok=True)
            for name, data in zip(('hub-ca.pem', 'client.pem', 'client.key'), tls_data):
                write(directory / name, data.decode(), 0o600)
        write(UNIT, service(args.docker_load, bool(tls_data)), 0o644)
        run('systemctl', 'daemon-reload')
        run('systemctl', 'enable', '--now', 'edgelab-receiver.service')
        print('Installed. Manage: sudo edgelab-manage receiver status|start|stop|restart|uninstall')
    elif args.action == 'uninstall':
        run('systemctl', 'disable', '--now', 'edgelab-receiver.service')
        UNIT.unlink(missing_ok=True)
        BINARY.unlink(missing_ok=True)
        MANAGER.unlink(missing_ok=True)
        run('systemctl', 'daemon-reload')
        print('Service/binaries removed. Retained /etc/edgelab and /var/lib/edgelab-receiver; remove explicitly to reset trust/state.')
    else:
        run('systemctl', args.action, 'edgelab-receiver.service')


def hub(args):
    require('docker')
    run('docker', 'compose', 'version', stdout=subprocess.DEVNULL)
    directory = Path(args.directory).expanduser().resolve()
    if args.action == 'setup':
        if urlsplit(args.registry).scheme == 'http' and not getattr(args, 'allow_registry_http', False):
            raise ValueError('HTTP registry requires explicit --allow-registry-http (isolated networks only)')
        run('docker', 'info', stdout=subprocess.DEVNULL)
        spec = importlib.util.spec_from_file_location('compose_setup', BUNDLE / 'deploy/compose/setup.py')
        setup = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(setup)
        args.binary = str(BUNDLE / ('edgelab' if (BUNDLE / 'edgelab').is_file() else 'bin/edgelab'))
        args.runtime_bundle = str(BUNDLE)
        setup.install(args)
        shutil.copyfile(Path(__file__), directory / 'manage')
        (directory / 'manage').chmod(0o755)
    compose = ['docker', 'compose', '-f', str(directory / 'compose.yaml')]
    # Credentials stay in the private installation, never in argv or output.
    credential = directory / 'registry-password'
    env = dict(os.environ)
    if credential.exists():
        env['REGISTRY_PASSWORD'] = credential.read_text().rstrip('\r\n')
    actions = {'setup': ['up', '-d', '--build'], 'start': ['up', '-d'],
               'stop': ['stop'], 'restart': ['restart'], 'status': ['ps'], 'uninstall': ['down']}
    run(*compose, *actions[args.action], env=env)
    if args.action == 'uninstall':
        print(f'Containers removed; keys/config/data retained in {directory}. Remove that directory explicitly to purge.')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    roles = parser.add_subparsers(dest='role', required=True)
    for role in ('receiver', 'hub'):
        sub = roles.add_parser(role)
        sub.add_argument('action', choices=['setup', 'start', 'status', 'stop', 'restart', 'uninstall'] + (['run'] if role == 'receiver' else []))
        if role == 'receiver':
            sub.add_argument('--hub')
            sub.add_argument('--public-key')
            sub.add_argument('--device-id', default=platform.node())
            sub.add_argument('--allow-http', action='store_true')
            sub.add_argument('--docker-load', action='store_true')
            sub.add_argument('--hub-ca')
            sub.add_argument('--hub-client-cert')
            sub.add_argument('--hub-client-key')
        else:
            sub.add_argument('--directory', required=True)
            sub.add_argument('--registry')
            sub.add_argument('--allow-registry-http', action='store_true')
            sub.add_argument('--repository')
            sub.add_argument('--allow')
            sub.add_argument('--username')
            sub.add_argument('--password-file', help='registry password file, copied privately; never put password in arguments')
            sub.add_argument('--port', type=int, default=8080)
    args = parser.parse_args()
    if args.action == 'setup':
        needed = ('hub', 'public_key') if args.role == 'receiver' else ('registry', 'repository', 'allow')
        for field in needed:
            if not getattr(args, field):
                parser.error(f'--{field.replace("_", "-")} is required for setup')
    try:
        (receiver if args.role == 'receiver' else hub)(args)
    except (OSError, ValueError, subprocess.CalledProcessError) as exc:
        parser.exit(1, f'install: {exc}\n')


if __name__ == '__main__':
    main()

#!/usr/bin/env python3
"""Make entry point. Env files are data, never shell scripts."""
import argparse
import os
from pathlib import Path
import platform
import subprocess
import sys
import install

ROOT = Path(__file__).resolve().parents[1]


def environment(path):
    values = {}
    for number, line in enumerate(Path(path).read_text().splitlines(), 1):
        line = line.strip()
        if not line or line.startswith('#'):
            continue
        key, sep, value = line.partition('=')
        if not sep or not key.replace('_', '').isalnum():
            raise ValueError(f'{path}:{number}: expected KEY=value')
        if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
            value = value[1:-1]
        values[key] = value
    return values


def boolean(values, key):
    value = values.get(key, 'false').lower()
    if value not in ('true', 'false'):
        raise ValueError(f'{key} must be true or false')
    return value == 'true'


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('role', choices=['hub', 'receiver'])
    parser.add_argument('action', choices=['up', 'status', 'restart', 'stop', 'uninstall'])
    parser.add_argument('--env-file')
    args = parser.parse_args()
    values = environment(args.env_file or ROOT / (args.role + '.env'))
    action = args.action
    if args.role == 'hub':
        directory = Path(values['HUB_DIRECTORY']).expanduser().resolve()
        action = ('start' if (directory / 'compose.yaml').is_file() else 'setup') if action == 'up' else action
        opts = dict(directory=str(directory), registry=values.get('REGISTRY'), repository=values.get('REPOSITORY'),
                    allow=values.get('ALLOW'), username=values.get('REGISTRY_USERNAME') or None,
                    password_file=values.get('REGISTRY_PASSWORD_FILE') or None, port=int(values.get('HUB_PORT', '8080')))
        opts['allow_registry_http'] = boolean(values, 'REGISTRY_ALLOW_HTTP')
    else:
        action = ('start' if install.UNIT.exists() else 'setup') if action == 'up' else action
        opts = dict(hub=values.get('HUB_URL'), public_key=values.get('PUBLISHER_PUBLIC_KEY'),
                    device_id=values.get('DEVICE_ID', ''), allow_http=boolean(values, 'ALLOW_HTTP'),
                    docker_load=boolean(values, 'DOCKER_LOAD'), hub_ca=values.get('HUB_CA') or None,
                    hub_client_cert=values.get('HUB_CLIENT_CERT') or None, hub_client_key=values.get('HUB_CLIENT_KEY') or None)
    if action == 'setup' and not (ROOT / 'edgelab').exists():
        if platform.system() != 'Linux':
            raise ValueError('persistent source installation requires a Linux host; use container-multi on other systems')
        if not (ROOT / 'go.mod').exists():
            raise ValueError('missing bundled edgelab binary')
        install.require('go')
        subprocess.run(['go', 'build', '-trimpath', '-o', str(ROOT / 'bin/edgelab'), './cmd/edgelab'],
                       cwd=ROOT, env=dict(os.environ, CGO_ENABLED='0'), check=True)
    getattr(install, args.role)(argparse.Namespace(action=action, **opts))


if __name__ == '__main__':
    try:
        main()
    except (KeyError, OSError, ValueError, subprocess.CalledProcessError) as exc:
        sys.exit(f'lifecycle: {exc}')

#!/usr/bin/env python3
"""Exercise an extracted Linux release in an owned container with no Go/source/socket.
This validates package commands, not systemd or registry-to-Docker delivery.
"""
import argparse

from pathlib import Path
import subprocess
import uuid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--archive', required=True, type=Path)
    args = parser.parse_args()
    archive = args.archive.resolve(strict=True)
    # Inspect before mutation; --rm only affects this uniquely named test container.
    for command in (['context', 'show'], ['ps', '-a'], ['network', 'ls'], ['volume', 'ls']):
        subprocess.run(['docker', *command], check=True)
    script = '''set -eu
apk add --no-cache make openssl >/dev/null
mkdir /bundle
cd /bundle
tar xzf /input/archive.tar.gz
! command -v go
! test -e go.mod
./edgelab keygen --out /tmp/keys
./install receiver --help >/dev/null
./install hub --help >/dev/null
cp hub.env.example hub.env
# Only proof-specific paths/hostname; packaged Makefile, Python and Caddy template unchanged.
python3 -c 'from pathlib import Path; p=Path("hub.env"); s=p.read_text().replace("/opt/edgelab-mtls", "/tmp/mtls").replace("hub.example.net", "localhost"); p.write_text(s)'
make mtls-init
make mtls-client CLIENT=edge-01
openssl verify -purpose sslclient -CAfile /tmp/mtls/authority/client-ca.pem /tmp/mtls/clients/edge-01/client.pem
for role in hub receiver; do
  for action in up status restart stop uninstall; do
    make -n "$role-$action" >/dev/null
  done
done
printf 'PASS: extracted Linux native keygen, install help, Make lifecycle dispatch, real mTLS enrollment without Go or checkout\\n'
'''
    subprocess.run(['docker', 'run', '--rm', '--name', 'edpackage-' + uuid.uuid4().hex[:10],
                    '--mount', f'type=bind,src={archive},dst=/input/archive.tar.gz,readonly',
                    'python:3.12-alpine', 'sh', '-c', script], check=True)


if __name__ == '__main__':
    main()

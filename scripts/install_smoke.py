#!/usr/bin/env python3
"""Opt-in real registry -> installed Compose publisher/hub -> native Docker client proof.
Uses a uniquely named disposable project and a same-daemon receiver. Never installs
onto a cluster or mounts Docker's socket into a service. Network pulls required.
"""
import argparse
import base64
import tarfile
import hashlib
import json
import os
from pathlib import Path
import socket
import subprocess
import time
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[1]


def port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


def wait(check, description, timeout=180):
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        try:
            result = check()
            if result:
                return result
        except (OSError, ValueError, KeyError, subprocess.CalledProcessError):
            pass
        time.sleep(1)
    raise RuntimeError('timed out: ' + description)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--work', type=Path, required=True)
    args = parser.parse_args()
    work = args.work.resolve()
    work.mkdir(parents=True, exist_ok=False)
    token = 'edinstall-' + uuid.uuid4().hex[:10]
    transcript = (work / 'transcript.log').open('w')
    client = None
    owned = []
    compose = []

    def run(*cmd, check=True, env=None):
        transcript.write('$ ' + ' '.join(map(str, cmd)) + '\n'); transcript.flush()
        result = subprocess.run(list(map(str, cmd)), cwd=ROOT, env=env,
                                stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, timeout=600)
        transcript.write(result.stdout); transcript.flush()
        if check and result.returncode:
            raise RuntimeError(f'{cmd[0]} exited {result.returncode}: {result.stdout[-3000:]}')
        return result

    def save(name, data):
        (work / name).write_text(json.dumps(data, indent=2) + '\n')

    try:
        platform = run('docker', 'info', '--format', '{{.OSType}}/{{.Architecture}}').stdout.strip().replace('x86_64', 'amd64').replace('aarch64', 'arm64')
        assert platform.startswith('linux/'), platform
        run('go', 'build', '-o', work / 'edgelab', './cmd/edgelab')
        hp, rp = port(), port()
        install = work / 'installation'
        run('python3', 'deploy/compose/setup.py', '--directory', install,
            '--binary', work / 'edgelab', '--registry', 'http://registry:5000',
            '--repository', 'proof/app', '--allow', '^v[12]$', '--port', hp)
        # Only the proof adds a registry. The documented installation expects an existing one.
        override = work / 'registry.yaml'
        override.write_text(json.dumps({'services': {'registry': {
            'image': 'registry:2', 'ports': [f'127.0.0.1:{rp}:5000']}}}))
        compose = ['docker', 'compose', '-p', token, '-f', str(install / 'compose.yaml'), '-f', str(override)]
        run(*compose, 'up', '-d', '--build')
        def registry_ready():
            with urllib.request.urlopen(f'http://127.0.0.1:{rp}/v2/', timeout=2) as response:
                return response.status == 200
        wait(registry_ready, 'real registry')
        base = f'http://127.0.0.1:{hp}'
        context = work / 'image'
        context.mkdir()
        payload = hashlib.shake_256(b'edge-install-real-image').digest(4 << 20)
        (context / 'payload.bin').write_bytes(payload)
        (context / 'Dockerfile').write_text(f'FROM alpine:3.20\nLABEL edge-delta-install-proof="{token}"\nCOPY payload.bin release.txt /\nCMD ["sh", "-c", "cat /release.txt; sha256sum /payload.bin"]\n')
        summaries = []
        ids = []
        client_log = (work / 'client.log').open('w')
        client = subprocess.Popen([str(work / 'edgelab'), 'watch', '--manifest', base + '/releases/desired.json',
            '--base', base, '--pub', str(install / 'keys/publisher.pub'), '--state', str(work / 'client'),
            '--allow-http', '--events-url', base.replace('http:', 'ws:') + '/events', '--poll', '2s', '--docker-load'],
            cwd=ROOT, stdout=client_log, stderr=client_log)
        for version in (1, 2):
            if version == 2:
                # New v2 publication must succeed after the installed services restart.
                run(*compose, 'restart', 'publisher', 'hub')
                payload = payload[:(2 << 20)] + b'changed!' * 4096 + payload[(2 << 20) + 32768:]
                (context / 'payload.bin').write_bytes(payload)
            (context / 'release.txt').write_text(f'v{version}\n')
            before_ids = set(run('docker', 'image', 'ls', '-aq', '--no-trunc').stdout.split())
            tag = f'127.0.0.1:{rp}/proof/app:v{version}'
            owned.append(tag)
            run('docker', 'build', '--provenance=false', '--platform', platform, '--no-cache', '-t', tag, context)
            image_id = run('docker', 'image', 'inspect', tag, '--format', '{{.Id}}').stdout.strip()
            ids.append(image_id)
            run('docker', 'push', tag)
            run('docker', 'image', 'rm', tag)
            # Prove the actual receiver lacks the image before Edge Delta imports it.
            absent = run('docker', 'image', 'inspect', image_id, check=False).returncode != 0
            assert absent, 'receiver must not retain the source image'
            def loaded():
                if client.poll() is not None:
                    raise RuntimeError('persistent client exited')
                # Read the first completed transfer, not an overwritten repeat summary.
                text = (work / 'client.log').read_text()
                decoder = json.JSONDecoder()
                while '{' in text:
                    text = text[text.index('{'):]
                    data, end = decoder.raw_decode(text)
                    text = text[end:]
                    if data.get('phase') == 'loaded' and data.get('release', '').endswith(f'v{version}') and 'chunk_response_body_bytes' in data:
                        return data
                return None
            summary = wait(loaded, f'loaded v{version}')
            save(f'v{version}-summary.json', summary)
            # Docker 29 uses manifest IDs, not config IDs, as store references.
            # Save only newly appeared candidates and bind their config bytes to the
            # signed image ID before using a store reference for the offline probe.
            envelope = json.loads((install / 'origin/releases/desired.json').read_text())
            signed_id = json.loads(base64.b64decode(envelope['payload']))['image_ids'][0]
            candidates = set(run('docker', 'image', 'ls', '-aq', '--no-trunc').stdout.split()) - before_ids
            stored = None
            for candidate in candidates:
                archive = work / 'stored.tar'
                run('docker', 'image', 'save', '-o', archive, candidate)
                with tarfile.open(archive) as tar:
                    manifest = json.load(tar.extractfile('manifest.json'))[0]
                    config = tar.extractfile(manifest['Config']).read()
                archive.unlink()
                if 'sha256:' + hashlib.sha256(config).hexdigest() == signed_id:
                    stored = candidate
                    ids.append(stored)
                    break
            assert stored, 'no new stored image matches signed config ID'
            actual = run('docker', 'run', '--rm', '--pull', 'never', '--network', 'none', stored).stdout.strip()
            expected = f'v{version}\n{hashlib.sha256(payload).hexdigest()}  /payload.bin'
            assert actual == expected, (actual, expected)
            summaries.append({'version': version, 'source_inspect_id': image_id, 'signed_config_id': signed_id,
                              'stored_reference': stored, 'absent_before_import': absent,
                              'offline_probe': actual, 'summary': summary})
        assert summaries[1]['summary']['reused_chunks'] > 0
        assert summaries[1]['summary']['chunk_response_body_bytes'] < summaries[0]['summary']['chunk_response_body_bytes']
        key_hash = hashlib.sha256((install / 'keys/publisher.key').read_bytes()).hexdigest()
        sequence = (install / 'watcher-state/sequence').read_text()
        run(*compose, 'restart', 'publisher', 'hub')
        def hub_ready():
            with urllib.request.urlopen(base + '/releases/desired.json', timeout=2) as response:
                return response.status == 200
        wait(hub_ready, 'hub after restart')
        assert (install / 'watcher-state/sequence').read_text() == sequence
        assert hashlib.sha256((install / 'keys/publisher.key').read_bytes()).hexdigest() == key_hash
        # Restart the native receiver with its existing state, not a fresh cache.
        client.terminate(); client.wait(timeout=20)
        summary_path = work / 'client/summary.json'
        previous_mtime = summary_path.stat().st_mtime_ns
        client = subprocess.Popen([str(work / 'edgelab'), 'watch', '--manifest', base + '/releases/desired.json',
            '--base', base, '--pub', str(install / 'keys/publisher.pub'), '--state', str(work / 'client'),
            '--allow-http', '--poll', '2s', '--docker-load'], stdout=client_log, stderr=client_log)
        wait(lambda: client.poll() is None and summary_path.stat().st_mtime_ns > previous_mtime,
             'fresh completion from restarted client', timeout=30)
        resumed = json.loads(summary_path.read_text())
        assert resumed['phase'] == 'loaded' and resumed['chunk_response_body_bytes'] == 0
        save('result.json', {'status': 'PASS', 'topology': 'same local Linux Docker daemon; native host client',
             'platform': platform, 'versions': summaries, 'restart_summary': resumed,
             'publisher_sequence_preserved': True, 'signing_key_preserved': True,
             'not_proven': ['distinct remote receiver', 'production TLS/auth', 'container activation', 'power loss']})
        print('PASS:', work / 'result.json')
    finally:
        if client and client.poll() is None:
            client.terminate()
            try:
                client.wait(timeout=20)
            except subprocess.TimeoutExpired:
                client.kill(); client.wait(timeout=10)
        cleanup_errors = []
        cleanup_commands = []
        if compose:
            cleanup_commands.extend([compose + ['logs', '--no-color'],
                                     compose + ['down', '--volumes', '--rmi', 'local']])
        cleanup_commands.extend(['docker', 'image', 'rm', reference]
                                for reference in owned + locals().get('ids', []))
        for command in cleanup_commands:
            try:
                run(*command, check=False)
            except (OSError, subprocess.TimeoutExpired) as exc:
                cleanup_errors.append(str(exc))
        save('cleanup.json', {'errors': cleanup_errors})
        transcript.close()


if __name__ == '__main__':
    main()

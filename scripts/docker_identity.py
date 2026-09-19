#!/usr/bin/env python3
"""Explicit Docker config identity and local-only integration-test boundaries.

A containerd-backed build's inspect.Id can be an OCI index/manifest digest.
Signed image_ids are always the SHA256 of the exact saved configuration bytes.
The source descriptor is provenance, never a substitute for that config ID.
"""
from __future__ import annotations
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile
from normalize_archive import normalize


def normalize_image(source: Path, target: Path, source_inspect: dict) -> dict:
    ids = normalize(source, target)  # Verifies *uncompressed* layer DiffIDs.
    if len(ids) != 1:
        raise ValueError('test requires exactly one saved image/platform')
    config_id = ids[0]
    with tarfile.open(target) as archive:
        manifest = json.load(archive.extractfile('manifest.json'))[0]
        config = json.load(archive.extractfile(manifest['Config']))
    platform = config['os'] + '/' + config['architecture']
    diff_ids = config['rootfs']['diff_ids']
    if platform != source_inspect['Os'] + '/' + source_inspect['Architecture']:
        raise ValueError('saved config platform differs from source inspect')
    if diff_ids != source_inspect['RootFS']['Layers']:
        raise ValueError('saved config DiffIDs differ from source inspect')
    source_id = source_inspect['Id']
    chain = [config_id] if source_id == config_id else None
    # The exact saved descriptor graph must bind the selected config to the
    # source index/manifest. Ignore attestation/other-platform leaves, not IDs.
    if chain is None:
        with tarfile.open(source) as archive:
            def walk(digest, seen):
                if digest == config_id:
                    return [digest]
                if digest in seen or len(seen) >= 8 or not re.fullmatch(r'sha256:[0-9a-f]{64}', digest):
                    return None
                name = 'blobs/sha256/' + digest.split(':')[1]
                member = archive.getmember(name)
                if not member.isfile() or member.size > 16 << 20:
                    raise ValueError('invalid source descriptor member')
                body = archive.extractfile(member).read()
                if 'sha256:' + hashlib.sha256(body).hexdigest() != digest:
                    raise ValueError('source descriptor hash mismatch')
                obj = json.loads(body)
                children = obj.get('manifests', [])
                if 'config' in obj:
                    children = [obj['config']]
                for child in children:
                    child_platform = child.get('platform', {})
                    if child_platform and (child_platform.get('os'), child_platform.get('architecture')) != tuple(platform.split('/')):
                        continue
                    found = walk(child['digest'], seen | {digest})
                    if found:
                        return [digest] + found
                return None
            try:
                chain = walk(source_id, set())
            except KeyError as exc:
                raise ValueError('source descriptor absent from saved archive') from exc
        if chain is None:
            raise ValueError('source descriptor does not reference preserved config')
    return dict(config_id=config_id, source_inspect_id=source_id,
                source_descriptor=source_inspect.get('Descriptor'),
                source_descriptor_chain=chain, platform=platform, diff_ids=diff_ids,
                config_bytes_preserved=True, raw_layer_diffids_verified=True)


class LocalDocker:
    """Pin the actual endpoint before any build/save/load; never upload remotely."""
    def __init__(self, context=''):
        self.env = dict(os.environ)
        selected = context or self.env.get('DOCKER_CONTEXT', '')
        if selected or not self.env.get('DOCKER_HOST'):
            selected = selected or subprocess.check_output(['docker', 'context', 'show'], text=True, timeout=30).strip()
            record = json.loads(subprocess.check_output(['docker', 'context', 'inspect', selected], text=True, timeout=30))[0]
            endpoint = record['Endpoints']['docker']['Host']
            self.prefix = ['docker', '--context', selected]
            self.env['DOCKER_CONTEXT'] = selected
            self.env.pop('DOCKER_HOST', None)
        else:
            endpoint = self.env['DOCKER_HOST']
            self.prefix = ['docker', '--host', endpoint]
        if not endpoint.startswith('unix:///'):
            raise ValueError(f'remote/unmeasured Docker API endpoint is not eligible for this proof: {endpoint}')
        self.record = dict(context=selected or None, endpoint=endpoint,
                           endpoint_scope='local-unix', remote_upload=False)
        info = json.loads(self.text('info', '--format', '{{json .}}', timeout=30))
        self.record.update(daemon_id=info['ID'], server_version=info['ServerVersion'])
        self.platform = self.text('version', '--format', '{{.Server.Os}}/{{.Server.Arch}}', timeout=30)
        self.record['platform'] = self.platform
        if not self.record['daemon_id'] or self.platform not in ('linux/amd64', 'linux/arm64'):
            raise ValueError('identified Linux amd64/arm64 Docker daemon required')

    def run(self, *command, **kwargs):
        kwargs.setdefault('check', True)
        kwargs.setdefault('timeout', 300)
        return subprocess.run(self.prefix + list(command), env=self.env, **kwargs)

    def text(self, *command, **kwargs):
        return self.run(*command, stdout=subprocess.PIPE, text=True, **kwargs).stdout.strip()

    def inspect(self, image):
        return json.loads(self.text('image', 'inspect', image))[0]

    def assert_absent(self, *references):
        checks = []
        for reference in dict.fromkeys(references):
            result = self.run('image', 'inspect', reference, check=False, capture_output=True, text=True, timeout=30)
            if result.returncode == 0:
                raise AssertionError(f'target already present before transfer: {reference}')
            # A stopped/unreachable daemon is not proof of image absence.
            if 'no such image:' not in result.stderr.lower():
                raise RuntimeError(f'image absence not proven: {result.stderr.strip()}')
            checks.append(dict(reference=reference, absent=True, stderr=result.stderr.strip()))
        return checks

    def verify_loaded(self, identity):
        reference = identity.get('stored_reference')
        if not reference:
            raise AssertionError('loaded reference unproven; identity lacks stored_reference')
        inspected = self.inspect(reference)
        # Docker 29 containerd labels images by manifest digest, so a runnable
        # tag reference can never equal inspect.Id. A digest reference must
        # match Id exactly; a tag reference must still be listed on the image
        # record. The config-digest binding was already proven from saved
        # bytes in stored_reference(); this is the store-side recheck.
        if reference.startswith('sha256:'):
            if inspected['Id'] != reference:
                raise AssertionError('loaded digest no longer resolves')
        elif reference not in inspected.get('RepoTags', []):
            raise AssertionError('loaded tag no longer resolves to the stored image')
        if inspected['Os'] + '/' + inspected['Architecture'] != identity['platform']:
            raise AssertionError('loaded platform differs')
        return dict(config_id=identity['config_id'], stored_reference=reference,
                    loaded_inspect_id=inspected['Id'], platform=identity['platform'], verified=True)

    def stored_reference(self, config_digest: str, expected_tag: str) -> str:
        """Resolve the runnable store reference for a loaded image.

        Docker 29 containerd stores label images with OCI manifest/index
        digests, so inspect.Id never equals the config digest. Loaded images
        keep their RepoTags, and normalization preserves them, so the unique
        per-run tag is the addressable candidate. Identity still follows the
        bytes, not the tag: the image is saved and its preserved config bytes
        must hash to the signed config digest before the reference is trusted.
        """
        with tempfile.TemporaryDirectory(prefix='identity-check-') as temp:
            probe = Path(temp) / 'probe.tar'
            self.run('image', 'save', '-o', str(probe), expected_tag)
            with tarfile.open(probe) as archive:
                manifest = json.load(archive.extractfile('manifest.json'))[0]
                config_bytes = archive.extractfile(manifest['Config']).read()
            if 'sha256:' + hashlib.sha256(config_bytes).hexdigest() != config_digest:
                raise AssertionError(f'image saved from {expected_tag} does not carry signed config digest {config_digest}')
            return expected_tag


def add_transport_arguments(parser, interruption=True):
    for flag, default in [('drop-every', 7), ('drop-after-bytes', 8192), ('latency-ms', 10),
                          ('fail-first', 1), ('corrupt-first', 1), ('stall-first', 0), ('stall-ms', 0), ('offline-for-ms', 0)]:
        parser.add_argument('--' + flag, type=int, default=default)
    parser.add_argument('--workers', type=int, default=4)
    parser.add_argument('--attempts', type=int, default=30)
    parser.add_argument('--backoff', default='100ms')
    parser.add_argument('--max-backoff', default='1s')
    parser.add_argument('--request-timeout', default='30s')
    if interruption:
        parser.add_argument('--kill-after-commits', type=int, default=4)
        parser.add_argument('--kill-timeout-seconds', type=float, default=120)


def transport_settings(args):
    faults = {name: getattr(args, name) for name in ('rate_kbit', 'drop_every', 'drop_after_bytes', 'latency_ms',
              'fail_first', 'corrupt_first', 'stall_first', 'stall_ms', 'offline_for_ms')}
    if any(value < 0 for value in faults.values()) or args.rate_kbit <= 0:
        raise ValueError('rate must be positive; fault counts/delays must be nonnegative')
    if min(args.workers, args.attempts, getattr(args, 'kill_after_commits', 1), getattr(args, 'kill_timeout_seconds', 1)) <= 0:
        raise ValueError('workers, bounded attempts, SIGKILL count and deadline must be positive')
    for value in (args.backoff, args.max_backoff, args.request_timeout):
        if not re.fullmatch(r'(?:[0-9]+(?:\.[0-9]+)?(?:ms|s|m|h))+', value) or not any(float(n) > 0 for n in re.findall(r'[0-9]+(?:\.[0-9]+)?', value)):
            raise ValueError('retry durations require a positive Go duration such as 100ms or 1s')
    retry = {name: getattr(args, name) for name in ('workers', 'attempts', 'backoff', 'max_backoff', 'request_timeout')}
    return faults, retry


def retry_arguments(settings):
    return [part for key, value in settings.items() for part in ('--' + key.replace('_', '-'), str(value))]

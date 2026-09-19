"""Save fixtures model Docker containerd's dual OCI + Docker archive format.

Generated tiny tar streams (unit inputs, not claimed live Docker evidence).
The blob filename is the *encoded* digest, not its uncompressed DiffID;
inspect.Id is the OCI index digest, not the preserved image configuration ID.
"""
import gzip
import hashlib
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch
import subprocess
from docker_identity import LocalDocker, normalize_image


def save_fixture(directory, compressed=True):
    data = io.BytesIO()
    with tarfile.open(fileobj=data, mode='w') as layer:
        entry = tarfile.TarInfo('payload.bin')
        body = b'actual tar stream semantics\n' * 1024
        entry.size = len(body)
        layer.addfile(entry, io.BytesIO(body))
    raw = data.getvalue()
    diff_id = 'sha256:' + hashlib.sha256(raw).hexdigest()
    config = json.dumps({'architecture': 'amd64', 'os': 'linux', 'config': {'Entrypoint': ['/probe']}, 'rootfs': {'type': 'layers', 'diff_ids': [diff_id]}}).encode()
    blobs = {}
    def blob(body, media):
        digest = 'sha256:' + hashlib.sha256(body).hexdigest()
        blobs['blobs/sha256/' + digest.split(':')[1]] = body
        return {'digest': digest, 'size': len(body), 'mediaType': media}
    config_desc = blob(config, 'application/vnd.oci.image.config.v1+json')
    layer_desc = blob(gzip.compress(raw, mtime=0) if compressed else raw, 'application/vnd.oci.image.layer.v1.tar+gzip' if compressed else 'application/vnd.oci.image.layer.v1.tar')
    manifest = blob(json.dumps({'schemaVersion': 2, 'mediaType': 'application/vnd.oci.image.manifest.v1+json', 'config': config_desc, 'layers': [layer_desc]}).encode(), 'application/vnd.oci.image.manifest.v1+json')
    manifest['platform'] = {'os': 'linux', 'architecture': 'amd64'}
    index = blob(json.dumps({'schemaVersion': 2, 'mediaType': 'application/vnd.oci.image.index.v1+json', 'manifests': [manifest]}).encode(), 'application/vnd.oci.image.index.v1+json')
    path = directory / 'save.tar'
    blobs['manifest.json'] = json.dumps([{'Config': 'blobs/sha256/' + config_desc['digest'].split(':')[1], 'RepoTags': ['unit:v1'], 'Layers': ['blobs/sha256/' + layer_desc['digest'].split(':')[1]]}]).encode()
    blobs['index.json'] = json.dumps({'schemaVersion': 2, 'manifests': [index]}).encode()
    blobs['oci-layout'] = b'{"imageLayoutVersion":"1.0.0"}'
    with tarfile.open(path, 'w') as archive:
        for name, body in blobs.items():
            entry = tarfile.TarInfo(name)
            entry.size = len(body)
            archive.addfile(entry, io.BytesIO(body))
    inspect = {'Id': index['digest'], 'Descriptor': index, 'Os': 'linux', 'Architecture': 'amd64', 'RootFS': {'Type': 'layers', 'Layers': [diff_id]}}
    return path, inspect, config_desc['digest'], config, raw


class IdentityTest(unittest.TestCase):
    def test_containerd_index_is_provenance_not_signed_config_id(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            source, inspect, config_id, config, raw = save_fixture(root)
            result = normalize_image(source, root / 'normalized.tar', inspect)
            self.assertEqual(result['config_id'], config_id)
            self.assertEqual(result['source_inspect_id'], inspect['Id'])
            self.assertNotEqual(result['source_inspect_id'], config_id)
            self.assertEqual(result['diff_ids'], inspect['RootFS']['Layers'])
            self.assertEqual(result['platform'], 'linux/amd64')
            self.assertEqual(result['source_descriptor_chain'][-1], config_id)
            with tarfile.open(root / 'normalized.tar') as archive:
                manifest = json.load(archive.extractfile('manifest.json'))[0]
                self.assertEqual(archive.extractfile(manifest['Config']).read(), config)
                self.assertEqual(archive.extractfile(manifest['Layers'][0]).read(), raw)

    def test_classic_config_id_and_raw_layer_identity(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            source, inspect, config_id, _, _ = save_fixture(root, compressed=False)
            inspect['Id'] = config_id
            inspect.pop('Descriptor')
            result = normalize_image(source, root / 'normalized.tar', inspect)
            self.assertEqual(result['source_descriptor_chain'], [config_id])

    def test_unrelated_source_id_is_not_accepted(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            source, inspect, _, _, _ = save_fixture(root)
            inspect['Id'] = 'sha256:' + 'a' * 64
            with self.assertRaisesRegex(ValueError, 'descriptor absent'):
                normalize_image(source, root / 'normalized.tar', inspect)

    def test_source_diffids_must_match_saved_config(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            source, inspect, _, _, _ = save_fixture(root)
            inspect['RootFS']['Layers'] = ['sha256:' + 'a' * 64]
            with self.assertRaisesRegex(ValueError, 'DiffIDs differ'):
                normalize_image(source, root / 'normalized.tar', inspect)

    def test_selected_platform_must_match(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            source, inspect, _, _, _ = save_fixture(root)
            inspect['Architecture'] = 'arm64'
            with self.assertRaisesRegex(ValueError, 'platform differs'):
                normalize_image(source, root / 'normalized.tar', inspect)


class LocalBoundaryTest(unittest.TestCase):
    def test_remote_context_rejected_before_daemon_access(self):
        record = [{'Endpoints': {'docker': {'Host': 'ssh://example.invalid'}}}]
        with patch('docker_identity.subprocess.check_output', return_value=json.dumps(record)) as query:
            with self.assertRaisesRegex(ValueError, 'remote/unmeasured'):
                LocalDocker('remote-unit-test')
            self.assertEqual(query.call_count, 1)

    def test_remote_docker_host_rejected_before_daemon_access(self):
        with patch.dict('os.environ', {'DOCKER_HOST': 'tcp://example.invalid:2375'}, clear=True), patch('docker_identity.subprocess.check_output') as query:
            with self.assertRaisesRegex(ValueError, 'remote/unmeasured'):
                LocalDocker()
            query.assert_not_called()

    def test_connection_failure_is_not_image_absence(self):
        daemon = object.__new__(LocalDocker)
        with patch.object(daemon, 'run', return_value=subprocess.CompletedProcess([], 1, '', 'Cannot connect to Docker daemon')):
            with self.assertRaisesRegex(RuntimeError, 'absence not proven'):
                daemon.assert_absent('sha256:' + 'a' * 64)

    def test_present_image_fails_absence_gate(self):
        daemon = object.__new__(LocalDocker)
        with patch.object(daemon, 'run', return_value=subprocess.CompletedProcess([], 0, '[]', '')):
            with self.assertRaisesRegex(AssertionError, 'already present'):
                daemon.assert_absent('sha256:' + 'a' * 64)

    def test_loaded_reference_and_platform_are_still_mandatory(self):
        # Docker 29 containerd labels images by manifest digest, so the legacy
        # inspect.Id == config_id equality is unstatable. The proven contract:
        # the identity must carry a stored_reference resolved by hashing saved
        # config bytes (stored_reference()); a digest reference must match
        # inspect.Id, a tag reference must remain listed in RepoTags; the
        # platform must match.
        daemon = object.__new__(LocalDocker)
        identity = dict(config_id='sha256:' + 'a' * 64, diff_ids=['sha256:' + 'b' * 64], platform='linux/amd64')
        with patch.object(daemon, 'inspect') as inspected:
            with self.assertRaisesRegex(AssertionError, 'stored_reference'):
                daemon.verify_loaded(identity)
            inspected.assert_not_called()
        tag = 'edge-delta-demo-unit:app-a-v1'
        identity['stored_reference'] = tag
        detached = dict(Id='sha256:' + 'c' * 64, RepoTags=[], Os='linux', Architecture='amd64')
        with patch.object(daemon, 'inspect', return_value=detached):
            with self.assertRaisesRegex(AssertionError, 'no longer resolves'):
                daemon.verify_loaded(identity)
        digest_ref = 'sha256:' + 'd' * 64
        with patch.object(daemon, 'inspect', return_value=dict(Id='sha256:' + 'e' * 64, RepoTags=[tag])):
            with self.assertRaisesRegex(AssertionError, 'no longer resolves'):
                daemon.verify_loaded(dict(identity, stored_reference=digest_ref))
        with patch.object(daemon, 'inspect', return_value=dict(Id=tag, RepoTags=[tag], Os='windows', Architecture='amd64')):
            with self.assertRaisesRegex(AssertionError, 'platform differs'):
                daemon.verify_loaded(identity)
        with patch.object(daemon, 'inspect', return_value=dict(Id='sha256:' + 'f' * 64, RepoTags=[tag], Os='linux', Architecture='amd64')):
            result = daemon.verify_loaded(identity)
        self.assertTrue(result['verified'])
        self.assertEqual(result['config_id'], identity['config_id'])
        self.assertEqual(result['stored_reference'], tag)


if __name__ == '__main__':
    unittest.main()

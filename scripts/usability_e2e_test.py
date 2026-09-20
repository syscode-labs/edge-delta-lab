"""Driver contract tests only: these do NOT claim two-host runtime success."""
import argparse
import io
import json
from pathlib import Path
import struct
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch
import usability_e2e as e2e


class Contracts(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.h = e2e.Harness(argparse.Namespace(work=self.root / 'evidence'))

    def archive(self, architecture=62, extra=None):
        path = self.root / 'bundle.tar.gz'
        binary = bytearray(64)
        binary[:6] = b'\x7fELF\x02\x01'
        binary[18:20] = struct.pack('<H', architecture)
        files = {'edgelab': bytes(binary), 'Makefile': b'', 'hub.env.example': b'',
                 'receiver.env.example': b'', 'scripts/lifecycle.py': b'',
                 'scripts/install.py': b'', 'scripts/mtls.py': b''}
        with tarfile.open(path, 'w:gz') as t:
            for name, data in files.items():
                member = tarfile.TarInfo(name)
                member.size = len(data)
                member.mode = 0o755
                t.addfile(member, io.BytesIO(data))
            if extra:
                t.addfile(extra)
        return path

    def test_amd64_archive_identity_is_inspection_not_execution(self):
        value = e2e.archive_info(self.archive(), 'amd64')
        self.assertEqual(len(value['archive_sha256']), 64)
        self.assertFalse(value['runtime_executed'])

    def test_arm64_is_inspected_only_and_mismatch_rejected(self):
        path = self.archive(183)
        self.assertFalse(e2e.archive_info(path, 'arm64')['runtime_executed'])
        with self.assertRaises(ValueError):
            e2e.archive_info(path, 'amd64')

    def test_path_traversal_and_links_rejected_before_guest_creation(self):
        for name, kind in (('../escape', tarfile.REGTYPE), ('/absolute', tarfile.REGTYPE),
                           ('link', tarfile.SYMTYPE), ('hard', tarfile.LNKTYPE)):
            m = tarfile.TarInfo(name)
            m.type = kind
            m.linkname = '/etc/shadow'
            with self.subTest(name=name), self.assertRaises(ValueError):
                e2e.archive_info(self.archive(extra=m), 'amd64')

    def test_first_summary_preserved_in_mixed_journal(self):
        first = {'phase': 'loaded', 'sequence': 1, 'downloaded_chunks': 6}
        repeat = dict(first, downloaded_chunks=0)
        text = 'Loaded image ID: sha256:abcd\n' + json.dumps(first, indent=2) + '\n' + json.dumps({'event': 'poll'}) + '\n' + json.dumps(repeat)
        self.assertEqual(e2e.summaries(text), [first, repeat])

    def test_incomplete_and_staged_summaries_not_success(self):
        self.assertEqual(e2e.summaries('{"phase":"staged","downloaded_chunks":1}\n{\n"phase":'), [])

    def test_work_directory_must_be_fresh(self):
        with self.assertRaises(FileExistsError):
            e2e.Harness(argparse.Namespace(work=self.h.work))

    def test_unowned_guest_refused_even_for_root(self):
        with patch.object(self.h, 'command') as run:
            with self.assertRaises(ValueError):
                self.h.remote('production', 'true', root=True)
            run.assert_not_called()

    def test_remote_anchors_in_guest_home_not_translated_host_cwd(self):
        self.h.owned.append('ed-use-test-hub')
        with patch.object(self.h, 'command') as run:
            self.h.remote('ed-use-test-hub', 'make hub-up')
            self.assertEqual(run.call_args.args[0][-1], 'cd "$HOME"; make hub-up')

    def test_private_operation_does_not_retain_output_or_stdin(self):
        secret = b'super-secret-registry-value'
        with patch('usability_e2e.subprocess.run', return_value=subprocess.CompletedProcess([], 0, secret, secret)):
            self.h.command(['some-command'], data=secret)
        for path in self.h.work.iterdir():
            self.assertNotIn(secret, path.read_bytes())

    def test_private_failure_does_not_disclose_output(self):
        with patch('usability_e2e.subprocess.run', return_value=subprocess.CompletedProcess([], 1, b'password', b'password')):
            with self.assertRaisesRegex(RuntimeError, 'sensitive output withheld'):
                self.h.command(['some-command'])
        self.assertNotIn('password', (self.h.work / '001.json').read_text())

    def test_make_uses_documented_default_env_and_receiver_sudo(self):
        self.h.hub = 'hub'
        self.h.receiver = 'receiver'
        with patch.object(self.h, 'remote') as run:
            self.h.make('hub', 'up')
            self.assertEqual(run.call_args.args[:2], ('hub', 'cd bundle; make hub-up'))
            self.h.make('receiver', 'up')
            self.assertEqual(run.call_args.args[:2], ('receiver', 'cd bundle; sudo make receiver-up'))


if __name__ == '__main__':
    unittest.main()

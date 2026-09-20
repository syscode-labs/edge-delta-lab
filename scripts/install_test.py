"""Installer contracts: real filesystem/keygen; mocked host service manager only."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import install

ROOT = Path(__file__).resolve().parents[1]


class InstallerTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.build = tempfile.TemporaryDirectory()
        cls.native = Path(cls.build.name) / 'edgelab'
        subprocess.run(['go', 'build', '-o', str(cls.native), './cmd/edgelab'], cwd=ROOT, check=True)

    @classmethod
    def tearDownClass(cls):
        cls.build.cleanup()

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.bundle = self.root / 'bundle'
        self.bundle.mkdir()
        shutil.copyfile(self.native, self.bundle / 'edgelab')
        (self.bundle / 'edgelab').chmod(0o755)
        shutil.copytree(ROOT / 'deploy/compose', self.bundle / 'deploy/compose')
        subprocess.run([str(self.native), 'keygen', '--out', str(self.root / 'keys')], check=True)
        self.args = argparse.Namespace(action='setup', hub='https://hub.example.net',
            public_key=str(self.root / 'keys/publisher.pub'), device_id='edge-01', allow_http=False, docker_load=False)
        for name, value in {'BUNDLE': self.bundle, 'CONFIG': self.root / 'etc/receiver.json',
                            'UNIT': self.root / 'systemd/receiver.service', 'BINARY': self.root / 'bin/edgelab',
                            'MANAGER': self.root / 'bin/edgelab-manage'}.items():
            p = patch.object(install, name, value)
            p.start()
            self.addCleanup(p.stop)

    def test_https_and_public_only(self):
        cfg, key = install.receiver_config(self.args)
        self.assertFalse(cfg['docker_load'])
        self.assertEqual(len(bytes.fromhex(key.strip())), 32)
        for hub in ['http://hub.test', 'https://u:secret@hub.test', 'ftp://hub.test',
                    'https://hub.test/path', 'https://hub.test/?token=secret']:
            self.args.hub = hub
            with self.subTest(hub=hub), self.assertRaises(ValueError):
                install.receiver_config(self.args)
        self.args.hub, self.args.allow_http = 'http://hub.test', True
        self.assertTrue(install.receiver_config(self.args)[0]['allow_http'])
        self.args.public_key = str(self.root / 'keys/publisher.key')
        with self.assertRaisesRegex(ValueError, 'never a private key'):
            install.receiver_config(self.args)

    @patch.object(install, 'require')
    @patch.object(install.os, 'geteuid', return_value=0)
    @patch.object(install.platform, 'system', return_value='Linux')
    def test_receiver_lifecycle_and_launcher(self, *_):
        real_run = install.run
        commands = []
        def run(*cmd, **kwargs):
            if str(cmd[0]) == 'systemctl':
                commands.append(cmd)
            else:
                return real_run(*cmd, **kwargs)
        with patch.object(install, 'run', side_effect=run):
            install.receiver(self.args)
            unit = install.UNIT.read_text()
            self.assertIn('DynamicUser=yes', unit)
            self.assertIn('Restart=always', unit)
            self.assertNotIn('SupplementaryGroups', unit)
            self.assertNotIn('publisher.key', unit)
            self.assertEqual(install.CONFIG.stat().st_mode & 0o777, 0o644)
            self.assertEqual(install.CONFIG.with_name('publisher.pub').read_bytes(), Path(self.args.public_key).read_bytes())
            self.assertIn(('systemctl', 'enable', '--now', 'edgelab-receiver.service'), commands)
            with self.assertRaisesRegex(ValueError, 'refusing overwrite'):
                install.receiver(self.args)
            self.args.action = 'run'
            with patch.object(install.os, 'execv', side_effect=RuntimeError('exec')) as execute:
                with self.assertRaisesRegex(RuntimeError, 'exec'):
                    install.receiver(self.args)
                argv = execute.call_args.args[1]
                self.assertIn('https://hub.example.net/releases/desired.json', argv)
                self.assertIn('/var/lib/edgelab-receiver', argv)
                self.assertNotIn('--docker-load', argv)
                self.assertNotIn('--allow-http', argv)
            cfg = json.loads(install.CONFIG.read_text())
            cfg.update(allow_http=True, docker_load=True)
            install.CONFIG.write_text(json.dumps(cfg))
            with patch.object(install.os, 'execv', side_effect=RuntimeError('exec')) as execute:
                with self.assertRaisesRegex(RuntimeError, 'exec'):
                    install.receiver(self.args)
                self.assertIn('--allow-http', execute.call_args.args[1])
                self.assertIn('--docker-load', execute.call_args.args[1])
            for action in ['start', 'status', 'stop', 'restart', 'uninstall']:
                self.args.action = action
                install.receiver(self.args)
            self.assertFalse(install.UNIT.exists())
            self.assertTrue(install.CONFIG.exists())
            self.assertTrue(install.CONFIG.with_name('publisher.pub').exists())
            self.assertFalse(install.BINARY.exists())

    def test_explicit_docker_group(self):
        self.assertIn('SupplementaryGroups=docker', install.service(True))

    @patch.object(install, 'require')
    def test_http_registry_needs_explicit_opt_in(self, *_):
        args = argparse.Namespace(action='setup', directory=str(self.root / 'hub'), registry='http://registry.test')
        with patch.object(install, 'run'), self.assertRaisesRegex(ValueError, 'allow-registry-http'):
            install.hub(args)
        self.assertFalse(Path(args.directory).exists())

    @patch.object(install, 'require')
    @patch.object(install.os, 'geteuid', return_value=0)
    @patch.object(install.platform, 'system', return_value='Linux')
    def test_old_systemd_rejects_mtls_before_writes(self, *_):
        for field in ('hub_ca', 'hub_client_cert', 'hub_client_key'):
            path = self.root / field
            path.write_text('test-only')
            setattr(self.args, field, str(path))
        with patch.object(install, 'run', return_value=subprocess.CompletedProcess([], 0, stdout='systemd 246\n')), self.assertRaisesRegex(ValueError, '247'):
            install.receiver(self.args)
        self.assertFalse(install.CONFIG.exists())
        self.assertFalse(install.BINARY.exists())

    @patch.object(install, 'require')
    @patch.object(install.os, 'geteuid', return_value=0)
    @patch.object(install.platform, 'system', return_value='Linux')
    def test_mtls_private_systemd_credentials_and_retained_reinstall(self, *_):
        # Certificate contents are opaque to installer; real X.509 tested by lifecycle_test/mtls_smoke.
        for field in ('hub_ca', 'hub_client_cert', 'hub_client_key'):
            path = self.root / field
            path.write_text('test credential ' + field)
            setattr(self.args, field, str(path))
        real_run = install.run
        def run(*cmd, **kwargs):
            if str(cmd[0]) == 'systemctl':
                return subprocess.CompletedProcess(cmd, 0, stdout='systemd 252\n')
            return real_run(*cmd, **kwargs)
        with patch.object(install, 'run', side_effect=run):
            install.receiver(self.args)
            self.assertIn('LoadCredential=client.key:', install.UNIT.read_text())
            key = install.CONFIG.parent / 'tls/client.key'
            original = key.read_bytes()
            self.assertEqual(key.stat().st_mode & 0o777, 0o600)
            self.assertEqual(key.parent.stat().st_mode & 0o777, 0o700)
            self.args.action = 'run'
            with patch.dict(os.environ, CREDENTIALS_DIRECTORY='/run/credentials/test'), patch.object(install.os, 'execv', side_effect=RuntimeError('exec')) as execute:
                with self.assertRaises(RuntimeError):
                    install.receiver(self.args)
                argv = execute.call_args.args[1]
                self.assertIn('--hub-client-key', argv)
                self.assertIn('/run/credentials/test/client.key', argv)
                self.assertNotIn('test credential', repr(argv))
            self.args.action = 'uninstall'
            install.receiver(self.args)
            self.args.action = 'setup'
            self.args.hub = 'https://changed-but-not-applied.test'
            install.receiver(self.args)
            self.assertEqual(key.read_bytes(), original)
            self.assertEqual(json.loads(install.CONFIG.read_text())['hub'], 'https://hub.example.net')

    @patch.object(install, 'require')
    @patch.object(install.os, 'geteuid', return_value=0)
    @patch.object(install.platform, 'system', return_value='Linux')
    def test_partial_mtls_fails_before_writes(self, *_):
        self.args.hub_ca = 'a.pem'
        with patch.object(install, 'run'), self.assertRaisesRegex(ValueError, 'together'):
            install.receiver(self.args)
        self.assertFalse(install.CONFIG.exists())
        self.assertFalse(install.BINARY.exists())

    @patch.object(install, 'require')
    @patch.object(install.os, 'geteuid', return_value=0)
    @patch.object(install.platform, 'system', return_value='Linux')
    def test_missing_systemd_fails_before_writes(self, *_):
        with patch.object(install, 'run', side_effect=subprocess.CalledProcessError(1, ['systemctl'])):
            with self.assertRaises(subprocess.CalledProcessError):
                install.receiver(self.args)
        self.assertFalse(install.CONFIG.exists())
        self.assertFalse(install.UNIT.exists())
        self.assertFalse(install.BINARY.exists())

    def test_released_hub_security_and_persistent_credentials(self):
        password = self.root / 'password'
        password.write_text('secret-$-not-argv\n')
        args = argparse.Namespace(action='setup', directory=str(self.root / 'hub'),
            registry='https://registry.example.net', repository='team/app', allow='^v.*$',
            username='alice', password_file=str(password), port=18080)
        calls = []
        def run(*cmd, **kwargs):
            calls.append((cmd, kwargs))
        with patch.object(install, 'require'), patch.object(install, 'run', side_effect=run):
            install.hub(args)
            directory = Path(args.directory)
            compose = json.loads((directory / 'compose.yaml').read_text())
            for svc in compose['services'].values():
                self.assertEqual(svc['build']['context'], './runtime')
                self.assertEqual(svc['cap_drop'], ['ALL'])
                self.assertTrue(svc['read_only'])
                self.assertEqual(svc['security_opt'], ['no-new-privileges:true'])
            hub = compose['services']['hub']
            self.assertEqual(hub['ports'], ['127.0.0.1:18080:8080'])
            self.assertFalse(any('key' in m['source'] for m in hub['volumes']))
            self.assertNotIn('environment', hub)
            self.assertEqual({p.name for p in (directory / 'runtime').iterdir()}, {'Dockerfile', 'edgelab'})
            self.assertNotIn('golang', (directory / 'runtime/Dockerfile').read_text())
            self.assertEqual((directory / 'registry-password').stat().st_mode & 0o777, 0o600)
            self.assertEqual(calls[-1][1]['env']['REGISTRY_PASSWORD'], 'secret-$-not-argv')
            self.assertNotIn('secret', repr(calls[-1][0]))
            for action in ['start', 'status', 'stop', 'restart', 'uninstall']:
                args.action = action
                install.hub(args)
            self.assertTrue((directory / 'keys/publisher.key').exists())
            if shutil.which('docker'):
                env = dict(os.environ, REGISTRY_PASSWORD='test')
                subprocess.run(['docker', 'compose', '-f', str(directory / 'compose.yaml'), 'config', '--quiet'], env=env, check=True)


if __name__ == '__main__':
    unittest.main()

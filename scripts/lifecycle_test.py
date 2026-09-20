"""Make/env entry points and real OpenSSL enrollment contracts (no Docker mutation)."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
import lifecycle
import mtls

ROOT = Path(__file__).resolve().parents[1]


class LifecycleTest(unittest.TestCase):
    def test_env_is_data_and_boolean_is_strict(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'env'
            path.write_text('# comment\nALLOW=^v.*$\nTOKEN=$(touch should-not-exist)\nA="some spaces"\n')
            values = lifecycle.environment(path)
            self.assertEqual(values['ALLOW'], '^v.*$')
            self.assertEqual(values['TOKEN'], '$(touch should-not-exist)')
            self.assertEqual(values['A'], 'some spaces')
            self.assertFalse(lifecycle.boolean({}, 'ALLOW_HTTP'))
            with self.assertRaises(ValueError):
                lifecycle.boolean({'ALLOW_HTTP': 'yes'}, 'ALLOW_HTTP')
            path.write_text('not env')
            with self.assertRaises(ValueError):
                lifecycle.environment(path)

    def test_make_contract(self):
        default = subprocess.run(['make', '-n'], cwd=ROOT,
                                 capture_output=True, text=True, check=True)
        self.assertIn('go build', default.stdout)
        self.assertNotIn('lifecycle.py', default.stdout)
        for role in ('hub', 'receiver'):
            for action in ('up', 'status', 'restart', 'stop', 'uninstall'):
                result = subprocess.run(['make', '-n', f'{role}-{action}'], cwd=ROOT,
                                        capture_output=True, text=True, check=True)
                self.assertIn(f'scripts/lifecycle.py {role} {action}', result.stdout)
                self.assertNotIn('go build', result.stdout)
        for action in ('init', 'up', 'down', 'client'):
            result = subprocess.run(['make', '-n', 'mtls-' + action, 'CLIENT=edge-01'], cwd=ROOT,
                                    capture_output=True, text=True, check=True)
            self.assertIn(f'scripts/mtls.py {action}', result.stdout)

    def test_hub_up_reuses_existing_install_without_build_or_config_rewrite(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / 'compose.yaml').write_text('{}')
            env = root / 'hub.env'
            env.write_text(f'HUB_DIRECTORY={root}\n')
            with patch.object(sys, 'argv', ['lifecycle', 'hub', 'up', '--env-file', str(env)]), patch.object(lifecycle.install, 'hub') as hub, patch.object(lifecycle.subprocess, 'run') as run:
                lifecycle.main()
                self.assertEqual(hub.call_args.args[0].action, 'start')
                run.assert_not_called()

    def test_example_home_paths_expand_only_at_directory_use(self):
        with tempfile.TemporaryDirectory() as tmp:
            home = Path(tmp).resolve()
            env = home / 'hub.env'
            env.write_text((ROOT / 'hub.env.example').read_text() +
                           '\nREGISTRY_USERNAME=$USER\nREGISTRY_PASSWORD_FILE=$(not-a-command)\n')
            values = lifecycle.environment(env)
            self.assertEqual(values['HUB_DIRECTORY'], '~/edgelab-hub')
            self.assertEqual(values['MTLS_DIRECTORY'], '~/edgelab-mtls')
            (home / 'edgelab').touch()  # emulate a bundled binary; no build
            with patch.dict(os.environ, {'HOME': tmp}), patch.object(lifecycle, 'ROOT', home), patch.object(sys, 'argv', ['lifecycle', 'hub', 'up', '--env-file', str(env)]), patch.object(lifecycle.install, 'hub') as hub:
                lifecycle.main()
                opts = hub.call_args.args[0]
                self.assertEqual(opts.directory, str(home / 'edgelab-hub'))
                self.assertEqual(opts.username, '$USER')
                self.assertEqual(opts.password_file, '$(not-a-command)')
                self.assertEqual(opts.action, 'setup')
            with patch.dict(os.environ, {'HOME': tmp}), patch.object(sys, 'argv', ['mtls', 'init', '--env-file', str(env)]), patch.object(mtls, 'initialize') as initialize:
                mtls.main()
                self.assertEqual(initialize.call_args.args[0], home / 'edgelab-mtls')
            self.assertFalse((home / 'edgelab-hub').exists())
            self.assertFalse((home / 'edgelab-mtls').exists())

    def test_source_build_is_static_for_alpine_runtime(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / 'go.mod').write_text('module proof\n')
            env = root / 'hub.env'
            env.write_text(f'HUB_DIRECTORY={root / "hub"}\nREGISTRY=https://registry.test\nREPOSITORY=app\nALLOW=.*\n')
            with patch.object(lifecycle, 'ROOT', root), patch.object(lifecycle.platform, 'system', return_value='Linux'), patch.object(lifecycle.install, 'require'), patch.object(sys, 'argv', ['lifecycle', 'hub', 'up', '--env-file', str(env)]), patch.object(lifecycle.install, 'hub') as hub, patch.object(lifecycle.subprocess, 'run') as run:
                lifecycle.main()
                self.assertEqual(run.call_args.kwargs['env']['CGO_ENABLED'], '0')
                self.assertEqual(hub.call_args.args[0].action, 'setup')


class EnrollmentTest(unittest.TestCase):
    def test_distinct_authorities_permissions_eku_and_client_only_distribution(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            env = root / 'hub.env'
            directory = root / 'tls'
            env.write_text(f'MTLS_DIRECTORY={directory}\nMTLS_HOST=hub.example.test\nMTLS_PORT=8443\nHUB_PORT=8080\n')
            def command(action, *args, check=True):
                return subprocess.run([sys.executable, str(ROOT / 'scripts/mtls.py'), action,
                                       '--env-file', str(env), *args], capture_output=True, text=True, check=check)
            command('init')
            self.assertNotEqual(command('init', check=False).returncode, 0)
            command('client', '--client', 'edge-01')
            self.assertNotEqual(command('client', '--client', '../escape', check=False).returncode, 0)
            self.assertNotEqual(command('client', '--client', 'edge-01', check=False).returncode, 0)
            files = directory / 'clients/edge-01'
            self.assertEqual({p.name for p in files.iterdir()}, {'client.pem', 'client.key', 'hub-ca.pem'})
            for key in directory.rglob('*.key'):
                self.assertEqual(key.stat().st_mode & 0o777, 0o600)
            for purpose, authority, cert in [('sslserver', 'server-ca', directory / 'runtime/server.pem'),
                                              ('sslclient', 'client-ca', files / 'client.pem')]:
                result = subprocess.run(['openssl', 'verify', '-purpose', purpose, '-CAfile', str(directory / 'authority' / (authority + '.pem')), str(cert)], capture_output=True)
                self.assertEqual(result.returncode, 0, result.stderr)
            wrong = subprocess.run(['openssl', 'verify', '-purpose', 'sslclient', '-CAfile', str(directory / 'authority/server-ca.pem'), str(files / 'client.pem')], capture_output=True)
            self.assertNotEqual(wrong.returncode, 0)
            config = json.loads((directory / 'compose.yaml').read_text())['services']['proxy']
            self.assertEqual(config['network_mode'], 'host')
            self.assertEqual([m['source'] for m in config['volumes']], ['./runtime'])
            caddy = (directory / 'runtime/Caddyfile').read_text()
            self.assertIn('require_and_verify', caddy)
            self.assertIn('trust_pool file', caddy)
            self.assertIn('reverse_proxy 127.0.0.1:8080', caddy)


if __name__ == '__main__':
    unittest.main()

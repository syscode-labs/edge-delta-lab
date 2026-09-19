#!/usr/bin/env python3
"""Publication contracts; JSON-form YAML needs no optional PyYAML dependency.

Shell policy is exercised locally, not merely matched as text. This is not a
GitHub Actions execution or proof of registry upload / multi-architecture runtime.
"""
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
TEXT = (ROOT / '.github/workflows/release.yml').read_text()
WORKFLOW = json.loads('\n'.join(line for line in TEXT.splitlines()
                                if not line.lstrip().startswith('#')))


def action(job, name):
    return next(step for step in WORKFLOW['jobs'][job]['steps']
                if step.get('uses', '').startswith(name + '@'))


def shell(script, **env):
    with tempfile.TemporaryDirectory() as directory:
        output = Path(directory) / 'output'
        result = subprocess.run(['bash', '-c', script], cwd=directory,
                                env=dict(os.environ, GITHUB_OUTPUT=str(output), **env),
                                capture_output=True, text=True)
        return result.returncode, output.read_text() if output.exists() else ''


class ReleaseWorkflowTest(unittest.TestCase):
    def test_tag_only_trigger(self):
        self.assertEqual(WORKFLOW['on'], {'push': {'tags': ['v*']}})
        self.assertFalse(WORKFLOW['concurrency']['cancel-in-progress'])

    def test_least_permissions_and_token_only(self):
        self.assertEqual(WORKFLOW['permissions'], {})
        expected = {'validate': {'contents': 'read'},
                    'github-release': {'contents': 'write'},
                    'containers': {'contents': 'read', 'packages': 'write'}}
        self.assertEqual(set(WORKFLOW['jobs']), set(expected))
        for name, permissions in expected.items():
            self.assertEqual(WORKFLOW['jobs'][name]['permissions'], permissions)
        self.assertEqual(set(re.findall(r'secrets\.([A-Za-z_]+)', TEXT)), {'GITHUB_TOKEN'})
        for job in WORKFLOW['jobs'].values():
            for step in job['steps']:
                if 'uses' in step:
                    self.assertRegex(step['uses'], r'^[\w/-]+@[0-9a-f]{40}$')
        for name in ('validate', 'containers'):
            self.assertEqual(action(name, 'actions/checkout')['with'],
                             {'ref': '${{ github.sha }}', 'persist-credentials': False})

    def test_validation_precedes_publication_and_shared_packaging(self):
        steps = WORKFLOW['jobs']['validate']['steps']
        runs = [step['run'] for step in steps if 'run' in step]
        self.assertEqual(runs[1:], ['make -j1 check', 'make openspec-check',
                                  'make release VERSION="${tag}"'])
        package = next(s for s in steps if s.get('run') == runs[-1])
        self.assertEqual(package['env']['tag'], '${{ steps.identity.outputs.tag }}')
        self.assertEqual(steps[-1], action('validate', 'actions/upload-artifact'))
        for job in ('github-release', 'containers'):
            self.assertEqual(WORKFLOW['jobs'][job]['needs'], 'validate')

    def test_identity_policy_executed(self):
        identity = WORKFLOW['jobs']['validate']['steps'][0]
        self.assertEqual(identity['env'], {'TAG': '${{ github.ref_name }}',
                         'REF_TYPE': '${{ github.ref_type }}',
                         'REPOSITORY': '${{ github.repository }}'})
        for tag in ('v0.1.0', 'v10.20.30'):
            code, output = shell(identity['run'], TAG=tag, REF_TYPE='tag', REPOSITORY='Owner/Repo')
            self.assertEqual(code, 0)
            self.assertEqual(output, f'tag={tag}\nstable=true\nrepository=owner/repo\n')
        # The unchanged local packager only accepts stable semver. Fail closed.
        for tag in ('v1.0.0-rc.1', 'v1.0.0+build.1', 'v01.0.0', 'v1.2',
                    'v1.2.3.4', 'main', 'v1.2.3\nmalicious=true', 'v1;exit 0'):
            code, output = shell(identity['run'], TAG=tag, REF_TYPE='tag', REPOSITORY='Owner/Repo')
            self.assertNotEqual(code, 0, tag)
            self.assertEqual(output, '')
        code, _ = shell(identity['run'], TAG='v1.2.3', REF_TYPE='branch', REPOSITORY='Owner/Repo')
        self.assertNotEqual(code, 0)

    def test_images_and_stable_only_latest_executed(self):
        job = WORKFLOW['jobs']['containers']
        self.assertEqual(job['strategy']['matrix']['include'],
                         [{'target': 'daemon', 'image': 'edgelab'},
                          {'target': 'client', 'image': 'edgelab-client'}])
        build = action('containers', 'docker/build-push-action')['with']
        self.assertEqual(build['target'], '${{ matrix.target }}')
        self.assertEqual(build['platforms'], 'linux/amd64,linux/arm64')
        self.assertEqual(build['context'], '.')
        self.assertEqual(build['file'], 'Dockerfile')
        self.assertIs(build['push'], True)
        self.assertEqual(build['tags'], '${{ steps.tags.outputs.tags }}')
        tags = next(s for s in job['steps'] if s.get('id') == 'tags')
        self.assertEqual(tags['env'], {
            'IMAGE': 'ghcr.io/${{ needs.validate.outputs.repository }}/${{ matrix.image }}',
            'TAG': '${{ needs.validate.outputs.tag }}',
            'STABLE': '${{ needs.validate.outputs.stable }}'})
        for image in ('edgelab', 'edgelab-client'):
            image = f'ghcr.io/owner/repo/{image}'
            for tag, stable, latest in [('v1.2.3', 'true', True),
                                        ('v1.2.3-rc.1', 'false', False),
                                        ('v1.2.3-rc.1', 'true', False),
                                        ('v1.2.3+build.1', 'true', False),
                                        ('v1.2.3', 'false', False)]:
                code, output = shell(tags['run'], IMAGE=image, TAG=tag, STABLE=stable)
                self.assertEqual(code, 0)
                self.assertEqual(output, f'tags<<EOF\n{image}:{tag}\n' +
                                 (f'{image}:latest\n' if latest else '') + 'EOF\n')

    def test_exact_release_assets(self):
        upload = action('validate', 'actions/upload-artifact')['with']
        download = action('github-release', 'actions/download-artifact')['with']
        self.assertEqual(upload['path'], 'dist/')
        self.assertEqual(upload['if-no-files-found'], 'error')
        self.assertEqual(download, {'name': upload['name'], 'path': 'dist/'})
        step = WORKFLOW['jobs']['github-release']['steps'][-1]
        self.assertEqual(step['env'], {'GH_TOKEN': '${{ secrets.GITHUB_TOKEN }}',
                         'GH_REPO': '${{ github.repository }}',
                         'TAG': '${{ needs.validate.outputs.tag }}'})
        self.assertIn('gh release create "$TAG" --verify-tag', step['run'])
        self.assertIn('gh release upload "$TAG" dist/* --clobber', step['run'])
        self.assertIn('sha256sum -c SHA256SUMS', step['run'])
        self.assertNotIn('--target', step['run'])


if __name__ == '__main__':
    unittest.main()

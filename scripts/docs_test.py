"""Current Markdown links/anchors and narrowly preserved historical references."""
from collections import Counter
from pathlib import Path
import re
import subprocess
import unittest
from urllib.parse import unquote, urlsplit

ROOT = Path(__file__).resolve().parents[1]
# Immutable evidence is not rewritten to follow current documentation moves.
# TESTING.md links the complete pre-consolidation tree where these resolve.
HISTORICAL_LINKS = {
    ('evidence/intended-install/packaged-linux/README.md', '../../../deploy/compose/README.md'),
    ('evidence/revision-3/README.md', '../../docs/EXPERIMENTS.md#linux-first-daemon-acceptance'),
}


def prose(text):
    return re.sub(r'```.*?```', '', text, flags=re.S)


def anchors(text):
    text = prose(text)
    found = set(re.findall(r'<a\s+(?:id|name)=[\"\']([^\"\']+)', text))
    counts = Counter()
    for heading in re.findall(r'^#{1,6}\s+(.+?)\s*#*$', text, re.M):
        heading = re.sub(r'\[([^]]+)\]\([^)]*\)', r'\1', heading)
        slug = re.sub(r'[^\w\- ]', '', heading.lower()).replace(' ', '-')
        number = counts[slug]
        counts[slug] += 1
        found.add(slug + (f'-{number}' if number else ''))
    return found


class DocumentationLinksTest(unittest.TestCase):
    def test_all_tracked_markdown_local_targets_and_anchors(self):
        names = subprocess.check_output(
            ['git', 'ls-files', '--cached', '--others', '--exclude-standard', '*.md'],
            cwd=ROOT, text=True).splitlines()
        historical_seen = set()
        for name in sorted(set(names)):
            source = ROOT / name
            if not source.is_file():  # A tracked deletion in a working tree.
                continue
            text = prose(source.read_text())
            targets = re.findall(r'\]\(([^)]+)\)', text)
            targets += re.findall(r'^\s*\[[^]]+\]:\s*(\S+)', text, re.M)
            for target in targets:
                with self.subTest(source=name, target=target):
                    if (name, target) in HISTORICAL_LINKS:
                        historical_seen.add((name, target))
                        continue
                    parsed = urlsplit(target.strip('<>'))
                    if parsed.scheme or parsed.netloc:
                        continue
                    destination = (source.parent / unquote(parsed.path)).resolve() if parsed.path else source
                    self.assertTrue(destination.exists(), 'missing local target')
                    if parsed.fragment and destination.suffix == '.md':
                        self.assertIn(unquote(parsed.fragment), anchors(destination.read_text()))
        self.assertEqual(historical_seen, HISTORICAL_LINKS)
        testing = (ROOT / 'TESTING.md').read_text()
        for name, _ in HISTORICAL_LINKS:
            self.assertIn('blob/811b082eeaec9deeefce4bd85d942dff5763257e/' + name, testing)

    def test_current_install_pages_do_not_advertise_old_or_pending_release(self):
        for name in ('README.md', 'OPERATIONS.md', 'MTLS.md', 'docs/DEVELOPMENT.md'):
            text = (ROOT / name).read_text()
            self.assertNotIn('v0.1.1', text, name)
            self.assertNotRegex(text, r'(?i)(?:pending publication|unpublished|pending.*v0\.1\.2)')
        self.assertNotIn('## Lifecycle', (ROOT / 'README.md').read_text())
        self.assertNotIn('make demo', (ROOT / 'README.md').read_text())

    def test_anchor_normalization(self):
        self.assertEqual(anchors('# Hello, **world**!\n## Hello, **world**!\n<a id="old"></a>'),
                         {'hello-world', 'hello-world-1', 'old'})


if __name__ == '__main__':
    unittest.main()

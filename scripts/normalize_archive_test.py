import gzip
import hashlib
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from normalize_archive import normalize


class NormalizeTest(unittest.TestCase):
    def make_archive(self, directory: Path, compressed: bool = False, bad_hash: bool = False):
        inner = io.BytesIO()
        with tarfile.open(fileobj=inner, mode="w") as archive:
            body = b"fixture content\n" * 1024
            entry = tarfile.TarInfo("example.txt")
            entry.mode, entry.size = 0o640, len(body)
            archive.addfile(entry, io.BytesIO(body))
        raw_layer = inner.getvalue()
        digest = "0" * 64 if bad_hash else hashlib.sha256(raw_layer).hexdigest()
        config = json.dumps({"architecture": "amd64", "os": "linux", "rootfs": {"type": "layers", "diff_ids": ["sha256:" + digest]}}).encode()
        image_id = hashlib.sha256(config).hexdigest()
        manifest = [{"Config": "config.json", "RepoTags": ["fixture:v1"], "Layers": ["source-layer"]}]
        path = directory / "source.tar"
        with tarfile.open(path, "w") as archive:
            for name, body in [("source-layer", gzip.compress(raw_layer, mtime=0) if compressed else raw_layer), ("config.json", config), ("manifest.json", json.dumps(manifest).encode())]:
                entry = tarfile.TarInfo(name)
                entry.size = len(body)
                archive.addfile(entry, io.BytesIO(body))
        return path, raw_layer, image_id

    def check_round_trip(self, compressed):
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            source, raw, image_id = self.make_archive(directory, compressed)
            output = directory / "normalized.tar"
            self.assertEqual(normalize(source, output), ["sha256:" + image_id])
            with tarfile.open(output) as archive:
                manifest = json.load(archive.extractfile("manifest.json"))
                self.assertEqual(archive.extractfile(manifest[0]["Layers"][0]).read(), raw)
                config = archive.extractfile(manifest[0]["Config"]).read()
                self.assertEqual(hashlib.sha256(config).hexdigest(), image_id)

    def test_raw_layers_preserve_exact_bytes(self):
        self.check_round_trip(False)

    def test_gzip_layers_preserve_exact_uncompressed_bytes(self):
        self.check_round_trip(True)

    def test_wrong_diffid_is_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            source, _, _ = self.make_archive(directory, bad_hash=True)
            with self.assertRaisesRegex(ValueError, "DiffID mismatch"):
                normalize(source, directory / "out.tar")

    def test_normalization_is_repeatable(self):
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            source, _, _ = self.make_archive(directory, compressed=True)
            one, two = directory / "one.tar", directory / "two.tar"
            normalize(source, one)
            normalize(source, two)
            self.assertEqual(one.read_bytes(), two.read_bytes())

    def test_duplicate_diffid_does_not_skip_different_corrupt_member(self):
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            source, raw, _ = self.make_archive(directory)
            diff_id = 'sha256:' + hashlib.sha256(raw).hexdigest()
            config = json.dumps({'rootfs': {'type': 'layers', 'diff_ids': [diff_id, diff_id]}}).encode()
            manifest = [{'Config': 'config.json', 'Layers': ['good', 'bad']}]
            with tarfile.open(source, 'w') as archive:
                for name, body in [('good', raw), ('bad', b'corrupted second member'), ('config.json', config), ('manifest.json', json.dumps(manifest).encode())]:
                    entry = tarfile.TarInfo(name); entry.size = len(body)
                    archive.addfile(entry, io.BytesIO(body))
            with self.assertRaisesRegex(ValueError, 'DiffID mismatch'):
                normalize(source, directory / 'out.tar')
            self.assertFalse((directory / 'out.tar').exists())


if __name__ == "__main__":
    unittest.main()

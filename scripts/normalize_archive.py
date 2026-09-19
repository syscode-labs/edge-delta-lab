#!/usr/bin/env python3
"""Normalize a docker-save archive to uncompressed legacy Docker layer members.

No layer filesystem is extracted or re-tarred. Its exact uncompressed tar bytes
are preserved, and checked against config.rootfs.diff_ids. gzip and already-raw
members are supported. Zstandard-only/OCI-only exports fail explicitly.
"""
from __future__ import annotations
import argparse
import gzip
import hashlib
import io
import json
from pathlib import Path
import tarfile
import tempfile


def normalize(source: Path, target: Path, max_layer_bytes: int = 8 << 30) -> list[str]:
    """Return preserved config SHA256 IDs, never source OCI index/manifest IDs."""
    if source.resolve() == target.resolve():
        raise ValueError("input and output must differ")
    target.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="normalize-", dir=target.parent) as temp:
        tempdir = Path(temp)
        with tarfile.open(source, "r:*") as archive:
            def member_bytes(name: str, limit: int = 16 << 20) -> bytes:
                member = archive.getmember(name)
                if not member.isfile() or member.size > limit:
                    raise ValueError(f"invalid/oversized metadata member: {name}")
                with archive.extractfile(member) as stream:
                    result = stream.read(limit + 1)
                if len(result) > limit:
                    raise ValueError("metadata exceeds limit")
                return result

            try:
                manifest = json.loads(member_bytes("manifest.json"))
            except KeyError as exc:
                raise ValueError("OCI-only export: use skopeo copy to docker-archive first") from exc
            layers: dict[str, Path] = {}
            verified_members = set()
            configs: dict[str, bytes] = {}
            output_manifest = []
            ids = []
            for image in manifest:
                config_bytes = member_bytes(image["Config"])
                config = json.loads(config_bytes)
                config_hash = hashlib.sha256(config_bytes).hexdigest()
                ids.append("sha256:" + config_hash)
                configs[config_hash + ".json"] = config_bytes
                diff_ids = config["rootfs"]["diff_ids"]
                if len(diff_ids) != len(image["Layers"]):
                    raise ValueError("layer count does not match config")
                names = []
                for member_name, expected in zip(image["Layers"], diff_ids):
                    algorithm, digest = expected.split(":", 1)
                    if algorithm != "sha256" or len(digest) != 64 or any(c not in "0123456789abcdef" for c in digest):
                        raise ValueError("unsupported/invalid layer identity")
                    name = digest + "/layer.tar"
                    names.append(name)
                    # Deduplicate output by DiffID, but validate every distinct
                    # source member even when two entries claim the same DiffID.
                    if (member_name, expected) in verified_members:
                        continue
                    member = archive.getmember(member_name)
                    if not member.isfile():
                        raise ValueError("layer must be a regular archive member")
                    raw = archive.extractfile(member)
                    buffered = io.BufferedReader(raw)
                    magic = buffered.peek(4)[:4]
                    if magic == b"\x28\xb5\x2f\xfd":
                        buffered.close()
                        raise ValueError("zstd member: export decompressed with Skopeo; this helper supports raw/gzip")
                    stream = gzip.GzipFile(fileobj=buffered) if magic.startswith(b"\x1f\x8b") else buffered
                    path = tempdir / digest
                    count = 0
                    hasher = hashlib.sha256()
                    try:
                        with path.open("wb") as out:
                            while True:
                                block = stream.read(1 << 20)
                                if not block:
                                    break
                                count += len(block)
                                if count > max_layer_bytes:
                                    raise ValueError("decompressed layer exceeds configured limit")
                                out.write(block)
                                hasher.update(block)
                    finally:
                        stream.close()
                        buffered.close()
                    if hasher.hexdigest() != digest:
                        raise ValueError(f"layer DiffID mismatch: {member_name}")
                    verified_members.add((member_name, expected))
                    layers[name] = path
                output_manifest.append({"Config": config_hash + ".json", "RepoTags": image.get("RepoTags", []), "Layers": names})

        temporary_output = tempdir / "normalized.tar"
        with tarfile.open(temporary_output, "w", format=tarfile.PAX_FORMAT) as out:
            for name, path in sorted(layers.items()):
                entry = tarfile.TarInfo(name)
                entry.size, entry.mode = path.stat().st_size, 0o644
                with path.open("rb") as stream:
                    out.addfile(entry, stream)
            for name, body in sorted(configs.items()):
                entry = tarfile.TarInfo(name)
                entry.size, entry.mode = len(body), 0o644
                out.addfile(entry, io.BytesIO(body))
            body = json.dumps(output_manifest, separators=(",", ":"), sort_keys=True).encode()
            entry = tarfile.TarInfo("manifest.json")
            entry.size, entry.mode = len(body), 0o644
            out.addfile(entry, io.BytesIO(body))
        temporary_output.replace(target)
    return ids


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source", type=Path)
    parser.add_argument("target", type=Path)
    args = parser.parse_args()
    print(json.dumps({"image_ids": normalize(args.source, args.target)}))

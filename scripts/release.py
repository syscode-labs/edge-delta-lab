#!/usr/bin/env python3
"""Build local release archives. Requires Go, Helm and Python's standard library."""
import gzip
import hashlib
import io
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile

PLATFORMS = ("linux/amd64", "linux/arm64", "darwin/arm64")
BINARIES = ("edgelab", "edgelab-exporter")
INSTALL_FILES = {
    "install": ("scripts/install.sh", 0o755),
    "scripts/install.py": ("scripts/install.py", 0o755),
    "deploy/compose/setup.py": ("deploy/compose/setup.py", 0o644),
    "deploy/compose/compose.yaml": ("deploy/compose/compose.yaml", 0o644),
    "deploy/compose/Dockerfile.runtime": ("deploy/compose/Dockerfile.runtime", 0o644),
}


def archive(path, entries, epoch):
    """Write sorted regular files with no host paths, owners or wall-clock times."""
    with path.open("wb") as raw:
        with gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=epoch) as gz:
            with tarfile.open(fileobj=gz, mode="w", format=tarfile.USTAR_FORMAT) as tar:
                for name, data, mode in sorted(entries):
                    info = tarfile.TarInfo(name)
                    info.size, info.mode, info.mtime = len(data), mode, epoch
                    tar.addfile(info, io.BytesIO(data))


def release(root, version, epoch=0, go="go", helm="helm"):
    if not re.fullmatch(r"v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)", version):
        raise ValueError("VERSION must be a v-prefixed release version, e.g. v0.1.0")
    if not 0 <= epoch <= 0xFFFFFFFF:
        raise ValueError("SOURCE_DATE_EPOCH must be in 0..4294967295")
    chart = root / "deploy/helm/edgelab-hub"
    metadata = subprocess.run([helm, "show", "chart", str(chart)], check=True,
                              text=True, capture_output=True).stdout
    for key, expected in (("name", "edgelab-hub"), ("version", version[1:]),
                          ("appVersion", version)):
        values = re.findall(rf"^{key}:\s*(.*?)\s*$", metadata, re.MULTILINE)
        if len(values) != 1 or values[0].strip("\"'") != expected:
            raise ValueError(f"Chart.yaml {key} must equal {expected}")

    # Validate before clearing any previous output. A failed build has no checksum manifest.
    dist = root / "dist"
    if dist.is_symlink():
        raise ValueError("dist must not be a symlink")
    if dist.exists():
        shutil.rmtree(dist)
    dist.mkdir()
    with tempfile.TemporaryDirectory(prefix="edgelab-release-") as work:
        work = Path(work)
        for platform in PLATFORMS:
            system, arch = platform.split("/")
            env = dict(os.environ, CGO_ENABLED="0", GOOS=system, GOARCH=arch,
                       GOAMD64="v1", GOARM64="v8.0", GOFLAGS="", GOEXPERIMENT="")
            entries = []
            for binary in BINARIES:
                output = work / binary
                subprocess.run([go, "build", "-trimpath", "-buildvcs=false",
                                "-ldflags=-s -w -buildid=", "-o", str(output),
                                f"./cmd/{binary}"], cwd=root, env=env, check=True)
                entries.append((binary, output.read_bytes(), 0o755))
            if system == "linux":
                entries.extend((name, (root / source).read_bytes(), mode)
                               for name, (source, mode) in INSTALL_FILES.items())
            archive(dist / f"edgelab-{version}-{system}-{arch}.tar.gz", entries, epoch)
        subprocess.run([helm, "package", str(chart), "--destination", str(work)], check=True)
        chart_name = f"edgelab-hub-{version[1:]}.tgz"
        # Helm applies .helmignore and serializes chart metadata; normalize its timestamps.
        with tarfile.open(work / chart_name, "r:gz") as tar:
            entries = []
            for member in tar.getmembers():
                if not member.isfile():
                    raise ValueError(f"Unexpected chart archive member: {member.name}")
                entries.append((member.name, tar.extractfile(member).read(), 0o644))
        archive(dist / chart_name, entries, epoch)
    lines = []
    for path in sorted(dist.iterdir()):
        lines.append(f"{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}\n")
    (dist / "SHA256SUMS").write_text("".join(lines), encoding="ascii")
    print(f"Release {version} written to {dist}")


if __name__ == "__main__":
    try:
        release(Path(__file__).resolve().parents[1], os.environ.get("VERSION", ""),
                int(os.environ.get("SOURCE_DATE_EPOCH", "0")),
                os.environ.get("GO", "go"), os.environ.get("HELM", "helm"))
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        raise SystemExit(f"release: {error}")

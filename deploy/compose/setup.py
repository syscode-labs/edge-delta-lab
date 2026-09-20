#!/usr/bin/env python3
"""Create a fresh host-local publisher/hub installation; Python stdlib only."""
import argparse
import json
import os
from pathlib import Path
import shlex
import subprocess
import sys
from urllib.parse import urlsplit

CHECKOUT = Path(__file__).resolve().parents[2]
TEMPLATE = Path(__file__).with_name("compose.yaml")


def write_json(path, value, mode=0o640):
    # JSON is a YAML subset, preserving regexes and avoiding YAML scalar surprises.
    with path.open("x", encoding="utf-8") as stream:
        json.dump(value, stream, indent=2)
        stream.write("\n")
    path.chmod(mode)


def install(args):
    directory = Path(args.directory).expanduser()
    if not directory.is_absolute():
        raise ValueError("--directory must be an absolute path")
    if directory.exists() or directory.is_symlink():
        raise ValueError(f"refusing to overwrite existing path: {directory}")
    url = urlsplit(args.registry)
    if (url.scheme not in ("http", "https") or not url.hostname
            or url.username or url.password or url.query or url.fragment
            or url.path not in ("", "/")):
        raise ValueError("--registry must be an http(s) registry origin without credentials or a path")
    if not args.repository or any(c.isspace() for c in args.repository) or ":" in args.repository or "@" in args.repository or args.repository.startswith("/"):
        raise ValueError("--repository must be a repository name, without registry, tag or digest")
    if not args.allow:
        raise ValueError("--allow must be a nonempty Go/RE2 tag regex (use .* explicitly for all tags)")
    if not 1 <= args.port <= 65535:
        raise ValueError("--port must be between 1 and 65535")
    binary = Path(args.binary).expanduser().resolve()
    if not binary.is_file() or not os.access(binary, os.X_OK):
        raise ValueError(f"build the native binary first: go build -o {binary} ./cmd/edgelab")
    # Bind mounts are local to the Docker daemon. Host GID access avoids root,
    # chown, world-writable state, and world-readable signing keys on Linux.
    gid = os.getgid()
    directory.mkdir(mode=0o700, parents=True)
    for relative in ("origin", "origin/releases", "origin/objects", "watcher-state", "watcher-state/tmp", "hub-state"):
        path = directory / relative
        path.mkdir(mode=0o770)
        path.chmod(0o770)
    subprocess.run([str(binary), "keygen", "--out", str(directory / "keys")], check=True)
    (directory / "keys").chmod(0o700)
    (directory / "keys/publisher.key").chmod(0o640)
    (directory / "keys/publisher.pub").chmod(0o644)
    config = {
        "registry_url": args.registry.rstrip("/"), "poll": "30s",
        "repos": [{"name": args.repository, "allow": args.allow}],
        "state_file": "/state/watcher.json",
        "publish": {"root": "/origin", "key": "/keys/publisher.key",
                    "channel": "desired", "sequence_file": "/state/sequence"},
    }
    compose = json.loads(TEMPLATE.read_text())
    for service in compose["services"].values():
        # Compose interpolation must not reinterpret dollar signs in checkout paths.
        service["build"]["context"] = str(CHECKOUT).replace("$", "$$")
        service["user"] = f"100:{gid}"
    compose["services"]["hub"]["ports"] = [f"127.0.0.1:{args.port}:8080"]
    if args.username:
        config.update(username=args.username, password_env="REGISTRY_PASSWORD")
        compose["services"]["publisher"]["environment"]["REGISTRY_PASSWORD"] = "${REGISTRY_PASSWORD:?Export REGISTRY_PASSWORD for publisher registry authentication}"
    write_json(directory / "watcher.yaml", config)
    write_json(directory / "compose.yaml", compose)
    print("WARNING: hub uses unauthenticated HTTP, bound to loopback only. Do not expose it publicly.", file=sys.stderr)
    if url.scheme == "http":
        print("WARNING: registry HTTP is unencrypted; use a trusted isolated network only.", file=sys.stderr)
    command = f"docker compose -f {shlex.quote(str(directory / 'compose.yaml'))}"
    print(f"Created {directory}; containers run as UID 100, host GID {gid}.")
    print(f"Start: {command} up -d --build")
    print(f"Logs:  {command} logs -f publisher hub")
    print(f"Stop:  {command} down   # preserves all host state and keys")
    print(f"Trust: distribute only {directory / 'keys/publisher.pub'} to receivers")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--directory", required=True, help="new absolute installation directory on the Docker host")
    parser.add_argument("--registry", required=True, help="real registry origin, e.g. https://registry.example.net")
    parser.add_argument("--repository", required=True, help="one repository, e.g. team/app")
    parser.add_argument("--allow", required=True, help="Go/RE2 tag allow regex")
    parser.add_argument("--binary", default=str(CHECKOUT / "bin/edgelab"), help="built native edgelab binary")
    parser.add_argument("--username", help="registry username; export REGISTRY_PASSWORD when starting Compose")
    parser.add_argument("--port", type=int, default=8080, help="loopback hub port (default 8080)")
    args = parser.parse_args()
    try:
        install(args)
    except (OSError, ValueError, subprocess.CalledProcessError) as exc:
        parser.exit(1, f"setup: {exc}\n")


if __name__ == "__main__":
    main()

#!/bin/sh
set -eu
command -v python3 >/dev/null 2>&1 || { printf '%s\n' 'install: Python 3 is required (no Go/compiler needed)' >&2; exit 1; }
HERE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
exec python3 "$HERE/scripts/install.py" "$@"

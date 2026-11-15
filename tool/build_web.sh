#!/usr/bin/env bash
set -euo pipefail
export PYTHONDONTWRITEBYTECODE=1

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if ! command -v python3 >/dev/null 2>&1; then
  echo 'Web build requires Python 3.10+ on PATH.' >&2
  exit 1
fi
exec python3 "${ROOT_DIR}/tool/web/build.py" "$@"

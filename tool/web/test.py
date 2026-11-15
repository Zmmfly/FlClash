#!/usr/bin/env python3
"""Run Web staging, browser-bridge and compiled Dart VFS contract tests."""

import sys

sys.dont_write_bytecode = True
if sys.version_info < (3, 10):
    raise SystemExit('Python 3.10 or newer is required')

import shutil
import subprocess
import tempfile
from pathlib import Path

from stage import ROOT, stage


def run(arguments, cwd=ROOT):
    subprocess.run(arguments, cwd=cwd, check=True)


def main():
    for executable in ('flutter', 'dart', 'node'):
        if shutil.which(executable) is None:
            raise SystemExit(f'Missing test dependency on PATH: {executable}')
    run(['python3', '-m', 'unittest', 'discover', '-s', 'tool/web', '-p', 'test_*.py', '-v'])
    run(['node', '--test', 'tool/web/bridge_test.mjs'])
    with tempfile.TemporaryDirectory(prefix='flclash-web-tests-') as temporary:
        source = stage(ROOT, Path(temporary) / 'src')
        shutil.copyfile(ROOT / 'tool/web/vfs_test.dart.tmpl', source / 'vfs_test.dart')
        run(['flutter', 'pub', 'get'], cwd=source)
        compiled = source / 'vfs_test.js'
        run(['dart', 'compile', 'js', 'vfs_test.dart', '-o', str(compiled)], cwd=source)
        run(['node', str(ROOT / 'tool/web/vfs_runtime_test.mjs'), str(compiled)])


if __name__ == '__main__':
    main()

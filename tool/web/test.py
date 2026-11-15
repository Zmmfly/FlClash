#!/usr/bin/env python3
"""Run Web staging, browser-bridge and compiled Dart VFS contract tests."""

import sys

sys.dont_write_bytecode = True
if sys.version_info < (3, 10):
    raise SystemExit('Python 3.10 or newer is required')

import os
import shutil
import subprocess
import tempfile
from pathlib import Path

from stage import ROOT, stage


def run(arguments, cwd=ROOT, without_proxy=False):
    environment = dict(os.environ)
    if without_proxy:
        environment = {key: value for key, value in environment.items()
                       if key.lower() not in ('http_proxy', 'https_proxy', 'all_proxy')}
    subprocess.run(arguments, cwd=cwd, check=True, env=environment)


def configure_browser_test_hooks(source):
    pubspec = source / 'pubspec.yaml'
    text = pubspec.read_text()
    anchor = '  user_defines:\n    setup:'
    if text.count(anchor) != 1:
        raise RuntimeError('Missing isolated browser-test hook anchor')
    # Chrome uses WASM SQLite; its unrelated host hook must not fetch a native library.
    pubspec.write_text(text.replace(anchor,
        '  user_defines:\n    sqlite3:\n      source: system\n    setup:', 1))


def main():
    for executable in ('flutter', 'dart', 'node'):
        if shutil.which(executable) is None:
            raise SystemExit(f'Missing test dependency on PATH: {executable}')
    if 'CHROME_EXECUTABLE' not in os.environ and Path('/usr/bin/google-chrome').is_file():
        os.environ['CHROME_EXECUTABLE'] = '/usr/bin/google-chrome'
    run(['python3', '-m', 'unittest', 'discover', '-s', 'tool/web', '-p', 'test_*.py', '-v'])
    run(['node', '--test', 'tool/web/bridge_test.mjs', 'tool/web/login_test.mjs'])
    with tempfile.TemporaryDirectory(prefix='flclash-web-tests-') as temporary:
        source = stage(ROOT, Path(temporary) / 'src')
        configure_browser_test_hooks(source)
        shutil.copyfile(ROOT / 'tool/web/vfs_test.dart.tmpl', source / 'vfs_test.dart')
        run(['flutter', 'pub', 'get'], cwd=source)
        run(['dart', 'run', 'intl_utils:generate'], cwd=source)
        compiled = source / 'vfs_test.js'
        run(['dart', 'compile', 'js', 'vfs_test.dart', '-o', str(compiled)], cwd=source)
        run(['node', str(ROOT / 'tool/web/vfs_runtime_test.mjs'), str(compiled)])
        (source / 'test').mkdir()
        shutil.copyfile(ROOT / 'tool/web/account_widget_test.dart.tmpl', source / 'test/web_account_test.dart')
        shutil.copyfile(ROOT / 'tool/web/account_disabled_test.dart.tmpl', source / 'test/web_account_disabled_test.dart')
        run(['flutter', 'test', '--platform', 'chrome', '--dart-define=WEB_PANEL=true',
             '--dart-define=SAFE_MODE=true', 'test/web_account_test.dart'], cwd=source, without_proxy=True)
        run(['flutter', 'test', '--platform', 'chrome', '--dart-define=SAFE_MODE=true',
             'test/web_account_disabled_test.dart'], cwd=source, without_proxy=True)


if __name__ == '__main__':
    main()

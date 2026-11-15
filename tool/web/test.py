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
        shutil.copyfile(ROOT / 'tool/web/vfs_test.dart.tmpl', source / 'vfs_test.dart')
        run(['flutter', 'pub', 'get'], cwd=source)
        run(['dart', 'run', 'intl_utils:generate'], cwd=source)
        run(['python3', str(ROOT / 'tool/web/validate_artifact.py'),
             '--staged', str(source), '--deps', str(source / '.dart_tool/package_config.json')])
        compiled = source / 'vfs_test.js'
        run(['dart', 'compile', 'js', 'vfs_test.dart', '-o', str(compiled)], cwd=source)
        run(['node', str(ROOT / 'tool/web/vfs_runtime_test.mjs'), str(compiled)])
        (source / 'test').mkdir(exist_ok=True)
        api_tests = []
        for template in sorted((ROOT / 'tool/web').glob('api_*_test.dart.tmpl')):
            if template.name == 'api_client_browser_test.dart.tmpl':
                continue
            destination = source / 'test' / template.with_suffix('').name
            shutil.copyfile(template, destination)
            api_tests.append(str(destination.relative_to(source)))
        browser_client = ROOT / 'tool/web/api_client_browser_test.dart.tmpl'
        if browser_client.is_file():
            shutil.copyfile(browser_client, source / 'api_client_browser_test.dart')
            browser_js = source / 'api_client_browser_test.js'
            run(['dart', 'compile', 'js', 'api_client_browser_test.dart',
                 '-o', str(browser_js)], cwd=source)
            run(['node', str(ROOT / 'tool/web/api_redirect_fixture.mjs'),
                 str(browser_js)], without_proxy=True)
        api_tests = sorted(set(api_tests) | {
            str(path.relative_to(source)) for path in (source / 'test').glob('api_*_test.dart')
        })
        if not api_tests:
            raise RuntimeError('Missing typed API browser contracts')
        run(['flutter', 'test', '--platform', 'chrome', '--dart-define=WEB_PANEL=true',
             '--dart-define=SAFE_MODE=true', *api_tests],
            cwd=source, without_proxy=True)
        shutil.copyfile(ROOT / 'tool/web/account_widget_test.dart.tmpl', source / 'test/web_account_test.dart')
        shutil.copyfile(ROOT / 'tool/web/account_disabled_test.dart.tmpl', source / 'test/web_account_disabled_test.dart')
        shutil.copyfile(ROOT / 'tool/web/navigation_widget_test.dart.tmpl', source / 'test/web_navigation_test.dart')
        run(['flutter', 'test', '--platform', 'chrome', '--dart-define=WEB_PANEL=true',
             '--dart-define=SAFE_MODE=true', 'test/web_account_test.dart',
             'test/web_navigation_test.dart'], cwd=source, without_proxy=True)
        run(['flutter', 'test', '--platform', 'chrome', '--dart-define=SAFE_MODE=true',
             'test/web_account_disabled_test.dart'], cwd=source, without_proxy=True)


if __name__ == '__main__':
    main()

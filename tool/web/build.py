#!/usr/bin/env python3
"""Validate dependencies, stage the checkout, and build the standalone Web panel."""

import sys

sys.dont_write_bytecode = True
if sys.version_info < (3, 10):
    raise SystemExit('Python 3.10 or newer is required')

import argparse
import hashlib
import json
import os
import re
import shutil
import subprocess
import tempfile
from pathlib import Path

from stage import ROOT, StageError, stage, validate_source
from validate_artifact import validate_artifact

def run(arguments, cwd=ROOT, capture=False, env=None):
    return subprocess.run(arguments, cwd=cwd, check=True, text=True,
                          stdout=subprocess.PIPE if capture else None,
                          env=env).stdout


def version_tuple(value):
    match = re.search(r'(\d+)\.(\d+)(?:\.(\d+))?', value)
    if not match:
        raise StageError(f'Cannot parse toolchain version: {value!r}')
    return tuple(int(part or 0) for part in match.groups())


def scalar(text, name):
    match = re.search(rf'^{name}:\s*(.+?)\s*$', text, re.M)
    if not match:
        raise StageError(f'Missing build_config.yaml scalar: {name}')
    return match[1].strip('"\'')


def preflight():
    for command in ('flutter', 'dart', 'go'):
        if shutil.which(command) is None:
            raise StageError(f'Missing {command} on PATH; install the repository-pinned SDK/toolchain first')
    validate_source(ROOT)
    if not (ROOT / 'core/cmd/flclash-web/main.go').is_file():
        raise StageError('Missing core/cmd/flclash-web backend')
    if not (ROOT / 'core/cmd/flclash-web/web_dist/.gitkeep').is_file():
        raise StageError('Missing Web embed placeholder core/cmd/flclash-web/web_dist/.gitkeep')
    sdk = json.loads(run(['flutter', '--version', '--machine'], capture=True))
    if not ((3, 47, 2) <= version_tuple(sdk['frameworkVersion']) < (3, 48, 0)):
        raise StageError('Flutter 3.47.x (at least 3.47.2; CI pins 3.47.6) is required')
    if version_tuple(sdk['dartSdkVersion']) < (3, 13, 2):
        raise StageError('Dart 3.13.2 or newer is required by code_forge')
    required = re.search(r'^go (\S+)', (ROOT / 'core/go.mod').read_text(), re.M)
    if not required:
        raise StageError('Missing Go version in core/go.mod')
    go_version = run(['go', 'version'], cwd=ROOT / 'core', capture=True)
    if version_tuple(go_version) < version_tuple(required[1]):
        raise StageError(f'Go {required[1]} or newer is required; found {go_version.strip()}')
    config = (ROOT / 'build_config.yaml').read_text()
    tags = scalar(config, 'tags')
    if not re.fullmatch(r'[\w, ]+', tags):
        raise StageError('Invalid build tags in build_config.yaml')
    tags = ','.join(['webpanel', *filter(None, re.split(r'[, ]+', tags))])
    version = re.search(r'^version:\s*([^+\s]+)', (ROOT / 'pubspec.yaml').read_text(), re.M)[1]
    return version, tags, scalar(config, 'go_ldflags'), scalar(config, 'core_version')


def validate_service_worker(web_output):
    worker = web_output / 'flutter_service_worker.js'
    if worker.exists():
        text = worker.read_text()
        if re.search(r'''addEventListener\s*\(\s*['"]fetch['"]|\bcaches\b|\bRESOURCES\b''', text):
            raise StageError('Refusing an authentication-bypassing caching service worker')


def require_api_artifact(**inputs):
    report = validate_artifact(**inputs)
    if not report.ok:
        findings = '; '.join(f'{item.code}: {item.path}' for item in report.errors)
        raise StageError(f'Typed Web artifact validation failed: {findings}')


def build(safe_mode=False):
    version, tags, flags, core_version = preflight()
    build_dir = ROOT / 'build'
    if build_dir.is_symlink():
        raise StageError('build/ must not be a symlink')
    build_dir.mkdir(exist_ok=True)
    work = Path(tempfile.mkdtemp(prefix='webpanel-src-', dir=build_dir))
    work.rmdir()
    stage(ROOT, work)
    print(f'Isolated Web sources: {work}', flush=True)
    run(['flutter', 'pub', 'get'], cwd=work)
    run(['dart', 'run', 'intl_utils:generate'], cwd=work)
    require_api_artifact(staged=work, deps=work / '.dart_tool/package_config.json')
    flutter_arguments = ['flutter', 'build', 'web', '--release', '--no-tree-shake-icons',
                         '--dart-define=APP_ENV=stable', '--dart-define=WEB_PANEL=true',
                         f'--build-name={version}']
    if safe_mode:
        flutter_arguments.append('--dart-define=SAFE_MODE=true')
    run(flutter_arguments, cwd=work)
    web_output = work / 'build/web'
    if not (web_output / 'index.html').is_file() or not (web_output / 'main.dart.js').is_file():
        raise StageError('Flutter build did not produce the expected Web entrypoints')
    validate_service_worker(web_output)
    require_api_artifact(web=web_output)
    embed = work / 'core/cmd/flclash-web/web_dist'
    shutil.copytree(web_output, embed)
    env = dict(os.environ, CGO_ENABLED='0')
    flags += f' -X main.Version={version} -X github.com/metacubex/mihomo/constant.Version={core_version}'
    binary = work / 'FlClash-web'
    run(['go', 'build', '-trimpath', '-tags', tags, '-ldflags', flags,
         '-o', str(binary), './cmd/flclash-web'], cwd=work / 'core', env=env)
    require_api_artifact(server=binary)
    dist = ROOT / 'dist'
    if dist.is_symlink():
        raise StageError('dist/ must not be a symlink')
    dist.mkdir(exist_ok=True)
    output = dist / 'FlClash-web'
    if output.is_symlink() or (output.exists() and not output.is_file()):
        raise StageError('Refusing to replace a non-regular dist/FlClash-web')
    descriptor, temporary_name = tempfile.mkstemp(prefix='.FlClash-web-', dir=dist)
    os.close(descriptor)
    temporary = Path(temporary_name)
    try:
        shutil.copy2(binary, temporary)
        temporary.chmod(0o755)
        temporary.replace(output)
    finally:
        temporary.unlink(missing_ok=True)
    print(f'Built {output} (v{version}; tags={tags})', flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--check', action='store_true', help='Only validate build dependencies')
    parser.add_argument('--safe-mode', action='store_true',
                        help='Build the UI with SAFE_MODE=true for host-safe browser validation')
    args = parser.parse_args()
    try:
        if args.check:
            preflight()
            print('Web build dependencies are ready')
        else:
            build(safe_mode=args.safe_mode)
    except (StageError, OSError, ValueError, KeyError, subprocess.CalledProcessError) as error:
        parser.exit(1, f'Web build failed: {error}\n')


if __name__ == '__main__':
    main()

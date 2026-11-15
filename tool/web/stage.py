#!/usr/bin/env python3
"""Stage the current checkout for Web without changing native sources."""

import sys

sys.dont_write_bytecode = True
if sys.version_info < (3, 10):
    raise SystemExit('Python 3.10 or newer is required')

import argparse
import json
import re
import shutil
import tempfile
from pathlib import Path

from navigation_adapt import adapt_navigation
from api_adapt import adapt_api, check_api_overlay, validate_api_sources
from workflow_adapt import adapt_workflows
from scheduler_adapt import adapt_scheduler
from utility_adapt import adapt_utilities
from evaluate_adapt import adapt_evaluate

from adaptations import (
    CODE_FORGE_IMPORT_REWRITES,
    LIB_IMPORT_REWRITES,
    NOOP_HOOK_DART,
    RUST_API_PUBSPEC_WEB,
    WEB_SYSTEM_FONTS_DART,
    dedupe_imports,
)

ROOT = Path(__file__).resolve().parents[2]
OVERLAY = Path(__file__).resolve().parent / 'overlay'
COPY_DIRS = ('lib', 'plugins', 'assets', 'arb', 'core')
COPY_FILES = (
    'pubspec.yaml', 'pubspec.lock', 'build.yaml', 'build_config.yaml',
    'analysis_options.yaml', 'lint_options.yaml', 'LICENSE',
)


class StageError(RuntimeError):
    pass


def generated(path):
    return ('generated' in path.parts or 'l10n' in path.parts or
            path.name.endswith(('.g.dart', '.freezed.dart')))


def replace_exact(root, relative, old, new, count=1):
    path = root / relative
    if generated(path):
        raise StageError(f'Cannot patch generated file: {relative}')
    if not path.is_file():
        raise StageError(f'Missing required adaptation file: {relative}')
    text = path.read_text()
    actual = text.count(old)
    if actual != count:
        raise StageError(f'{relative}: expected {count} adaptation anchor(s), found {actual}: {old!r}')
    path.write_text(text.replace(old, new))


def replace_file(root, relative, content, anchor):
    path = root / relative
    if not path.is_file() or anchor not in path.read_text():
        raise StageError(f'{relative}: missing replacement contract: {anchor!r}')
    if generated(path):
        raise StageError(f'Cannot replace generated file: {relative}')
    path.write_text(content)


def adapt(root, overlay=OVERLAY):
    for template in sorted(overlay.rglob('*.dart.tmpl')):
        relative = template.relative_to(overlay).with_suffix('')
        destination = root / relative
        if generated(destination):
            raise StageError(f'Generated overlay forbidden: {relative}')
        if (destination.exists() and relative != Path('lib/common/mitm_trust.dart')
                and not check_api_overlay(root, relative, StageError)):
            raise StageError(f'Unexpected overlay collision: {relative}')
        if relative == Path('lib/common/mitm_trust.dart'):
            if 'MitmTrustStore? mitmTrustStoreForPlatform()' not in destination.read_text():
                raise StageError('Missing native MITM trust-store anchor')
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(template, destination)

    adapt_api(root, replace_exact, StageError)
    adapt_workflows(root, replace_exact)
    adapt_scheduler(root, replace_exact)
    adapt_utilities(root, replace_exact)
    obsolete_cache = root / 'lib/common/cache.dart'
    if 'extension CacheManagerExt on CacheManager' not in obsolete_cache.read_text():
        raise StageError('Missing native image cache helper anchor')
    obsolete_cache.unlink()
    adapt_evaluate(root, replace_exact)
    shim = root / 'lib/web_shim'
    rust = root / 'plugins/rust_api/lib'
    if not (rust / 'rust_api.dart').is_file():
        raise StageError('Missing rust_api package entrypoint')
    for name in ('js_bridge.dart', 'vfs.dart', 'web_core_bridge.dart', 'dart_io.dart',
                 'rust_api_editor.dart', 'rust_api.dart'):
        shutil.copyfile(shim / name, rust / name)
        (shim / name).write_text(f"export 'package:rust_api/{name}';\n")
    (rust / 'dart_io.dart').write_text("export 'vfs.dart';\n" + (rust / 'dart_io.dart').read_text())
    replace_file(root, 'plugins/rust_api/lib/editor.dart', "export 'rust_api_editor.dart';\n", 'export ')
    replace_file(root, 'plugins/rust_api/pubspec.yaml', RUST_API_PUBSPEC_WEB, 'name: rust_api')
    for relative in ('plugins/setup/hook/build.dart', 'plugins/rust_api/hook/build.dart'):
        replace_file(root, relative, NOOP_HOOK_DART, 'void main(')
    replace_file(root, 'lib/common/system_fonts.dart', WEB_SYSTEM_FONTS_DART, 'loadSystemFontFamilies()')
    replace_exact(root, 'lib/providers/actions/setup.dart',
                  'Future<DateTime?> serviceStartTime() async => service?.getRunTime();',
                  '''Future<DateTime?> serviceStartTime() async {
    final started = await WebRuntimeBinding.instance.startTime();
    return started == null || started == 0
        ? null : DateTime.fromMillisecondsSinceEpoch(started);
  }''')
    replace_exact(root, 'lib/plugins/app.dart', 'return FileImage(File(path));',
                  'return MemoryImage(File(path).readAsBytesSync());')
    replace_exact(root, 'lib/common/layout.dart',
                  '  if (!isDesktop) return 0;\n  return isMacOS ? 28 : 32;', '  return 0;')
    replace_exact(root, 'lib/common/layout.dart',
                  '  if (!isDesktop) return false;\n  return !(isMacOS && (version <= 10 || !isMobileView));',
                  '  return false;')
    replace_exact(root, 'plugins/prism_ui/lib/keyboard.dart', "import 'dart:io';",
                  "import 'package:flutter/foundation.dart';")
    replace_exact(root, 'plugins/prism_ui/lib/keyboard.dart', 'Platform.isMacOS',
                  '(defaultTargetPlatform == TargetPlatform.macOS)')
    replace_exact(root, 'plugins/prism_ui/lib/liquid_glass.dart',
                  'builder.pushClipRSuperellipse(corners.toRSuperellipse(rect));',
                  'builder.pushClipRSuperellipse(corners.toRSuperellipse(rect), clipBehavior: ui.Clip.antiAlias);')

    replace_exact(root, 'lib/views/tools.dart',
                  "import 'package:fl_clash/widgets/widgets.dart';",
                  "import 'package:fl_clash/widgets/widgets.dart';\n"
                  "import 'package:fl_clash/web_auth/account_view.dart';")
    replace_exact(root, 'lib/views/tools.dart',
                  '        const _GeneralItem(),',
                  '        const _GeneralItem(),\n        if (webPanelBuild) const WebAccountItem(),')
    merge_web_localizations(root)
    adapt_navigation(root, replace_exact)

    counts = {}
    for directory, rewrites in ((root / 'lib', LIB_IMPORT_REWRITES),
                                (root / 'plugins/code_forge/lib', CODE_FORGE_IMPORT_REWRITES)):
        for file in sorted(directory.rglob('*.dart')):
            if shim in file.parents or generated(file):
                continue
            original = file.read_text()
            text = original
            for pattern, replacement in rewrites:
                text, count = pattern.subn(replacement, text)
                if count:
                    counts[pattern.pattern] = counts.get(pattern.pattern, 0) + count
            text = dedupe_imports(text)
            if original != text:
                file.write_text(text)
    for pattern, _ in LIB_IMPORT_REWRITES + CODE_FORGE_IMPORT_REWRITES:
        if counts.get(pattern.pattern, 0) == 0:
            raise StageError(f'Import rewrite has no matching anchor: {pattern.pattern}')
    validate_api_sources(root, StageError)
    return counts


def merge_web_localizations(root):
    expected = None
    for locale in ('en', 'zh_CN', 'ja', 'ru', 'fr', 'es', 'ko'):
        source = root / 'core/webui' / f'intl_{locale}.arb'
        if not source.is_file():
            raise StageError(f'Missing Web account locale: {locale}')
        messages = json.loads(source.read_text())
        keys = set(messages)
        if not keys or any(not key.startswith('web') or not isinstance(value, str) or not value
                           for key, value in messages.items()):
            raise StageError(f'Invalid Web account locale: {locale}')
        if expected is not None and keys != expected:
            raise StageError(f'Web account locale keys differ: {locale}')
        expected = keys
        destination = root / 'arb' / f'intl_{locale}.arb'
        if not destination.is_file():
            raise StageError(f'Missing native locale: {locale}')
        original = json.loads(destination.read_text())
        if keys & set(original):
            raise StageError(f'Web account keys collide with native locale: {locale}')
        original.update(messages)
        destination.write_text(json.dumps(original, ensure_ascii=False, indent=2) + '\n')


def validate_source(source):
    for relative in COPY_DIRS + COPY_FILES + (
        'plugins/prism_ui/lib/keyboard.dart', 'core/Clash.Meta/go.mod',
    ):
        if not (source / relative).exists():
            raise StageError(f'Missing {relative}; initialize pinned submodules and dependencies first')
    if not re.search(r'^version: 0\.9\.3\+', (source / 'pubspec.yaml').read_text(), re.M):
        raise StageError('Web adaptations require the v0.9.3 application/protocol baseline')


def validate_output(source, output):
    absolute = Path(output).absolute()
    if any(parent.is_symlink() for parent in (absolute, *absolute.parents)):
        raise StageError('Output and its parents must not be symlinks')
    output = absolute.resolve()
    if output.exists():
        raise StageError(f'Refusing to overwrite existing output: {output}')
    if output == source or output in source.parents:
        raise StageError('Output must not contain the source checkout')
    if source in output.parents and output.parent != source / 'build':
        raise StageError('Inside the checkout, output must be a direct child of build/')
    if not output.parent.is_dir():
        raise StageError('Output parent directory must already exist')
    return output


def copy_ignore(directory, names):
    ignored = {'.git', '.dart_tool', 'build', 'dist', 'node_modules', '__pycache__', '.DS_Store', 'web_dist'}
    return [name for name in names if name in ignored]


def stage(source, output, overlay=OVERLAY):
    source = Path(source).resolve()
    validate_source(source)
    output = validate_output(source, output)
    work = Path(tempfile.mkdtemp(prefix='.webpanel-stage-', dir=output.parent))
    try:
        for directory in COPY_DIRS:
            shutil.copytree(source / directory, work / directory, ignore=copy_ignore)
        for relative in COPY_FILES:
            shutil.copy2(source / relative, work / relative)
        shutil.copytree(source / 'web', work / 'web', ignore=copy_ignore)
        counts = adapt(work, overlay)
        (work / '.webpanel-stage.json').write_text(json.dumps({
            'source': str(source), 'protocol': '0.9.3', 'import_rewrites': counts,
        }, indent=2) + '\n')
        output.mkdir()
        work.rename(output)
        return output
    finally:
        if work.exists():
            shutil.rmtree(work)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', required=True, type=Path,
                        help='Absent destination; never replaced or deleted if it already exists')
    args = parser.parse_args()
    try:
        print(stage(ROOT, args.output))
    except (StageError, OSError) as error:
        parser.exit(1, f'Web staging failed: {error}\n')


if __name__ == '__main__':
    main()

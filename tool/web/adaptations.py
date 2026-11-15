import re


WEB_SYSTEM_FONTS_DART = """import 'package:flutter/foundation.dart' show visibleForTesting;

@visibleForTesting
Future<List<String>> Function() systemFontFamiliesReader = () async =>
    sortFontFamilies(const [
      'Roboto',
      'Noto Sans SC',
      'Noto Sans TC',
      'Noto Sans JP',
      'Inter',
      'JetBrains Mono',
      'DejaVu Sans',
      'Arial',
      'Helvetica',
      'monospace',
      'sans-serif',
    ]);

Future<List<String>> loadSystemFontFamilies() async {
  try {
    return await systemFontFamiliesReader();
  } catch (_) {
    return const [];
  }
}

@visibleForTesting
List<String> sortFontFamilies(Iterable<String> families) {
  final unique = <String, String>{};
  for (final family in families) {
    final name = family.trim();
    if (name.isEmpty || name.startsWith('.')) {
      continue;
    }
    unique.putIfAbsent(name.toLowerCase(), () => name);
  }
  return unique.values.toList()
    ..sort((a, b) => a.toLowerCase().compareTo(b.toLowerCase()));
}

@visibleForTesting
List<String> androidFontFamilies(String fontsXml) => [
  for (final match in RegExp(
    r'<family\\b[^>]*\\bname="([^"]+)"',
  ).allMatches(fontsXml))
    match.group(1)!,
];
"""

NOOP_HOOK_DART = """import 'package:hooks/hooks.dart';

void main(List<String> args) async {
  await build(args, (input, output) async {});
}
"""

RUST_API_PUBSPEC_WEB = """name: rust_api
description: "Flutter Rust Bridge FFI package (Web shim mode)."
version: 0.0.1
publish_to: none

environment:
  sdk: '>=3.9.2 <4.0.0'
  flutter: '>=3.3.0'

dependencies:
  archive: any
  code_assets: ^1.2.1
  flutter:
    sdk: flutter
  flutter_rust_bridge: 2.13.0
  flutter_rust_bridge_hooks: 2.13.0
  path: any

dev_dependencies:
  flutter_test:
    sdk: flutter
  flutter_lints: ^6.0.0
"""

LIB_IMPORT_REWRITES = [
    (
        re.compile(r"""^(import|export)\s+['"]dart:io['"](.*)$""", re.MULTILINE),
        r"\1 'package:fl_clash/web_shim/dart_io.dart'\2",
    ),
    (
        re.compile(r"""^(import|export)\s+['"]dart:ffi['"](.*)$""", re.MULTILINE),
        r"\1 'package:fl_clash/web_shim/dart_ffi.dart'\2",
    ),
    (
        re.compile(r"""^(import|export)\s+['"]dart:isolate['"](.*)$""", re.MULTILINE),
        r"\1 'package:fl_clash/web_shim/dart_io.dart'\2",
    ),
    (
        re.compile(r"""^(import|export)\s+['"]package:ffi/ffi\.dart['"](.*)$""", re.MULTILINE),
        r"\1 'package:fl_clash/web_shim/dart_ffi.dart'\2",
    ),
    (
        re.compile(r"""^(import|export)\s+['"]package:win32_registry/win32_registry\.dart['"](.*)$""", re.MULTILINE),
        r"\1 'package:fl_clash/web_shim/dart_ffi.dart'\2",
    ),
    (
        re.compile(r"""^(import|export)\s+['"]package:dio/dio\.dart['"](.*)$""", re.MULTILINE),
        r"\1 'package:fl_clash/web_shim/dio.dart'\2",
    ),
    (
        re.compile(r"""^(import|export)\s+['"]package:dio/io\.dart['"](.*)$""", re.MULTILINE),
        r"\1 'package:fl_clash/web_shim/dio_io.dart'\2",
    ),
    (
        re.compile(r"""^(import|export)\s+['"]package:path_provider/path_provider\.dart['"](.*)$""", re.MULTILINE),
        r"\1 'package:fl_clash/web_shim/plugins_shim.dart'\2",
    ),
    (
        re.compile(r"""^(import|export)\s+['"]package:file_picker/file_picker\.dart['"](.*)$""", re.MULTILINE),
        r"\1 'package:fl_clash/web_shim/plugins_shim.dart'\2",
    ),
    (
        re.compile(r"""^(import|export)\s+['"]package:window/window\.dart['"](.*)$""", re.MULTILINE),
        r"\1 'package:fl_clash/web_shim/plugins_shim.dart'\2",
    ),
    (
        re.compile(r"""^(import|export)\s+['"]package:launch_at_startup/launch_at_startup\.dart['"](.*)$""", re.MULTILINE),
        r"\1 'package:fl_clash/web_shim/plugins_shim.dart'\2",
    ),
    (
        re.compile(r"""^(import|export)\s+['"]package:app_links/app_links\.dart['"](.*)$""", re.MULTILINE),
        r"\1 'package:fl_clash/web_shim/plugins_shim.dart'\2",
    ),
    (
        re.compile(r"""^(import|export)\s+['"]package:tray/tray\.dart['"](.*)$""", re.MULTILINE),
        r"\1 'package:fl_clash/web_shim/plugins_shim.dart'\2",
    ),
    (
        re.compile(r"""^(import|export)\s+['"]package:proxy/proxy\.dart['"](.*)$""", re.MULTILINE),
        r"\1 'package:fl_clash/web_shim/plugins_shim.dart'\2",
    ),
    (
        re.compile(r"""^(import|export)\s+['"]package:connectivity_plus/connectivity_plus\.dart['"](.*)$""", re.MULTILINE),
        r"\1 'package:fl_clash/web_shim/plugins_shim.dart'\2",
    ),
]

CODE_FORGE_IMPORT_REWRITES = [
    (
        re.compile(r"""^(import|export)\s+['"]dart:io['"](.*)$""", re.MULTILINE),
        r"\1 'package:rust_api/dart_io.dart'\2",
    ),
    (
        re.compile(r"""^(import|export)\s+['"]dart:isolate['"](.*)$""", re.MULTILINE),
        r"\1 'package:rust_api/dart_io.dart'\2",
    ),
]

def dedupe_imports(text: str) -> str:
    seen = set()
    out_lines = []
    for line in text.splitlines(keepends=True):
        stripped = line.strip()
        if stripped.startswith("import 'package:fl_clash/web_shim/") or stripped.startswith(
            "import 'package:rust_api/"
        ):
            if stripped in seen:
                continue
            seen.add(stripped)
        out_lines.append(line)
    return "".join(out_lines)

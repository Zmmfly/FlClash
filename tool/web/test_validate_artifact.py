import contextlib
import io
import json
import tempfile
import unittest
from pathlib import Path

import validate_artifact as validator


class ArtifactValidationTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)

    def tearDown(self):
        self.temporary.cleanup()

    def write(self, relative, content):
        path = self.root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        if isinstance(content, bytes):
            path.write_bytes(content)
        else:
            path.write_text(content)
        return path

    def staged(self):
        self.write('lib/web_api/client.dart',
                   "const route = '/api/v1/profiles';\n")
        self.write('lib/web_shim/vfs.dart',
                   "const ephemeral = '/tmp/';\nconst download = '/downloads/';\n")
        self.write('lib/database/database.dart', 'class WebDatabase {}\n')
        self.write('lib/providers/database.dart', 'class WebApiRepository {}\n')
        self.write('web/bridge.js',
                   "fetch('/api/auth/session');\n")
        self.write('web/canvaskit.wasm', b'\x00asm\x01\x00\x00\x00')
        self.write('pubspec.yaml', 'name: fl_clash\n')
        return self.root

    def codes(self, findings):
        return {finding.code for finding in findings}

    def test_clean_staged_source_has_no_findings(self):
        self.assertEqual([], validator.validate_staged_source(self.staged()))

    def test_staged_server_component_keeps_guest_runtime(self):
        self.staged()
        self.write('core/web_api_evaluate_guest/quickjs.wasm', b'\x00asm')
        self.assertEqual([], validator.validate_staged_source(self.root))

    def test_staged_browser_assets_reject_guest_runtime(self):
        self.staged()
        for relative in ('web/quickjs.wasm', 'assets/quickjs.wasm'):
            self.write(relative, b'\x00asm')
            codes = self.codes(validator.validate_staged_source(self.root))
            self.assertIn('server_only_wasm_in_browser', codes, relative)

    def test_staged_server_component_still_rejects_sqlite_engine(self):
        self.staged()
        self.write('core/web_api_evaluate_guest/sqlite3.wasm', b'\x00asm')
        codes = self.codes(validator.validate_staged_source(self.root))
        self.assertIn('sqlite_engine', codes)

    def test_sqlite_engine_in_staged_tree_is_rejected(self):
        self.staged()
        self.write('web/sqlite3.wasm', b'\x00asm')
        codes = self.codes(validator.validate_staged_source(self.root))
        self.assertIn('browser_sqlite_engine', codes)

    def test_drift_database_staged_is_rejected(self):
        self.staged()
        self.write('lib/database/database.dart',
                   "import 'package:drift/drift.dart';\n@DriftDatabase(tables: [])\nclass Database {}\n")
        codes = self.codes(validator.validate_staged_source(self.root))
        self.assertIn('native_database_staged', codes)
        self.assertIn('sqlite_dependency', codes)

    def test_native_drift_shim_is_rejected(self):
        self.staged()
        self.write('lib/web_shim/drift_native.dart', 'class Shim {}\n')
        codes = self.codes(validator.validate_staged_source(self.root))
        self.assertIn('native_database_staged', codes)

    def test_ordinary_sql_in_persistence_boundary_is_rejected(self):
        self.staged()
        self.write('lib/web_api/store.dart',
                   "const pragma = 'PRAGMA foreign_keys = ON';\n")
        codes = self.codes(validator.validate_staged_source(self.root))
        self.assertIn('ordinary_sql', codes)

    def test_sql_dependency_and_engine_markers_are_rejected(self):
        self.staged()
        self.write('lib/web_api/legacy.dart',
                   "import 'package:sqlite3/wasm.dart';\nfinal db = WasmSqlite3();\n")
        codes = self.codes(validator.validate_staged_source(self.root))
        self.assertIn('sqlite_dependency', codes)
        self.assertIn('sqlite_engine', codes)

    def test_staged_sqflite_family_and_cache_manager_are_rejected(self):
        self.staged()
        self.write('lib/widgets/icon.dart',
                   "import 'package:flutter_cache_manager/flutter_cache_manager.dart';\n")
        self.write('lib/legacy_cache.dart',
                   "import 'package:sqflite_common_ffi_web/sqflite_ffi_web.dart';\n")
        self.write('pubspec.yaml',
                   'name: fl_clash\ndependencies:\n  sqflite: any\n  flutter_cache_manager: any\n')
        self.write('pubspec.lock', '  sqflite:\n  flutter_cache_manager:\n')
        findings = validator.validate_staged_source(self.root)
        errors = {finding.code for finding in findings if finding.severity == validator.ERROR}
        self.assertIn('cache_manager_dependency', errors)
        self.assertIn('sqflite_dependency', errors)
        self.assertIn('staged_sqflite_dependency', errors)
        self.assertIn('staged_cache_manager_dependency', errors)
        self.assertIn('staged_lockfile_sqflite', errors)
        self.assertIn('staged_lockfile_cache_manager', errors)

    def test_dependencies_reject_sqflite_family(self):
        for dependency in ('package:sqflite/sqflite.dart',
                           'sqflite_common_ffi_web',
                           'sqflite_common',
                           'sqflite_web'):
            codes = self.codes(validator.validate_deps(dependency))
            self.assertIn('sqflite_dependency', codes, dependency)
        findings = validator.validate_deps('flutter_cache_manager: 3.4.0\n')
        self.assertIn('cache_manager_dependency', self.codes(findings))
        self.assertTrue(all(finding.severity == validator.WARNING for finding in findings))

    def test_browser_artifact_rejects_sqflite_family_and_cache_manager(self):
        artifact = self.web_artifact()
        self.write('web-artifact/main.dart.js',
                   "const runtime = 'sqflite_common_ffi_web';\n")
        codes = self.codes(validator.validate_web_artifact(artifact))
        self.assertIn('sqflite_engine', codes)
        self.write('web-artifact/main.dart.js',
                   "const helper = 'flutter_cache_manager';\n")
        codes = self.codes(validator.validate_web_artifact(artifact))
        self.assertIn('cache_manager_in_browser', codes)
        server = self.root / 'server'
        self.write('server/web_dist/main.dart.js', "const runtime = 'sqflite';\n")
        self.assertIn('sqflite_engine', self.codes(validator.validate_server_artifact(server)))

    def test_legacy_transport_in_staged_javascript_is_rejected(self):
        self.staged()
        self.write('web/legacy.js',
                   "fetch('/api/http-proxy', {body: payload});\n")
        codes = self.codes(validator.validate_staged_source(self.root))
        self.assertIn('legacy_transport', codes)

    def test_synchronous_xhr_in_bridge_is_rejected(self):
        self.staged()
        self.write('web/bridge.js', 'new XMLHttpRequest().open("GET", "/x", false);\n')
        codes = self.codes(validator.validate_staged_source(self.root))
        self.assertIn('synchronous_xhr', codes)

    def test_database_bytes_in_any_staged_file_are_rejected(self):
        self.staged()
        self.write('lib/cache.bin', validator.DB_MAGIC + b'payload')
        codes = self.codes(validator.validate_staged_source(self.root))
        self.assertIn('database_file', codes)

    def test_sqlite_reference_in_staged_tree_is_rejected(self):
        self.staged()
        self.write('lib/web_api/assets.dart', "const engine = 'sqlite3.wasm';\n")
        codes = self.codes(validator.validate_staged_source(self.root))
        self.assertIn('sqlite_engine', codes)

    def web_artifact(self):
        self.write('web-artifact/main.dart.js', "console.log('/api/v1/profiles');\n")
        self.write('web-artifact/flutter_bootstrap.js', 'const _flutter = {};\n')
        self.write('web-artifact/canvaskit.wasm', b'\x00asm')
        self.write('web-artifact/AssetManifest.json', '{"assets": []}')
        return self.root / 'web-artifact'

    def test_clean_browser_artifact_keeps_canvas_wasm(self):
        self.assertEqual([], validator.validate_web_artifact(self.web_artifact()))

    def test_browser_artifact_rejects_sqlite_wasm(self):
        artifact = self.web_artifact()
        self.write('web-artifact/sqlite3.wasm', b'\x00asm')
        codes = self.codes(validator.validate_web_artifact(artifact))
        self.assertIn('browser_sqlite_engine', codes)

    def test_browser_artifact_rejects_server_only_guest_runtime(self):
        artifact = self.web_artifact()
        self.write('web-artifact/quickjs.wasm', b'\x00asm')
        codes = self.codes(validator.validate_web_artifact(artifact))
        self.assertIn('server_only_wasm_in_browser', codes)

    def test_unknown_browser_wasm_warns_but_does_not_fail(self):
        artifact = self.web_artifact()
        self.write('web-artifact/physics.wasm', b'\x00asm')
        findings = validator.validate_web_artifact(artifact)
        self.assertIn('unexpected_wasm', self.codes(findings))
        self.assertTrue(all(finding.severity == validator.WARNING for finding in findings))

    def test_browser_artifact_rejects_removed_transport(self):
        artifact = self.web_artifact()
        self.write('web-artifact/main.dart.js',
                   "fetch('/api/fs/read?path=/data/database.sqlite');\n")
        codes = self.codes(validator.validate_web_artifact(artifact))
        self.assertIn('legacy_transport', codes)

    def test_browser_asset_manifest_cannot_list_sqlite(self):
        artifact = self.web_artifact()
        self.write('web-artifact/AssetManifest.bin',
                   b'\x00assets/sqlite3.wasm\x00')
        codes = self.codes(validator.validate_web_artifact(artifact))
        self.assertIn('browser_sqlite_engine', codes)
        self.assertIn('asset_manifest_review', codes)

    def test_browser_artifact_rejects_database_bytes(self):
        artifact = self.web_artifact()
        self.write('web-artifact/legacy.db', validator.DB_MAGIC + b'\x00')
        codes = self.codes(validator.validate_web_artifact(artifact))
        self.assertIn('database_file', codes)

    def test_server_artifact_allows_guest_runtime_but_not_sqlite(self):
        server = self.root / 'server'
        self.write('server/quickjs.wasm', b'\x00asm')
        self.assertEqual([], validator.validate_server_artifact(server))
        self.write('server/sqlite3.wasm', b'\x00asm')
        codes = self.codes(validator.validate_server_artifact(server))
        self.assertIn('sqlite_engine', codes)

    def test_server_binary_rejects_database_prefix(self):
        binary = self.write('server/FlClash-web', validator.DB_MAGIC + b'\x00' * 64)
        codes = self.codes(validator.validate_server_artifact(binary))
        self.assertIn('database_file', codes)

    def test_server_binary_allows_legacy_archive_reader_constant(self):
        # A server-only legacy archive reader keeps the SQLite header as a
        # parser constant; it is not an embedded database.
        binary = self.write('server/FlClash-web',
                            b'\x7fELF\x02\x01\x01\x00' + b'\x00' * 64 +
                            validator.DB_MAGIC + b'\x00reader\x00')
        self.assertEqual([], validator.validate_server_artifact(binary))

    def test_server_binary_rejects_removed_transport(self):
        binary = self.write('server/FlClash-web',
                            b'\x7fELF' + b'/api/http-proxy\x00')
        codes = self.codes(validator.validate_server_artifact(binary))
        self.assertIn('legacy_transport', codes)

    def test_dependency_listing_rejects_sql_packages(self):
        self.assertEqual([], validator.validate_deps('package:flutter/material.dart\npackage:web/web.dart'))
        codes = self.codes(validator.validate_deps(
            'package:flutter/material.dart\npackage:sqlite3/wasm.dart\n'))
        self.assertIn('sqlite_dependency', codes)
        codes = self.codes(validator.validate_deps('libsqlparser.so\n'))
        self.assertIn('sql_parser', codes)
        codes = self.codes(validator.validate_deps('assets/sqlite3.wasm\n'))
        self.assertIn('sqlite_engine', codes)

    def test_dependency_file_path_is_read(self):
        path = self.write('deps.txt', 'package:drift/drift.dart\n')
        codes = self.codes(validator.validate_deps(str(path)))
        self.assertIn('sqlite_dependency', codes)

    def test_network_trace_accepts_typed_routes_and_static_assets(self):
        entries = [
            '/',
            '/api/v1/profiles',
            '/api/auth/session',
            '/api/auth/logout',
            '/ws/v1/events',
            '/assets/AssetManifest.json',
            '/canvaskit/canvaskit.js',
            '/main.dart.js',
            '/flutter_bootstrap.js',
            '/version.json',
            {'url': '/api/v1/runtime'},
            {'request': {'url': '/api/v1/rules?scope=global'}},
            'data:image/png;base64,AA==',
            'blob:https://panel.test/1',
        ]
        self.assertEqual([], validator.validate_network_trace(entries))

    def test_network_trace_rejects_removed_transports(self):
        for url in ('/api/fs/snapshot', '/api/fs/read?path=/data/x',
                    '/api/http-proxy', '/ws/core'):
            findings = validator.validate_network_trace([url])
            self.assertIn('legacy_endpoint', self.codes(findings), url)

    def test_network_trace_rejects_unapproved_routes_and_origins(self):
        self.assertIn('unapproved_api_route',
                      self.codes(validator.validate_network_trace(['/api/v2/profiles'])))
        self.assertIn('unapproved_ws_route',
                      self.codes(validator.validate_network_trace(['/ws/v1/other'])))
        self.assertIn('cross_origin_request',
                      self.codes(validator.validate_network_trace(
                          ['https://example.test/api/v1/profiles'], origin='https://panel.test')))
        self.assertEqual([], validator.validate_network_trace(
            ['https://panel.test/api/v1/profiles'], origin='https://panel.test'))
        self.assertIn('cross_origin_request',
                      self.codes(validator.validate_network_trace(['https://example.test/x'])))
        self.assertIn('unexpected_request',
                      self.codes(validator.validate_network_trace(['/unknown'])))
        self.assertIn('unexpected_request',
                      self.codes(validator.validate_network_trace(
                          ['https://panel.test/unknown'], origin='https://panel.test')))

    def test_network_trace_accepts_har_documents(self):
        har = {
            'log': {
                'version': '1.2',
                'entries': [
                    {'request': {'method': 'GET', 'url': 'https://panel.test/api/v1/profiles'}},
                    {'request': {'url': '/main.dart.js'}},
                ],
            },
        }
        trace = self.write('har.json', json.dumps(har))
        self.assertEqual([], validator.validate_network_trace(
            validator._trace_entries(trace), origin='https://panel.test'))
        har['log']['entries'].append({'request': {'url': '/api/fs/read?path=/data/x'}})
        trace = self.write('har-bad.json', json.dumps(har))
        codes = self.codes(validator.validate_network_trace(
            validator._trace_entries(trace), origin='https://panel.test'))
        self.assertIn('legacy_endpoint', codes)

    def test_network_trace_rejects_malformed_entries(self):
        findings = validator.validate_network_trace([42, {'request': {}}])
        codes = self.codes(findings)
        self.assertEqual({'unreadable_trace_entry'}, codes)

    def test_artifact_report_aggregates_every_source(self):
        self.staged()
        self.write('web/sqlite3.wasm', b'\x00asm')
        report = validator.validate_artifact(
            staged=self.root,
            deps='package:sqlite3/wasm.dart\n',
            trace=['/api/fs/read?path=/data/x'],
        )
        self.assertFalse(report.ok)
        self.assertIn('browser_sqlite_engine', self.codes(report.errors))
        self.assertIn('sqlite_dependency', self.codes(report.errors))
        self.assertIn('legacy_endpoint', self.codes(report.errors))

    def test_artifact_report_is_clean_for_synthetic_release(self):
        self.staged()
        self.write('core/web_api_evaluate_guest/quickjs.wasm', b'\x00asm')
        artifact = self.web_artifact()
        trace = self.write('trace.json', json.dumps([
            '/', '/api/v1/profiles', '/api/auth/session', '/ws/v1/events',
            '/main.dart.js', '/assets/AssetManifest.json',
        ]))
        report = validator.validate_artifact(
            staged=self.root,
            web=artifact,
            deps='package:flutter/material.dart\n',
            trace=trace,
            origin='https://panel.test',
        )
        self.assertTrue(report.ok, [finding.as_dict() for finding in report.findings])
        self.assertEqual([], report.warnings)

    def test_cli_reports_findings_and_exit_codes(self):
        self.staged()
        self.write('web/main.js', "fetch('/api/http-proxy');\n")
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            status = validator.main(['--staged', str(self.root), '--json'])
        self.assertEqual(1, status)
        findings = json.loads(output.getvalue())
        self.assertIn('legacy_transport', {finding['code'] for finding in findings})

        trace = self.write('trace.json', json.dumps(['/api/v1/profiles']))
        with contextlib.redirect_stdout(io.StringIO()):
            status = validator.main(['--staged', str(self.root), '--json'])
        self.assertEqual(1, status)
        with contextlib.redirect_stdout(io.StringIO()):
            status = validator.main(['--trace', str(trace)])
        self.assertEqual(0, status)

    def test_cli_rejects_missing_directory(self):
        with contextlib.redirect_stderr(io.StringIO()) as error:
            with self.assertRaises(SystemExit) as raised:
                validator.main(['--staged', str(self.root / 'absent')])
        self.assertEqual(2, raised.exception.code)
        self.assertIn('Not a directory', error.getvalue())


if __name__ == '__main__':
    unittest.main()

#!/usr/bin/env python3
"""Reject persistence and transport escapes in a staged Web tree or release artifact.

The browser artifact must not carry a SQLite engine, a database file, ordinary
SQL, or the removed /api/fs and /api/http-proxy transports. The sqflite family
counts as a SQLite path too, whichever package name pulls it in. A server-side
legacy archive reader may keep the SQLite header as a parser constant; only a
file whose contents start with that header counts as an embedded database. An
unrelated browser runtime such as canvas WASM stays allowed, while a server-only
guest runtime such as QuickJS is rejected from the browser artifact and accepted
only when the validated root is the server side. A staged tree or a stage-2
build is not a release pass: pass the final release artifact explicitly.
"""

import sys

sys.dont_write_bytecode = True
if sys.version_info < (3, 10):
    raise SystemExit('Python 3.10 or newer is required')

import argparse
import json
import re
from dataclasses import dataclass, field
from pathlib import Path
from urllib.parse import urlsplit

ERROR = 'error'
WARNING = 'warning'

DB_MAGIC = b'SQLite format 3\x00'
SQLITE_WASM_NAMES = frozenset({'sqlite3.wasm', 'sqlite.wasm', 'sqljs.wasm', 'sql.js.wasm'})
ALLOWED_BROWSER_WASM = frozenset({
    'canvaskit.wasm', 'canvaskit_skwasm.wasm', 'skwasm.wasm', 'skwasm_heavy.wasm',
})
SERVER_ONLY_WASM_PREFIXES = ('quickjs', 'qjs')
LEGACY_ENDPOINTS = ('/api/fs/', '/api/http-proxy', '/ws/core')

PERSISTENCE_DIRS = ('lib/web_api', 'lib/web_runtime', 'lib/web_shim')
PERSISTENCE_FILES = (
    'lib/database/database.dart',
    'lib/providers/database.dart',
    'lib/common/preferences.dart',
    'lib/common/backup.dart',
    'lib/bootstrap.dart',
    'web/bridge.js',
)

BOUNDARY_PATTERNS = (
    (re.compile(r'package:(?:drift|drift_flutter|sqlite3)/'), 'sqlite_dependency'),
    (re.compile(r'@DriftDatabase|WasmSqlite3|WasmDatabase|NativeDatabase|QueryExecutor|Sqlite3Filename|sqlite3_open'),
     'sqlite_engine'),
    (re.compile(r'\bsqlparser\b', re.IGNORECASE), 'sql_parser'),
    (re.compile(r'\bPRAGMA\s+[A-Za-z_]'), 'ordinary_sql'),
    (re.compile(r'\bCREATE\s+TABLE\b|\bINSERT\s+INTO\b|\bDELETE\s+FROM\b|\bUPDATE\s+[A-Za-z_]\w*\s+SET\b',
                re.IGNORECASE), 'ordinary_sql'),
    (re.compile(r'\bSELECT\b[^;\n]{0,80}?\bFROM\b', re.IGNORECASE), 'ordinary_sql'),
    (re.compile(r'\bXMLHttpRequest\b'), 'synchronous_xhr'),
)

ARTIFACT_TEXT_SUFFIXES = ('.js', '.json', '.html', '.bin', '.txt')
ARTIFACT_TEXT_NAMES = ('AssetManifest', 'assetmanifest')
STAGED_DEPENDENCY_FILES = ('pubspec.yaml', 'pubspec.lock')
FORBIDDEN_STAGED_IMPORTS = (
    (re.compile(r'package:(?:drift|sqlite3)/'), 'sqlite_dependency'),
    (re.compile(r'package:sqflite(?:_common(?:_ffi(?:_web)?)?|_web)?/'), 'sqflite_dependency'),
    (re.compile(r'package:flutter_cache_manager/'), 'cache_manager_dependency'),
)
SQFLITE_NAMES = re.compile(
    r'\b(?:sqflite|sqflite_common|sqflite_common_ffi|sqflite_common_ffi_web|sqflite_web)\b')
CACHE_MANAGER_NAME = re.compile(r'\bflutter_cache_manager\b')
APPROVED_PATHS = (
    '/api/v1/', '/api/auth/', '/ws/v1/events', '/assets/', '/canvaskit/',
    '/icons/', '/main.dart.js', '/flutter.js', '/flutter_bootstrap.js',
    '/flutter_service_worker.js', '/version.json', '/manifest.json', '/favicon',
)


@dataclass(frozen=True)
class Finding:
    severity: str
    code: str
    path: str
    detail: str

    def as_dict(self):
        return {
            'severity': self.severity,
            'code': self.code,
            'path': self.path,
            'detail': self.detail,
        }


@dataclass
class Report:
    findings: list = field(default_factory=list)

    def add(self, severity, code, path, detail):
        self.findings.append(Finding(severity, code, str(path), detail))

    def extend(self, findings):
        self.findings.extend(findings)

    @property
    def errors(self):
        return [finding for finding in self.findings if finding.severity == ERROR]

    @property
    def warnings(self):
        return [finding for finding in self.findings if finding.severity == WARNING]

    @property
    def ok(self):
        return not self.errors


def _read_text(path):
    try:
        return path.read_bytes().decode('utf-8', errors='ignore')
    except OSError as error:
        raise ValueError(f'Cannot read {path}: {error}') from error


def _files(root):
    root = Path(root)
    if not root.is_dir():
        raise ValueError(f'Not a directory: {root}')
    return sorted(path for path in root.rglob('*') if path.is_file())


def _relative(path, root):
    try:
        return Path(path).relative_to(Path(root)).as_posix()
    except ValueError:
        return str(path)


def _scan_db_magic(report, root):
    for path in _files(root):
        try:
            with path.open('rb') as handle:
                head = handle.read(len(DB_MAGIC))
        except OSError as error:
            report.add(ERROR, 'unreadable_file', _relative(path, root), str(error))
            continue
        if head == DB_MAGIC:
            report.add(ERROR, 'database_file', _relative(path, root),
                       'file starts with the SQLite database header')


def _scan_wasm(report, root, *, browser, paths=None):
    for path in (paths if paths is not None else _files(root)):
        name = path.name.lower()
        if name in SQLITE_WASM_NAMES or (name.startswith('sqlite') and name.endswith('.wasm')):
            report.add(ERROR, 'browser_sqlite_engine' if browser else 'sqlite_engine',
                       _relative(path, root), 'SQLite WASM must never ship')
            continue
        if not name.endswith('.wasm'):
            continue
        if name in ALLOWED_BROWSER_WASM:
            continue
        if name.startswith(SERVER_ONLY_WASM_PREFIXES):
            if browser:
                report.add(ERROR, 'server_only_wasm_in_browser', _relative(path, root),
                           'a guest runtime is server-only and must not be in the browser artifact')
            continue
        report.add(WARNING, 'unexpected_wasm', _relative(path, root),
                   'WASM is not broadly banned; review this browser runtime')


def _scan_legacy_transport(report, root):
    for path in _files(root):
        if path.suffix.lower() not in ('.dart', '.js', '.tmpl', '.json', '.html', '.yaml', '.pubspec'):
            continue
        text = _read_text(path)
        for endpoint in LEGACY_ENDPOINTS:
            if endpoint in text:
                report.add(ERROR, 'legacy_transport', _relative(path, root),
                           f'removed transport endpoint {endpoint}')
        if 'sqlite3.wasm' in text:
            report.add(ERROR, 'sqlite_engine', _relative(path, root),
                       'the staged tree must not reference sqlite3.wasm')


def _is_boundary(relative):
    if relative in PERSISTENCE_FILES:
        return True
    return any(relative.startswith(f'{directory}/') for directory in PERSISTENCE_DIRS)


def validate_staged_source(root):
    """Scan a staged Web source tree before it is compiled.

    The staged root is also the backend build input, so `core/` is the server
    component: a server-only guest runtime there is allowed, while SQLite stays
    forbidden. Everything outside `core/` is browser material and keeps the
    strict guest-runtime rule. The final `validate_web_artifact` check remains
    strict over the whole browser release.
    """
    root = Path(root)
    report = Report()
    files = _files(root)
    relatives = {path: _relative(path, root) for path in files}
    server_component = [path for path in files
                        if relatives[path] == 'core' or relatives[path].startswith('core/')]
    server_set = set(server_component)
    browser_component = [path for path in files if path not in server_set]
    _scan_db_magic(report, root)
    _scan_wasm(report, root, browser=False, paths=server_component)
    _scan_wasm(report, root, browser=True, paths=browser_component)
    _scan_legacy_transport(report, root)
    for path in _files(root):
        if path.suffix.lower() != '.dart':
            continue
        relative = _relative(path, root)
        text = _read_text(path)
        for pattern, code in FORBIDDEN_STAGED_IMPORTS:
            match = pattern.search(text)
            if match:
                report.add(ERROR, code, relative, f'matched {match.group(0)!r}')
    for relative in STAGED_DEPENDENCY_FILES:
        path = root / relative
        if not path.is_file():
            continue
        text = _read_text(path)
        lockfile = relative == 'pubspec.lock'
        match = SQFLITE_NAMES.search(text)
        if match:
            if lockfile:
                report.add(ERROR, 'staged_lockfile_sqflite', relative,
                           f'matched {match.group(0)!r}; regenerate it with flutter pub get before validating')
            else:
                report.add(ERROR, 'staged_sqflite_dependency', relative,
                           f'matched {match.group(0)!r}; the Web tree must not resolve the sqflite family')
        match = CACHE_MANAGER_NAME.search(text)
        if match:
            if lockfile:
                report.add(ERROR, 'staged_lockfile_cache_manager', relative,
                           f'matched {match.group(0)!r}; regenerate it with flutter pub get before validating')
            else:
                report.add(ERROR, 'staged_cache_manager_dependency', relative,
                           f'matched {match.group(0)!r}; its legacy icon helper pulls sqflite')
    for path in _files(root):
        if path.suffix.lower() not in ('.dart', '.js'):
            continue
        relative = _relative(path, root)
        if not _is_boundary(relative):
            continue
        text = _read_text(path)
        for pattern, code in BOUNDARY_PATTERNS:
            match = pattern.search(text)
            if match:
                report.add(ERROR, code, relative, f'matched {match.group(0)!r}')
    database = root / 'lib/database/database.dart'
    if database.is_file():
        text = _read_text(database)
        forbidden = re.search(r'package:(?:drift|sqlite3)|@DriftDatabase|QueryExecutor|\bpart\s', text)
        if forbidden:
            report.add(ERROR, 'native_database_staged', 'lib/database/database.dart',
                       f'matched {forbidden.group(0)!r}')
    drift_shim = root / 'lib/web_shim/drift_native.dart'
    if drift_shim.exists():
        report.add(ERROR, 'native_database_staged', 'lib/web_shim/drift_native.dart',
                   'the SQLite-backed Drift shim must be removed while staging')
    return report.findings


def _scan_artifact_text(report, root):
    for path in _files(root):
        name = path.name
        lower = name.lower()
        textual = path.suffix.lower() in ARTIFACT_TEXT_SUFFIXES or any(
            marker.lower() in lower for marker in ARTIFACT_TEXT_NAMES)
        if not textual:
            continue
        try:
            data = path.read_bytes()
        except OSError as error:
            report.add(ERROR, 'unreadable_file', _relative(path, root), str(error))
            continue
        relative = _relative(path, root)
        for endpoint in LEGACY_ENDPOINTS:
            if endpoint.encode() in data:
                report.add(ERROR, 'legacy_transport', relative, f'removed transport endpoint {endpoint}')
        if b'sqlite3.wasm' in data:
            report.add(ERROR, 'browser_sqlite_engine', relative, 'artifact references sqlite3.wasm')
        if b'WasmSqlite3' in data or b'SQLite format 3\x00' in data:
            report.add(ERROR, 'sqlite_engine', relative, 'artifact embeds the SQLite engine or a database')
        if b'sqflite' in data:
            report.add(ERROR, 'sqflite_engine', relative, 'artifact embeds the sqflite SQLite family')
        if b'flutter_cache_manager' in data:
            report.add(ERROR, 'cache_manager_in_browser', relative,
                       'the cache manager pulls sqflite into the browser artifact')
        if ('assetmanifest' in lower) and b'sqlite' in data.lower():
            report.add(WARNING, 'asset_manifest_review', relative,
                       'an asset manifest entry mentions sqlite; confirm it is not an engine')


def validate_web_artifact(root):
    """Scan the compiled browser release (build/web or its embedded copy)."""
    root = Path(root)
    report = Report()
    if not root.is_dir():
        raise ValueError(f'Not a directory: {root}')
    _scan_db_magic(report, root)
    _scan_wasm(report, root, browser=True)
    _scan_artifact_text(report, root)
    return report.findings


def validate_server_artifact(root):
    """Scan the server-side release; a server-only guest runtime is allowed here."""
    root = Path(root)
    report = Report()
    if root.is_file():
        try:
            data = root.read_bytes()
        except OSError as error:
            raise ValueError(f'Cannot read {root}: {error}') from error
        if data.startswith(DB_MAGIC):
            report.add(ERROR, 'database_file', root.name, 'binary starts with a SQLite database header')
        for endpoint in LEGACY_ENDPOINTS:
            if endpoint.encode() in data:
                report.add(ERROR, 'legacy_transport', root.name, f'removed transport endpoint {endpoint}')
        if b'sqlite3.wasm' in data:
            report.add(ERROR, 'sqlite_engine', root.name, 'binary references sqlite3.wasm')
        if b'sqflite' in data:
            report.add(ERROR, 'sqflite_engine', root.name, 'binary embeds the sqflite SQLite family')
        if b'flutter_cache_manager' in data:
            report.add(ERROR, 'cache_manager_in_browser', root.name,
                       'the cache manager pulls sqflite into the embedded browser bundle')
        return report.findings
    _scan_db_magic(report, root)
    _scan_wasm(report, root, browser=False)
    _scan_legacy_transport(report, root)
    _scan_artifact_text(report, root)
    return report.findings


def _dependency_text(deps):
    if isinstance(deps, Path):
        return deps.read_text()
    if isinstance(deps, str) and '\n' not in deps and len(deps) < 1024:
        try:
            path = Path(deps)
            if path.is_file():
                return path.read_text()
        except OSError:
            pass
    return deps if isinstance(deps, str) else str(deps)


def validate_deps(deps, name='deps'):
    """Scan a dart2js/go dependency listing or module list for SQLite packages."""
    text = _dependency_text(deps)
    report = Report()
    for pattern, code in (
        (r'package:(?:sqlite3|drift|drift_flutter)/', 'sqlite_dependency'),
        (r'package:sqflite(?:_common(?:_ffi(?:_web)?)?|_web)?/|'
         r'\b(?:sqflite|sqflite_common|sqflite_common_ffi|sqflite_common_ffi_web|sqflite_web)\b',
         'sqflite_dependency'),
        (r'sqlparser', 'sql_parser'),
        (r'\bsqlite3\.wasm\b', 'sqlite_engine'),
    ):
        match = re.search(pattern, text)
        if match:
            report.add(ERROR, code, name, f'matched {match.group(0)!r}')
    match = CACHE_MANAGER_NAME.search(text)
    if match:
        report.add(WARNING, 'cache_manager_dependency', name,
                   f'matched {match.group(0)!r}; the Web cache replacement must drop it')
    return report.findings


def _classify_url(url, origin):
    parts = urlsplit(url)
    if parts.scheme in ('blob', 'data'):
        return None
    if parts.scheme in ('http', 'https'):
        if not origin or f'{parts.scheme}://{parts.netloc}' != origin.rstrip('/'):
            return ('cross_origin_request', url, 'the browser artifact may only reach its own origin')
        path = parts.path
    elif parts.scheme or url.startswith('//'):
        return ('unexpected_request', url, 'unsupported request scheme')
    else:
        path = parts.path or url
    for endpoint in LEGACY_ENDPOINTS:
        if path.startswith(endpoint):
            return ('legacy_endpoint', url, f'removed transport endpoint {endpoint}')
    if path.startswith('/api/') and not path.startswith(('/api/v1/', '/api/auth/')):
        return ('unapproved_api_route', url, 'only typed /api/v1 and /api/auth routes are approved')
    if path.startswith('/ws/') and path != '/ws/v1/events':
        return ('unapproved_ws_route', url, 'only /ws/v1/events is approved')
    if path.startswith(APPROVED_PATHS) or path == '/':
        return None
    return ('unexpected_request', url, 'request is outside the approved API and static surface')


def validate_network_trace(entries, origin=''):
    """Check captured browser requests; entries are URLs or {'url': ...} maps."""
    report = Report()
    for index, entry in enumerate(entries):
        if isinstance(entry, dict):
            request = entry.get('request')
            url = entry.get('url')
            if url is None and isinstance(request, dict):
                url = request.get('url')
        else:
            url = entry
        if not isinstance(url, str) or not url:
            report.add(ERROR, 'unreadable_trace_entry', f'trace[{index}]', 'missing url')
            continue
        finding = _classify_url(url, origin)
        if finding:
            code, target, detail = finding
            report.add(ERROR, code, f'trace[{index}]', f'{target}: {detail}')
    return report.findings


def _trace_document(data):
    if isinstance(data, dict):
        log = data.get('log')
        if isinstance(log, dict) and isinstance(log.get('entries'), list):
            return log['entries']
        if isinstance(data.get('entries'), list):
            return data['entries']
    return data


def _trace_entries(trace):
    if isinstance(trace, (list, tuple)):
        return _trace_document(list(trace))
    if isinstance(trace, Path):
        return _trace_document(json.loads(trace.read_text()))
    if isinstance(trace, str):
        try:
            path = Path(trace)
            if path.is_file():
                return _trace_document(json.loads(path.read_text()))
        except OSError:
            pass
        raise ValueError('trace must be a JSON file or a list of URLs')
    return _trace_document(list(trace))


def validate_artifact(*, staged=None, web=None, server=None, deps=None, trace=None, origin=''):
    """Aggregate every check; a clean report has no ERROR findings."""
    report = Report()
    if staged:
        report.extend(validate_staged_source(staged))
    if web:
        report.extend(validate_web_artifact(web))
    if server:
        report.extend(validate_server_artifact(server))
    if deps:
        report.extend(validate_deps(deps))
    if trace:
        report.extend(validate_network_trace(_trace_entries(trace), origin=origin))
    return report


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--staged', type=Path, help='staged Web source tree')
    parser.add_argument('--web', type=Path, help='compiled browser release (build/web)')
    parser.add_argument('--server', type=Path, help='server binary or server-side release tree')
    parser.add_argument('--deps', help='dependency listing, .deps file or .dart_tool/package_config.json')
    parser.add_argument('--trace', type=Path, help='JSON array of captured request URLs')
    parser.add_argument('--origin', default='', help='browser origin allowed in the trace')
    parser.add_argument('--json', action='store_true', help='print findings as JSON')
    args = parser.parse_args(argv)
    try:
        report = validate_artifact(
            staged=args.staged, web=args.web, server=args.server,
            deps=args.deps, trace=args.trace, origin=args.origin,
        )
    except ValueError as error:
        parser.exit(2, f'Artifact validation failed: {error}\n')
    if args.json:
        print(json.dumps([finding.as_dict() for finding in report.findings], indent=2))
    else:
        for finding in report.findings:
            print(f'{finding.severity}: {finding.code}: {finding.path}: {finding.detail}')
        print(f'{len(report.errors)} error(s), {len(report.warnings)} warning(s)')
    return 0 if report.ok else 1


if __name__ == '__main__':
    raise SystemExit(main())

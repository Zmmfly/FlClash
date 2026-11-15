"""Select API repositories only in the disposable Web source tree."""

import re
from pathlib import Path


API_REPLACEMENTS = {
    'lib/database/database.dart': '@DriftDatabase(',
    'lib/providers/database.dart': 'mixin OptimisticMixin',
    'lib/providers/config.dart': 'Config _config(',
    'lib/common/preferences.dart': 'class Preferences',
    'lib/common/javascript.dart': 'ScriptEvaluator scriptEvaluator = evaluateScript;',
    'lib/common/ip_quality.dart': 'Future<IpQuality> lookupIpQuality(\n  Dio dio,\n  String ip, {',
    'lib/common/app_data.dart': 'Future<void> clearAppData()',
    'lib/common/backup.dart': 'package:drift/drift.dart',
    'lib/common/migration.dart': 'class Migration',
    'lib/providers/actions/profiles.dart': 'class ProfilesAction',
    'lib/providers/actions/proxies.dart': 'class ProxiesAction',
    'lib/providers/actions/clash_providers.dart': 'class ClashProvidersAction',
    'lib/providers/actions/mitm_modules.dart': 'class MitmModulesAction',
    'lib/providers/actions/resources.dart': 'class ResourcesAction',
    'lib/providers/actions/backup.dart': 'class BackupAction',
    'lib/providers/actions/store.dart': 'class StoreAction',
}

API_REQUIRED_OVERLAYS = (
    'lib/database/database.dart',
    'lib/providers/database.dart',
    'lib/providers/config.dart',
    'lib/common/preferences.dart',
    'lib/web_api/index.dart',
    'lib/web_runtime/runtime.dart',
    'lib/web_evaluate/index.dart',
    'lib/web_utilities/index.dart',
    'lib/web_scheduler/index.dart',
    'lib/web_api_recovery/index.dart',
)


def check_api_overlay(root, relative, error):
    anchor = API_REPLACEMENTS.get(relative.as_posix())
    if anchor is None:
        return False
    target = root / relative
    if not target.is_file() or anchor not in target.read_text():
        raise error(f'{relative}: missing native API replacement anchor: {anchor!r}')
    if relative.as_posix() == 'lib/common/ip_quality.dart' and (
            'class IpQualityLookupException implements Exception' not in target.read_text()):
        raise error('Missing native IP-quality exception contract')
    return True


def adapt_api(root, replace_exact, error):
    for relative in API_REQUIRED_OVERLAYS:
        if not (root / relative).is_file():
            raise error(f'Missing required typed API overlay: {relative}')
    database = (root / 'lib/database/database.dart').read_text()
    if re.search(r'package:(?:drift|sqlite3)|@DriftDatabase|QueryExecutor|part\s', database):
        raise error('The Web database entrypoint must be a pure DTO API repository')
    for entry in (root / 'lib/database').iterdir():
        if entry.name == 'database.dart':
            continue
        if entry.is_dir():
            import shutil
            shutil.rmtree(entry)
        else:
            entry.unlink()
    pubspec = root / 'pubspec.yaml'
    text = pubspec.read_text()
    text, count = re.subn(r'^  (?:drift|drift_flutter|drift_dev|sqlite3):[^\n]*\n', '', text, flags=re.M)
    if count != 4:
        raise error(f'Expected four native SQLite dependency anchors, found {count}')
    text, cache_count = re.subn(r'^  flutter_cache_manager:[^\n]*\n', '', text, flags=re.M)
    if cache_count != 1:
        raise error(f'Expected the native image cache dependency, found {cache_count}')
    pubspec.write_text(text)
    replace_exact(root, 'lib/bootstrap.dart',
                  '''  Future<void> _claimDataDirectory() async {
    if (!system.isDesktop || await singleInstanceLock.acquire()) {
      return;
    }
    commonPrint.log('another instance owns the data directory, exiting');
    exit(0);
  }
''', '')
    replace_exact(root, 'lib/common/snowflake.dart',
                  (root / 'lib/common/snowflake.dart').read_text(),
                  "export 'package:fl_clash/web_api/draft_ids.dart';\n")
    replace_exact(root, 'lib/bootstrap.dart',
                  "import 'package:fl_clash/database/database.dart';",
                  "import 'package:fl_clash/database/database.dart';\n"
                  "import 'package:fl_clash/web_api/index.dart';\n"
                  "import 'package:fl_clash/web_runtime/runtime.dart';\n"
                  "import 'package:fl_clash/web_workflows/index.dart';\n"
                  "import 'package:fl_clash/web_utilities/index.dart';\n"
                  "import 'package:fl_clash/web_scheduler/index.dart';\n"
                  "import 'package:fl_clash/web_settings/providers.dart';\n"
                  "import 'package:fl_clash/web_api_recovery/index.dart';")
    replace_exact(root, 'lib/bootstrap.dart',
                  '    await _claimDataDirectory();\n    globalState.packageInfo = await PackageInfo.fromPlatform();',
                  '    final settings = webSettings;\n'
                  '    registerWebWorkflowReceipts(webApiRepository);\n'
                  '    registerWebUtilityReceipts(webApiRepository);\n'
                  '    registerWebSchedulerReceipts(webApiRepository);\n'
                  '    WebRuntimeBinding.configure(webApiRepository);\n'
                  '    webApiRepository.configureProfilePresentation(\n'
                  '      read: settings.presentation.readProfileUnfoldSet,\n'
                  '      write: settings.presentation.saveProfileUnfoldSet,\n'
                  '    );\n'
                  '    await webApiRepository.initialize();\n'
                  '    await waitForWebApiRecovery(webApiRepository);\n'
                  '    globalState.packageInfo = await PackageInfo.fromPlatform();')
    replace_exact(root, 'lib/bootstrap.dart',
                  '    container.read(profilesProvider.notifier).reorder(profiles);',
                  '    container.read(profilesProvider.notifier).adoptCommitted(profiles);')
    replace_exact(root, 'lib/bootstrap.dart',
                  '    final patchClashConfig = config.patchClashConfig.ensureControllerSecret();',
                  '    final patchClashConfig = config.patchClashConfig;')
    replace_exact(root, 'lib/core/interface.dart',
                  "'Invoke method ${method.wireName} ${DateTime.now()} $arguments',",
                  "'Invoke method ${method.wireName} ${DateTime.now()}',")
    wasm = root / 'web/sqlite3.wasm'
    wasm.unlink(missing_ok=True)
    shim = root / 'lib/web_shim/drift_native.dart'
    shim.unlink(missing_ok=True)


def validate_api_sources(root, error):
    forbidden = re.compile(r'package:(?:drift|drift_flutter|sqlite3)/|/api/fs/|/api/http-proxy|WasmSqlite3|WasmDatabase|NativeDatabase|QueryExecutor')
    found = []
    for relative in ('lib/database/database.dart', 'lib/providers/database.dart',
                     'lib/web_shim/vfs.dart', 'lib/web_shim/dio_io.dart',
                     'lib/web_shim/dio.dart'):
        path = root / relative
        if path.is_file() and forbidden.search(path.read_text()):
            found.append(relative)
    if found:
        raise error('Legacy persistence or relay in API Web sources: ' + ', '.join(found))
    if (root / 'web/sqlite3.wasm').exists():
        raise error('SQLite engine must not be staged for Web')

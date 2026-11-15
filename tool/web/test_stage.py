import hashlib
import json
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import build
import stage
from validate_artifact import ERROR, Finding, Report


# The build's artifact gates stay real; this fixture only stands in for the pub
# resolution the Web tree would get after the cache manager is removed.
def write_mock_pub_dependencies(cwd):
    root = Path(cwd)
    (root / '.dart_tool').mkdir(parents=True, exist_ok=True)
    (root / '.dart_tool/package_config.json').write_text(
        json.dumps({'configVersion': 2, 'packages': []}) + '\n')
    (root / 'pubspec.lock').write_text(
        '# Mock resolution for the Web dependency set.\n'
        'packages:\n'
        '  path:\n'
        '    dependency: "direct main"\n'
        '    description:\n'
        '      name: path\n'
        '      url: "https://pub.dev"\n'
        '    source: hosted\n'
        '    version: "1.9.1"\n'
        'sdks:\n'
        '  dart: ">=3.13.0 <4.0.0"\n')


# WEBPANEL.md and README Web build claims are exercised through staging and build entrypoints.
class WebStagingTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temporary = tempfile.TemporaryDirectory()
        cls.base = Path(cls.temporary.name)
        cls.source = cls.base / 'checkout'
        cls.source.mkdir()
        for directory in ('lib', 'plugins', 'web', 'arb'):
            shutil.copytree(stage.ROOT / directory, cls.source / directory, ignore=stage.copy_ignore)
        for directory in ('assets', 'core/Clash.Meta', 'core/cmd/flclash-web/web_dist'):
            (cls.source / directory).mkdir(parents=True, exist_ok=True)
        for relative in stage.COPY_FILES:
            shutil.copyfile(stage.ROOT / relative, cls.source / relative)
        shutil.copytree(stage.ROOT / 'core/webui', cls.source / 'core/webui')
        (cls.source / 'core/go.mod').write_text('module core\ngo 1.27.0\n')
        (cls.source / 'core/Clash.Meta/go.mod').write_text('module mihomo\ngo 1.27.0\n')
        (cls.source / 'core/cmd/flclash-web/main.go').write_text('package main\n')
        (cls.source / 'core/cmd/flclash-web/web_dist/.gitkeep').touch()
        cls.sequence = 0

    @classmethod
    def tearDownClass(cls):
        cls.temporary.cleanup()

    def output(self):
        type(self).sequence += 1
        return self.base / f'output-{self.sequence}'

    def digest(self, root):
        return {str(file.relative_to(root)): hashlib.sha256(file.read_bytes()).hexdigest()
                for file in root.rglob('*') if file.is_file()}

    def digest_sources(self):
        return {key: value for key, value in self.digest(self.source).items()
                if not key.startswith(('dist/', 'build/'))}

    def test_stage_preserves_native_and_generated_sources_and_controller_default(self):
        before = self.digest(self.source)
        output = stage.stage(self.source, self.output())
        self.assertEqual(before, self.digest(self.source))
        self.assertIn("package:fl_clash/web_shim/dart_io.dart", (output / 'lib/plugins/app.dart').read_text())
        self.assertIn('MemoryImage(File(path).readAsBytesSync())', (output / 'lib/plugins/app.dart').read_text())
        self.assertIn("'127.0.0.1:9090'", (output / 'lib/enum/enum.dart').read_text())
        self.assertNotIn("'0.0.0.0:9090'", (output / 'lib/enum/enum.dart').read_text())
        self.assertIn('ui.Clip.antiAlias', (output / 'plugins/prism_ui/lib/liquid_glass.dart').read_text())
        self.assertIn('config.setup', (output / 'plugins/rust_api/lib/rust_api.dart').read_text())
        self.assertNotIn('"setupConfig"', (output / 'plugins/rust_api/lib/rust_api.dart').read_text())
        database_generated = self.source / 'lib/database/generated'
        self.assertTrue(database_generated.is_dir(),
                        'The native database generated part must exist to be excluded')
        for file in (self.source / 'lib').rglob('*.dart'):
            if not stage.generated(file):
                continue
            relative = file.relative_to(self.source)
            if file.is_relative_to(database_generated):
                self.assertFalse((output / relative).exists(),
                                 f'native database generated part survived staging: {relative}')
                continue
            self.assertEqual(file.read_bytes(), (output / relative).read_bytes(),
                             f'generated source changed outside the excluded database part: {relative}')
        for part in (self.source / 'lib/database').rglob('*.dart'):
            relative = part.relative_to(self.source)
            if relative.as_posix() == 'lib/database/database.dart':
                continue
            self.assertFalse((output / relative).exists(),
                             f'native database part survived staging: {relative}')
        exempted = {file.relative_to(self.source).as_posix()
                    for file in database_generated.rglob('*') if file.is_file()}
        missing_generated = {file.relative_to(self.source).as_posix()
                             for file in (self.source / 'lib').rglob('*.dart')
                             if stage.generated(file) and not (output / file.relative_to(self.source)).exists()}
        self.assertEqual(exempted, missing_generated,
                         'Only the staged native database generated part may be excluded')
        staged_database = (output / 'lib/database/database.dart').read_text()
        self.assertNotIn('@DriftDatabase', staged_database)
        self.assertNotIn('package:drift/', staged_database)
        self.assertNotIn('QueryExecutor', staged_database)
        self.assertFalse((output / 'lib/web_shim/drift_native.dart').exists())
        self.assertFalse((output / 'web/sqlite3.wasm').exists())
        self.assertFalse((output / 'core/cmd/flclash-web/web_dist').exists())
        self.assertFalse((output / 'tool').exists())
        self.assertFalse((self.source / 'lib/web_auth').exists())
        self.assertNotIn('WebLogoutScope', (self.source / 'lib/application.dart').read_text())
        self.assertIn('child: WebLogoutScope(child: child!)', (output / 'lib/application.dart').read_text())
        self.assertNotIn('footerAction', (self.source / 'plugins/prism_ui/lib/sidebar.dart').read_text())
        self.assertIn('footerAction: webLogoutNavigationAction(context)',
                      (output / 'lib/manager/app_manager.dart').read_text())
        self.assertIn('action: navigationAction', (output / 'lib/pages/home.dart').read_text())
        self.assertIn('hasAction: navigationAction != null', (output / 'lib/pages/home.dart').read_text())
        self.assertNotIn("ValueKey('web-log-out')", (output / 'lib/web_auth/account_view.dart').read_text())
        for relative in ('plugins/prism_ui/lib/sidebar.dart', 'plugins/prism_ui/lib/navigation_dock.dart',
                         'plugins/prism_ui/lib/navigation_bar.dart'):
            self.assertIn('NavigationAction?', (output / relative).read_text())
        self.assertNotIn('WebAccountItem', (self.source / 'lib/views/tools.dart').read_text())
        self.assertIn('if (webPanelBuild) const WebAccountItem()', (output / 'lib/views/tools.dart').read_text())
        self.assertIn("kIsWeb && bool.fromEnvironment('WEB_PANEL')",
                      (output / 'lib/web_auth/build_flag.dart').read_text())
        for locale in ('en', 'zh_CN', 'ja', 'ru', 'fr', 'es', 'ko'):
            native = json.loads((self.source / 'arb' / f'intl_{locale}.arb').read_text())
            staged = json.loads((output / 'arb' / f'intl_{locale}.arb').read_text())
            web = json.loads((self.source / 'core/webui' / f'intl_{locale}.arb').read_text())
            self.assertFalse(set(native) & set(web))
            self.assertEqual(native, {key: staged[key] for key in native})
            self.assertEqual(web, {key: staged[key] for key in web})
        self.assertTrue(json.loads((output / '.webpanel-stage.json').read_text())['import_rewrites'])

    def test_staged_bootstrap_wires_scheduler_import_and_registers_before_initialize(self):
        output = stage.stage(self.source, self.output())
        bootstrap = (output / 'lib/bootstrap.dart').read_text()
        lines = [line.strip() for line in bootstrap.splitlines()]
        # Stage4 shipped a required overlay without its bootstrap import.
        for line in (
            "import 'package:fl_clash/web_workflows/index.dart';",
            "import 'package:fl_clash/web_utilities/index.dart';",
            "import 'package:fl_clash/web_scheduler/index.dart';",
            "import 'package:fl_clash/web_settings/providers.dart';",
            "import 'package:fl_clash/web_api_recovery/index.dart';",
        ):
            self.assertEqual(1, lines.count(line), f'missing or duplicated staged import: {line}')
        last_import = max(index for index, line in enumerate(lines) if line.startswith('import '))
        self.assertLessEqual(lines.index("import 'package:fl_clash/web_scheduler/index.dart';"),
                             last_import, 'web_scheduler import must stay in the import block')

        def position(token):
            self.assertEqual(1, bootstrap.count(token), f'expected exactly one {token}')
            return bootstrap.index(token)

        settings = position('final settings = webSettings;')
        workflow = position('registerWebWorkflowReceipts(webApiRepository);')
        utility = position('registerWebUtilityReceipts(webApiRepository);')
        scheduler = position('registerWebSchedulerReceipts(webApiRepository);')
        runtime = position('WebRuntimeBinding.configure(webApiRepository);')
        initialize = position('await webApiRepository.initialize();')
        recovery = position('await waitForWebApiRecovery(webApiRepository);')
        migration = position('await migration.run();')
        profile_query = position('database.profilesDao.query()')
        for earlier, later in ((settings, workflow), (workflow, utility),
                               (utility, scheduler), (scheduler, runtime),
                               (runtime, initialize), (initialize, recovery),
                               (recovery, migration), (recovery, profile_query)):
            self.assertLess(earlier, later)
        self.assertNotIn('_claimDataDirectory', bootstrap)

    def test_missing_anchor_fails_without_partial_output_or_source_mutation(self):
        file = self.source / 'lib/plugins/app.dart'
        original = file.read_text()
        output = self.output()
        try:
            file.write_text(original.replace('return FileImage(File(path));', 'return changed;'))
            before = self.digest(self.source)
            with self.assertRaisesRegex(stage.StageError, 'adaptation anchor'):
                stage.stage(self.source, output)
            self.assertEqual(before, self.digest(self.source))
            self.assertFalse(output.exists())
            self.assertEqual([], list(self.base.glob('.webpanel-stage-*')))
        finally:
            file.write_text(original)

    def test_navigation_scope_anchor_is_strict_and_never_changes_native_checkout(self):
        file = self.source / 'lib/application.dart'
        original = file.read_text()
        output = self.output()
        try:
            file.write_text(original.replace('              terminateSignals: _terminateSignals,',
                                             '              terminateSignals: changedSignal,'))
            before = self.digest(self.source)
            with self.assertRaisesRegex(stage.StageError, 'lib/application.dart'):
                stage.stage(self.source, output)
            self.assertEqual(before, self.digest(self.source))
            self.assertFalse(output.exists())
        finally:
            file.write_text(original)

    def test_repeat_build_never_overwrites_user_directory(self):
        output = stage.stage(self.source, self.output())
        marker = output / 'user-file'
        marker.write_text('keep me')
        before = self.digest(output)
        with self.assertRaisesRegex(stage.StageError, 'overwrite existing'):
            stage.stage(self.source, output)
        self.assertEqual(before, self.digest(output))
        second = stage.stage(self.source, self.output())
        self.assertTrue((second / 'pubspec.yaml').is_file())

    def test_unsafe_outputs_and_symlinks_are_rejected(self):
        for output in (self.source, self.source.parent, self.source / 'lib/new-output'):
            with self.assertRaises(stage.StageError):
                stage.stage(self.source, output)
        target = self.base / 'linked'
        target.symlink_to(self.base, target_is_directory=True)
        try:
            with self.assertRaisesRegex(stage.StageError, 'symlinks'):
                stage.stage(self.source, target / 'unused')
        finally:
            target.unlink()

    def test_build_contract_uses_isolated_go_embed_and_atomic_stable_binary(self):
        calls = []
        def fake_run(arguments, cwd=None, capture=False, env=None):
            calls.append((arguments, cwd, env))
            if arguments[:3] == ['flutter', 'pub', 'get']:
                write_mock_pub_dependencies(cwd)
            if arguments[:3] == ['flutter', 'build', 'web']:
                web = cwd / 'build/web'
                web.mkdir(parents=True)
                (web / 'index.html').write_text('authenticated shell')
                (web / 'main.dart.js').write_text('compiled')
            if arguments[:2] == ['go', 'build']:
                self.assertEqual('authenticated shell', (cwd / 'cmd/flclash-web/web_dist/index.html').read_text())
                Path(arguments[arguments.index('-o') + 1]).write_bytes(b'binary')
        before = self.digest_sources()
        with patch.object(build, 'ROOT', self.source), patch.object(build, 'preflight', return_value=(
            '0.9.3', 'webpanel,with_gvisor', '-w -s', 'v1.19.33',
        )), patch.object(build, 'run', side_effect=fake_run):
            build.build()
            build.build(safe_mode=True)
        native_after = self.digest_sources()
        self.assertEqual(before, {key: native_after[key] for key in before})
        self.assertEqual(b'binary', (self.source / 'dist/FlClash-web').read_bytes())
        go = [call for call in calls if call[0][:2] == ['go', 'build']]
        self.assertEqual(2, len(go))
        flutter = [call[0] for call in calls if call[0][:3] == ['flutter', 'build', 'web']]
        self.assertNotIn('--dart-define=SAFE_MODE=true', flutter[0])
        self.assertIn('--dart-define=SAFE_MODE=true', flutter[1])
        self.assertTrue(all('--dart-define=WEB_PANEL=true' in arguments for arguments in flutter))
        self.assertEqual(2, sum(arguments == ['dart', 'run', 'intl_utils:generate'] for arguments, _, _ in calls))
        for arguments, cwd, env in go:
            self.assertEqual('webpanel,with_gvisor', arguments[arguments.index('-tags') + 1])
            self.assertEqual('0', env['CGO_ENABLED'])
            self.assertEqual('./cmd/flclash-web', arguments[-1])
            self.assertNotEqual(cwd, self.source / 'core')
        self.assertEqual(b'', (self.source / 'core/cmd/flclash-web/web_dist/.gitkeep').read_bytes())

    def test_failed_flutter_build_preserves_native_sources_and_existing_binary(self):
        dist = self.source / 'dist'
        dist.mkdir(exist_ok=True)
        binary = dist / 'FlClash-web'
        binary.write_bytes(b'previous-release')
        before = self.digest(self.source / 'lib')
        pubspec = (self.source / 'pubspec.yaml').read_bytes()
        def failed_run(arguments, **kwargs):
            if arguments[:3] == ['flutter', 'pub', 'get']:
                write_mock_pub_dependencies(kwargs.get('cwd'))
            if arguments[:3] == ['flutter', 'build', 'web']:
                raise subprocess.CalledProcessError(1, arguments)
        with patch.object(build, 'ROOT', self.source), patch.object(build, 'preflight', return_value=(
            '0.9.3', 'webpanel,with_gvisor', '-w -s', 'v1.19.33',
        )), patch.object(build, 'run', side_effect=failed_run):
            with self.assertRaises(subprocess.CalledProcessError):
                build.build()
        self.assertEqual(before, self.digest(self.source / 'lib'))
        self.assertEqual(pubspec, (self.source / 'pubspec.yaml').read_bytes())
        self.assertEqual(b'previous-release', binary.read_bytes())
        self.assertEqual(b'', (self.source / 'core/cmd/flclash-web/web_dist/.gitkeep').read_bytes())

    def test_artifact_validation_failure_preserves_previous_binary_and_native_sources(self):
        dist = self.source / 'dist'
        dist.mkdir(exist_ok=True)
        binary = dist / 'FlClash-web'
        binary.write_bytes(b'previous-release')
        before = self.digest(self.source / 'lib')
        gates = []

        def fake_run(arguments, cwd=None, capture=False, env=None):
            if arguments[:3] == ['flutter', 'pub', 'get']:
                write_mock_pub_dependencies(cwd)
            elif arguments[:3] == ['flutter', 'build', 'web']:
                web = cwd / 'build/web'
                web.mkdir(parents=True, exist_ok=True)
                (web / 'index.html').write_text('authenticated shell')
                (web / 'main.dart.js').write_text('compiled')
            elif arguments[:2] == ['go', 'build']:
                Path(arguments[arguments.index('-o') + 1]).write_bytes(b'rejected-build')

        def fake_validation(**inputs):
            gates.append(sorted(inputs))
            if inputs.get('server') is not None:
                return Report(findings=[
                    Finding(ERROR, 'synthetic_artifact', 'server', 'gate failure'),
                ])
            return Report()

        with patch.object(build, 'ROOT', self.source), patch.object(build, 'preflight', return_value=(
            '0.9.3', 'webpanel,with_gvisor', '-w -s', 'v1.19.33',
        )), patch.object(build, 'run', side_effect=fake_run), \
                patch.object(build, 'validate_artifact', side_effect=fake_validation):
            with self.assertRaisesRegex(stage.StageError, 'synthetic_artifact'):
                build.build()

        self.assertEqual([['deps', 'staged'], ['web'], ['server']], gates)
        self.assertEqual(b'previous-release', binary.read_bytes())
        self.assertEqual([], list(dist.glob('.FlClash-web-*')))
        self.assertEqual(before, self.digest(self.source / 'lib'))

    def test_generated_caching_worker_is_rejected_without_modifying_generated_content(self):
        directory = self.output()
        directory.mkdir()
        worker = directory / 'flutter_service_worker.js'
        for script in (
            '''self.addEventListener('fetch', () => {});''',
            '''caches.open('flutter-cache');''',
            '''const RESOURCES = {};''',
        ):
            worker.write_text(script)
            with self.assertRaisesRegex(stage.StageError, 'caching service worker'):
                build.validate_service_worker(directory)
            self.assertEqual(script, worker.read_text())
        worker.write_text('self.registration.unregister();')
        build.validate_service_worker(directory)

    def test_preflight_selects_go_toolchain_from_core_and_never_downgrades(self):
        def fake_run(arguments, cwd=None, capture=False, env=None):
            if arguments[0] == 'flutter':
                return json.dumps({'frameworkVersion': '3.47.6', 'dartSdkVersion': '3.13.5'})
            self.assertEqual(self.source / 'core', cwd)
            return 'go version go1.27.0 linux/amd64'
        with patch.object(build, 'ROOT', self.source), patch.object(build.shutil, 'which', return_value='/tool'), patch.object(build, 'run', side_effect=fake_run):
            self.assertEqual('webpanel,with_gvisor', build.preflight()[1])
        with patch.object(build, 'ROOT', self.source), patch.object(build.shutil, 'which', return_value='/tool'), patch.object(build, 'run', side_effect=lambda arguments, **kw: fake_run(arguments, **kw) if arguments[0] == 'flutter' else 'go version go1.26.8 linux/amd64'):
            with self.assertRaisesRegex(stage.StageError, 'Go 1.27.0'):
                build.preflight()


if __name__ == '__main__':
    unittest.main()

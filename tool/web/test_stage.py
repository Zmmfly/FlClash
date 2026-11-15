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


# WEBPANEL.md and README Web build claims are exercised through staging and build entrypoints.
class WebStagingTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temporary = tempfile.TemporaryDirectory()
        cls.base = Path(cls.temporary.name)
        cls.source = cls.base / 'checkout'
        cls.source.mkdir()
        for directory in ('lib', 'plugins', 'web'):
            shutil.copytree(stage.ROOT / directory, cls.source / directory, ignore=stage.copy_ignore)
        for directory in ('assets', 'arb', 'core/Clash.Meta', 'core/cmd/flclash-web/web_dist'):
            (cls.source / directory).mkdir(parents=True, exist_ok=True)
        for relative in stage.COPY_FILES:
            shutil.copyfile(stage.ROOT / relative, cls.source / relative)
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
        for file in (self.source / 'lib').rglob('*.dart'):
            if stage.generated(file):
                self.assertEqual(file.read_bytes(), (output / file.relative_to(self.source)).read_bytes())
        self.assertFalse((output / 'core/cmd/flclash-web/web_dist').exists())
        self.assertFalse((output / 'tool').exists())
        self.assertTrue(json.loads((output / '.webpanel-stage.json').read_text())['import_rewrites'])

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
            if arguments[:3] == ['flutter', 'build', 'web']:
                web = cwd / 'build/web'
                web.mkdir(parents=True)
                (web / 'index.html').write_text('authenticated shell')
                (web / 'main.dart.js').write_text('compiled')
            if arguments[:2] == ['go', 'build']:
                self.assertEqual('authenticated shell', (cwd / 'cmd/flclash-web/web_dist/index.html').read_text())
                Path(arguments[arguments.index('-o') + 1]).write_bytes(b'binary')
        before = self.digest(self.source)
        with patch.object(build, 'ROOT', self.source), patch.object(build, 'preflight', return_value=(
            '0.9.3', 'webpanel,with_gvisor', '-w -s', 'v1.19.33',
        )), patch.object(build, 'run', side_effect=fake_run):
            build.build()
            build.build(safe_mode=True)
        native_after = self.digest(self.source)
        self.assertEqual(before, {key: native_after[key] for key in before})
        self.assertEqual(b'binary', (self.source / 'dist/FlClash-web').read_bytes())
        go = [call for call in calls if call[0][:2] == ['go', 'build']]
        self.assertEqual(2, len(go))
        flutter = [call[0] for call in calls if call[0][:3] == ['flutter', 'build', 'web']]
        self.assertNotIn('--dart-define=SAFE_MODE=true', flutter[0])
        self.assertIn('--dart-define=SAFE_MODE=true', flutter[1])
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

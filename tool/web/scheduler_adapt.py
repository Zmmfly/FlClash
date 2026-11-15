"""Replace native auto-update consumers only inside an isolated Web stage."""

from pathlib import Path


def _method(root, replace_exact, relative, signature, replacement, required):
    source = (root / relative).read_text()
    if source.count(signature) != 1:
        raise RuntimeError(f'Scheduler method anchor changed: {relative}')
    start = source.index(signature)
    begin = source.index('{', start)
    depth = 1
    end = begin + 1
    while depth and end < len(source):
        depth += (source[end] == '{') - (source[end] == '}')
        end += 1
    old = source[start:end]
    if depth or any(anchor not in old for anchor in required):
        raise RuntimeError(f'Scheduler method contract changed: {relative}')
    replace_exact(root, relative, old, replacement)


def adapt_scheduler(root, replace_exact):
    root = Path(root).resolve()
    if root == Path(__file__).resolve().parents[2]:
        raise RuntimeError('Scheduler adaptations require an isolated staging directory')
    for relative in ('lib/providers/action.dart', 'lib/application.dart'):
        source = (root / relative).read_text()
        first = next((line for line in source.splitlines() if line.startswith('import ')), None)
        if first is None:
            raise RuntimeError(f'Scheduler import anchor changed: {relative}')
        replace_exact(root, relative, first, first +
                      "\nimport 'package:fl_clash/web_scheduler/index.dart';")
    _method(root, replace_exact, 'lib/providers/actions/profiles.dart',
            '  Future<void> autoUpdateProfiles() async {',
            '  Future<void> autoUpdateProfiles() async {\n'
            '    await webScheduler.hintDue();\n  }',
            ('profile.autoUpdate', 'await updateProfile(profile)'))
    _method(root, replace_exact, 'lib/providers/actions/resources.dart',
            '  Future<void> autoUpdate() async {',
            '  Future<void> autoUpdate() async {\n'
            '    await webScheduler.hintDue();\n  }',
            ('setting.geoAutoUpdate', '_autoUpdating'))
    _method(root, replace_exact, 'lib/providers/actions/mitm_modules.dart',
            '  Future<void> autoUpdate() async {',
            '  Future<void> autoUpdate() async {\n'
            '    await webScheduler.hintDue();\n  }',
            ('setting.mitm.enable', '_autoUpdating'))
    resources = (root / 'lib/providers/actions/resources.dart').read_text()
    if resources.count('_tryUpdate(') != 1:
        raise RuntimeError('Resources _tryUpdate still has another consumer')
    _method(root, replace_exact, 'lib/providers/actions/resources.dart',
            '  Future<void> _tryUpdate(String label, Future<void> Function() update) async {',
            '', ('await update();', 'auto update $label:', 'compactError(e)'))
    for relative in ('lib/providers/actions/resources.dart',
                     'lib/providers/actions/mitm_modules.dart'):
        source = (root / relative).read_text()
        fields = [line for line in source.splitlines(keepends=True)
                  if '_autoUpdating = false;' in line]
        if len(fields) != 1:
            raise RuntimeError(f'Scheduler field anchor changed: {relative}')
        replace_exact(root, relative, fields[0], '')
    _method(root, replace_exact, 'lib/application.dart',
            '  void _autoUpdateProfilesTask() {',
            '  void _autoUpdateProfilesTask() {\n'
            '    _autoUpdateProfilesTaskTimer = Timer(const Duration(minutes: 20), () async {\n'
            '      await globalState.safeRun(webScheduler.hintDue);\n'
            '      if (mounted) _autoUpdateProfilesTask();\n'
            '    });\n  }',
            ('autoUpdateProfiles()', 'resourcesActionProvider', 'mitmModulesActionProvider'))

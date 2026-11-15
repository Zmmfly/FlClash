"""Web-only utility business adapters; native source remains unchanged."""

from pathlib import Path


def _region(root, replace_exact, relative, start, end, replacement, required=()):
    text = (root / relative).read_text()
    if text.count(start) != 1 or text.count(end) != 1:
        raise RuntimeError(f'Utility method anchors changed: {relative}')
    begin, finish = text.index(start), text.index(end)
    old = text[begin:finish]
    if finish <= begin or any(anchor not in old for anchor in required):
        raise RuntimeError(f'Utility method contract changed: {relative}')
    replace_exact(root, relative, old, replacement)


def adapt_utilities(root, replace_exact):
    root = Path(root)
    replace_exact(root, 'lib/common/request.dart',
                  "import 'package:fl_clash/state.dart';",
                  "import 'package:fl_clash/state.dart';\nimport 'package:fl_clash/web_utilities/index.dart';")
    _region(root, replace_exact, 'lib/common/request.dart',
            '  /// Null when this build is current; throws when GitHub cannot be asked.\n',
            '  Future<String> downloadVerifiedRelease({\n', REQUEST_RELEASE,
            required=('Future<Map<String, dynamic>?> checkForUpdate()',
                      'Future<Map<String, dynamic>> latestRelease()'))
    replace_exact(root, 'lib/providers/action.dart',
                  "import 'package:url_launcher/url_launcher.dart';",
                  "import 'package:url_launcher/url_launcher.dart';\nimport 'package:fl_clash/web_utilities/index.dart';")
    replace_exact(root, 'lib/providers/actions/common.dart',
                  '  CancelToken? _updateCancelToken;\n', '')
    _region(root, replace_exact, 'lib/providers/actions/common.dart',
            '  Future<void> checkUpdateResultHandle({\n',
            '  void _openReleasePage() {\n', UPDATE_RESULT,
            required=('final suffix = selfUpdateSuffix;', '_downloadUpdate'))
    _region(root, replace_exact, 'lib/providers/actions/common.dart',
            '  void _openReleasePage() {\n',
            '\n}', UPDATE_ACTIONS,
            required=('Future<void> installUpdate()', 'Future<void> _downloadUpdate('))
    _adapt_icons(root, replace_exact)
    _adapt_ip_quality(root, replace_exact)
    relative = 'lib/views/profiles/custom/icon.dart'
    replace_exact(root, relative,
                  "import 'package:fl_clash/state.dart';",
                  "import 'package:fl_clash/state.dart';\nimport 'package:fl_clash/web_utilities/index.dart';")
    replace_exact(root, relative,
                  '  void _handleSelect(IconSetIcon icon) {\n    Navigator.of(context).pop(icon.url);\n  }',
                  '  Future<void> _handleSelect(IconSetIcon icon) async {\n'
                  "    if (icon.url.startsWith('data:')) {\n"
                  '      Navigator.of(context).pop(icon.url);\n'
                  '      return;\n'
                  '    }\n'
                  '    final accepted = await globalState.safeRun(\n'
                  '      () => previewWebUtilityIcon(webUtilities, context, icon.url),\n'
                  '    );\n'
                  '    if (accepted == true && mounted) Navigator.of(context).pop(icon.url);\n'
                  '  }')
    replace_exact(root, relative,
                  '    if (url != null && mounted) {\n      Navigator.of(context).pop(url.trim());\n    }',
                  '    if (url != null && mounted) {\n'
                  '      final accepted = await globalState.safeRun(\n'
                  '        () => previewWebUtilityIcon(webUtilities, context, url.trim()),\n'
                  '      );\n'
                  '      if (accepted == true && mounted) Navigator.of(context).pop(url.trim());\n'
                  '    }')


def _adapt_icons(root, replace_exact):
    relative = 'lib/widgets/icon.dart'
    replace_exact(root, relative,
                  "import 'package:fl_clash/common/cache.dart';",
                  "import 'package:fl_clash/web_utilities/index.dart';")
    replace_exact(root, relative,
                  "import 'package:flutter_cache_manager/flutter_cache_manager.dart';\n", '')
    _region(root, replace_exact, relative,
            'final _cacheManager = CacheManager(\n',
            'var _readRemoteIcon = _readCachedIcon;\n',
            'Stream<Uint8List> _readCachedIcon(String src) =>\n'
            '    Stream.fromFuture(webUtilities.iconBytes(src));\n\n',
            required=('getFileStreamV2(src)', 'data.file.readAsBytes()'))
    replace_exact(root, relative,
                  '  void remove(String key) {\n    _bytes -= _entries.remove(key)?.lengthInBytes ?? 0;\n  }\n',
                  '  void remove(String key) {\n    _bytes -= _entries.remove(key)?.lengthInBytes ?? 0;\n  }\n\n'
                  '  void clear() {\n    _entries.clear();\n    _bytes = 0;\n  }\n')
    replace_exact(root, relative,
                  'final _remoteIcons = _IconBytesCache(_maxCachedIconBytes);\n',
                  'final _remoteIcons = _IconBytesCache(_maxCachedIconBytes);\n'
                  'var _iconSessionEpoch = -1;\n\n'
                  'void _ensureIconSession() {\n'
                  '  final epoch = webUtilities.sessionEpoch;\n'
                  '  if (_iconSessionEpoch == epoch) return;\n'
                  '  _iconSessionEpoch = epoch;\n'
                  '  _remoteIcons.clear();\n'
                  '  _decodedIcons.clear();\n'
                  '}\n')
    replace_exact(root, relative,
                  '  Widget _buildIcon() {\n    if (src.isEmpty) {',
                  '  Widget _buildIcon() {\n    _ensureIconSession();\n    if (src.isEmpty) {')
    replace_exact(root, relative,
                  'Stream<Uint8List> _loadRemoteIcon(String src) {\n  return _readRemoteIcon(src).map((bytes) {',
                  'Stream<Uint8List> _loadRemoteIcon(String src) {\n'
                  '  _ensureIconSession();\n'
                  '  final epoch = webUtilities.sessionEpoch;\n'
                  '  return _readRemoteIcon(src).map((bytes) {\n'
                  '    webUtilities.requireSession(epoch);')
    replace_exact(root, relative,
                  '            if (!src.isSvg) {\n              await _decodeAhead(bytes);',
                  '            if (!webUtilities.isSvg(bytes)) {\n              await _decodeAhead(bytes);')
    replace_exact(root, relative,
                  '    _bytesNotifier = ValueNotifier(_remoteIcons[widget.src]);',
                  '    _ensureIconSession();\n    _bytesNotifier = ValueNotifier(_remoteIcons[widget.src]);')
    replace_exact(root, relative, '          isSvg: widget.src.isSvg,',
                  '          isSvg: webUtilities.isSvg(bytes),')


def _adapt_ip_quality(root, replace_exact):
    relative = 'lib/providers/ip_quality.dart'
    replace_exact(root, relative,
                  "import 'package:fl_clash/database/database.dart';\n", '')
    _region(root, replace_exact, relative,
            '  if (!ref.isRefresh) {\n', '  final cancelToken = CancelToken();\n', '',
            required=('database.ipQualitiesDao.fresh(ip, maxAge)',))
    _region(root, replace_exact, relative,
            '    final quality = await lookupIpQuality(\n',
            '  } on IpQualityLookupException catch (error) {\n',
            '    final result = await lookupWebIpQuality(\n'
            '      ip, cancelToken: cancelToken, refresh: ref.isRefresh,\n'
            '    );\n'
            '    _expireAt(ref, result.checkedAt.add(maxAge));\n'
            '    return result.quality;\n',
            required=('database.ipQualitiesDao.put(quality, checkedAt, maxAge)',))
    _region(root, replace_exact, relative,
            'Future<T?> _orNull<T>(String action, Future<T> future) async {\n',
            'void _expireAt(Ref ref, DateTime expiry) {\n', '')


REQUEST_RELEASE = '''  Future<Map<String, dynamic>?> checkForUpdate() async {
    final release = await webUtilities.latestRelease();
    return compareVersions(release.version, globalState.packageInfo.version) > 0
        ? release.toUpdateResult() : null;
  }

  Future<Map<String, dynamic>> latestRelease() async =>
      (await webUtilities.latestRelease()).toUpdateResult();

'''

UPDATE_RESULT = '''  Future<void> checkUpdateResultHandle({
    Map<String, dynamic>? data,
    bool isUser = false,
  }) async {
    if (data != null) {
      final page = webUtilities.releasePageForUpdate(data);
      final context = globalState.navigatorKey.currentContext!;
      final result = await dialogs.showMessage(
        title: currentAppLocalizations.discoverNewVersion,
        message: TextSpan(children: [
          TextSpan(text: '${currentAppLocalizations.webUpdateDeploymentHint}\\n\\n'),
          _releaseSpan(context, data['tag_name'] as String, data['body'] as String?),
        ]),
        confirmText: currentAppLocalizations.goDownload,
        cancelText: isUser ? null : currentAppLocalizations.noLongerRemind,
      );
      if (result == true) {
        _openReleasePage(page);
      } else if (!isUser && result == false) {
        ref.read(appSettingProvider.notifier)
            .update((state) => state.copyWith(autoCheckUpdate: false));
      }
    } else if (isUser) {
      await dialogs.showAlert(
        title: currentAppLocalizations.checkUpdate,
        message: TextSpan(text: '${currentAppLocalizations.webUpdateDeploymentHint}\\n\\n'
            '${currentAppLocalizations.checkUpdateError}'),
      );
    }
  }

'''

UPDATE_ACTIONS = '''  void _openReleasePage([Uri? page]) {
    unawaited(launchUrl(
      page ?? Uri.parse('https://github.com/chen08209/FlClash/releases/latest'),
      mode: LaunchMode.externalApplication,
      webOnlyWindowName: '_blank',
    ));
  }

  Future<void> testUpdateDownload() async {
    final release = await webUtilities.latestRelease();
    _openReleasePage(release.releaseUrl);
  }

  void cancelUpdateDownload() {}

  Future<void> discardUpdate() async {
    ref.read(updateDownloadStateProvider.notifier).value = null;
  }

  Future<void> installUpdate() async {
    await dialogs.showAlert(
      title: currentAppLocalizations.checkUpdate,
      message: TextSpan(text: currentAppLocalizations.webUpdateDeploymentHint),
    );
  }
'''

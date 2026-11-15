import 'package:fl_clash/enum/enum.dart';
import 'package:fl_clash/l10n/l10n.dart';
import 'package:fl_clash/models/models.dart';
import 'package:fl_clash/providers/providers.dart';
import 'package:fl_clash/views/profiles/extend/dialers.dart';
import 'package:fl_clash/widgets/widgets.dart';
import 'package:flutter/widgets.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';

import '../helpers/test_app.dart';
import '../helpers/test_profiles.dart';

class _RecordingProfiles extends TestProfiles {
  _RecordingProfiles(super.initial);

  int resets = 0;

  @override
  void resetDialerOverrides(int profileId) {
    resets++;
    updateProfile(
      profileId,
      (profile) => profile.copyWith(overrides: const ProfileOverrides()),
    );
  }
}

const _config = ClashConfig(
  proxies: [
    Proxy(name: 'A', type: 'ss', dialerProxy: 'B'),
    Proxy(name: 'B', type: 'socks5'),
    Proxy(name: ' node, ', type: 'ss'),
    Proxy(name: 'local-direct', type: 'direct'),
  ],
  proxyGroups: [
    ProxyGroup(id: 1, name: 'Group', type: GroupType.Selector, proxies: ['B']),
  ],
  proxyProviders: ['provider-only'],
);

void main() {
  Future<ProviderContainer> pumpPage(
    WidgetTester tester, {
    DialerProxyOverrides stored = const DialerProxyOverrides(),
    AsyncValue<ClashConfig> config = const AsyncData(_config),
  }) async {
    final profile = Profile(
      id: 1,
      autoUpdateDuration: Duration.zero,
      overrides: ProfileOverrides(dialerProxies: stored),
    );
    final container = ProviderContainer(
      retry: (_, _) => null,
      overrides: [
        profilesProvider.overrideWith(() => _RecordingProfiles([profile])),
        clashConfigProvider(1).overrideWithValue(config),
      ],
    );
    addTearDown(container.dispose);
    await tester.pumpWidget(
      UncontrolledProviderScope(
        container: container,
        child: const TestApp(child: ProfileDialersView(1)),
      ),
    );
    if (config.isLoading) {
      await tester.pump();
    } else {
      await tester.pumpAndSettle();
    }
    return container;
  }

  Map<String, String> saved(ProviderContainer container) =>
      container.read(profileProvider(1))!.overrides.dialerProxies.validated;

  Future<void> selectChoice(WidgetTester tester, String choice) async {
    await tester.tap(find.text('A'));
    await tester.pumpAndSettle();
    await tester.tap(find.text(choice).last);
    await tester.pumpAndSettle();
  }

  for (final width in [320.0, 1200.0]) {
    testWidgets(
      'ordinary profile dialers fit and save three states at $width dp',
      (tester) async {
        tester.view.physicalSize = Size(width, 1000);
        tester.view.devicePixelRatio = 1;
        addTearDown(tester.view.resetPhysicalSize);
        addTearDown(tester.view.resetDevicePixelRatio);
        final container = await pumpPage(tester);
        final l10n = AppLocalizations.current;
        expect(find.text(l10n.profileDialerOriginal('B')), findsOneWidget);
        expect(find.text(l10n.profileDialersDescription), findsOneWidget);
        expect(tester.takeException(), isNull);

        await selectChoice(tester, l10n.none);
        expect(saved(container), {'A': ''});
        await selectChoice(tester, l10n.followProfile);
        expect(saved(container), isEmpty);
        await selectChoice(tester, l10n.select);
        expect(find.text('provider-only'), findsNothing);
        expect(find.text('A').hitTestable(), findsNothing);
        expect(find.text('DIRECT'), findsOneWidget);
        expect(find.text('Group'), findsOneWidget);
        await tester.tap(find.text(' node, ').hitTestable());
        await tester.pumpAndSettle();
        expect(saved(container), {'A': ' node, '});
        expect(tester.takeException(), isNull);
        await tester.pumpWidget(const SizedBox.shrink());
      },
    );
  }

  testWidgets('canceling either sheet leaves the saved override untouched', (
    tester,
  ) async {
    final container = await pumpPage(
      tester,
      stored: DialerProxyOverrides.values({'A': 'B'}),
    );
    await tester.tap(find.text('A'));
    await tester.pumpAndSettle();
    Navigator.of(tester.element(find.byType(ProfileDialersView))).pop();
    await tester.pumpAndSettle();
    expect(saved(container), {'A': 'B'});
    await selectChoice(tester, AppLocalizations.current.select);
    Navigator.of(tester.element(find.byType(ProfileDialersView))).pop();
    await tester.pumpAndSettle();
    expect(saved(container), {'A': 'B'});
    await tester.pumpWidget(const SizedBox.shrink());
  });

  testWidgets(
    'missing sources stay visible with accessible errors until explicitly removed',
    (tester) async {
      final container = await pumpPage(
        tester,
        stored: DialerProxyOverrides.values({'missing': 'B'}),
      );
      final l10n = AppLocalizations.current;
      final message = l10n.profileDialerSourceMissing('missing');
      expect(find.text('missing'), findsOneWidget);
      expect(find.byTooltip(message), findsOneWidget);
      expect(saved(container), {'missing': 'B'});
      await tester.tap(find.text('missing'));
      await tester.pumpAndSettle();
      expect(find.text(l10n.select), findsNothing);
      await tester.tap(find.text(l10n.followProfile).last);
      await tester.pumpAndSettle();
      expect(saved(container), isEmpty);
      expect(find.text('missing'), findsNothing);
      await tester.pumpWidget(const SizedBox.shrink());
    },
  );

  testWidgets('missing targets and loops remain saved and visibly invalid', (
    tester,
  ) async {
    final container = await pumpPage(
      tester,
      stored: DialerProxyOverrides.values({'A': 'missing', 'B': 'B'}),
    );
    final l10n = AppLocalizations.current;
    expect(find.byTooltip(l10n.invalidPolicy('missing')), findsOneWidget);
    expect(find.byTooltip(l10n.invalidPolicy('B')), findsOneWidget);
    expect(saved(container), {'A': 'missing', 'B': 'B'});
    await tester.pumpWidget(const SizedBox.shrink());
  });

  testWidgets(
    'unsupported sources cannot acquire nonempty dialers but can clear one',
    (tester) async {
      final container = await pumpPage(tester);
      await tester.tap(find.text('local-direct'));
      await tester.pumpAndSettle();
      expect(find.byType(ProfileDialersView), findsOneWidget);
      expect(find.text(AppLocalizations.current.select), findsNothing);
      expect(saved(container), isEmpty);
      await tester.tap(find.text(AppLocalizations.current.none).last);
      await tester.pumpAndSettle();
      expect(saved(container), {'local-direct': ''});
      await tester.pumpWidget(const SizedBox.shrink());
    },
  );

  testWidgets('loading and errors never render a stale catalog as ready', (
    tester,
  ) async {
    await pumpPage(tester, config: const AsyncLoading());
    expect(find.text('A'), findsNothing);
    await tester.pumpWidget(const SizedBox.shrink());
    await pumpPage(
      tester,
      config: AsyncError(StateError('unavailable'), StackTrace.current),
    );
    expect(find.text('A'), findsNothing);
    expect(find.text(AppLocalizations.current.retry), findsOneWidget);
    await tester.pumpWidget(const SizedBox.shrink());
  });

  testWidgets('retry reloads the catalog without changing stored dialers', (
    tester,
  ) async {
    var attempts = 0;
    final profile = Profile(
      id: 1,
      autoUpdateDuration: Duration.zero,
      overrides: ProfileOverrides(
        dialerProxies: DialerProxyOverrides.values({'A': 'B'}),
      ),
    );
    final container = ProviderContainer(
      retry: (_, _) => null,
      overrides: [
        profilesProvider.overrideWith(() => _RecordingProfiles([profile])),
        clashConfigProvider(1).overrideWith((_) async {
          if (attempts++ == 0) throw StateError('catalog unavailable');
          return _config;
        }),
      ],
    );
    addTearDown(container.dispose);
    await tester.pumpWidget(
      UncontrolledProviderScope(
        container: container,
        child: const TestApp(child: ProfileDialersView(1)),
      ),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.text(AppLocalizations.current.retry));
    await tester.pumpAndSettle();
    expect(attempts, 2);
    expect(find.text('A'), findsOneWidget);
    expect(saved(container), {'A': 'B'});
    await tester.pumpWidget(const SizedBox.shrink());
  });

  testWidgets('unreadable policy reset requires explicit confirmation', (
    tester,
  ) async {
    final container = await pumpPage(
      tester,
      stored: const DialerProxyOverrides.invalidDocument('{broken'),
    );
    final l10n = AppLocalizations.current;
    expect(find.text(l10n.profileDialersUnreadable), findsOneWidget);
    final profiles =
        container.read(profilesProvider.notifier) as _RecordingProfiles;
    await tester.tap(find.text(l10n.reset));
    await tester.pumpAndSettle();
    expect(find.text(l10n.profileDialersResetTip), findsOneWidget);
    await tester.tap(find.text(l10n.cancel));
    await tester.pumpAndSettle();
    expect(profiles.resets, 0);
    expect(
      container.read(profileProvider(1))!.overrides.dialerProxies.isUnreadable,
      isTrue,
    );
    await tester.tap(find.text(l10n.reset));
    await tester.pumpAndSettle();
    await tester.tap(
      find
          .descendant(
            of: find.byType(AlertDialog),
            matching: find.text(l10n.reset),
          )
          .last,
    );
    await tester.pumpAndSettle();
    expect(profiles.resets, 1);
    expect(saved(container), isEmpty);
    await tester.pumpWidget(const SizedBox.shrink());
  });
}

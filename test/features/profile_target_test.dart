import 'package:fl_clash/enum/enum.dart';
import 'package:fl_clash/features/form/form.dart';
import 'package:fl_clash/models/clash_config.dart';
import 'package:fl_clash/l10n/l10n.dart';
import 'package:fl_clash/widgets/widgets.dart';
import 'package:fl_clash/providers/providers.dart';
import 'package:flutter/widgets.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';

import '../helpers/test_app.dart';

class _Catalog extends Notifier<ClashConfig> {
  @override
  ClashConfig build() => const ClashConfig(
    proxies: [Proxy(name: 'node', type: 'ss')],
  );

  void clear() => state = const ClashConfig();
}

final _catalogProvider = NotifierProvider<_Catalog, ClashConfig>(_Catalog.new);

void main() {
  const config = ClashConfig(
    proxyGroups: [
      ProxyGroup(id: 1, name: 'group', type: GroupType.Selector),
      ProxyGroup(id: 2, name: 'duplicate', type: GroupType.Selector),
    ],
    proxies: [
      Proxy(name: 'node', type: 'ss'),
      Proxy(name: 'duplicate', type: 'ss'),
      Proxy(name: 'bad,name', type: 'ss'),
    ],
  );

  test('target names remain exact and duplicate roots are ambiguous', () {
    expect(profileTargetNames(config), {'group', 'node', 'bad,name'});
    expect(isRuleTargetName(' 节点'), isFalse);
    expect(isRuleTargetName('节点 '), isFalse);
    expect(isRuleTargetName('节点,一'), isFalse);
    expect(isRuleTargetName('节点\n一'), isFalse);
    expect(isRuleTargetName('节点 一'), isTrue);
  });

  testWidgets('sheet offers fixed, group and valid root nodes', (tester) async {
    String? chosen;
    await tester.pumpWidget(
      TestApp(
        overrides: [
          clashConfigProvider(3).overrideWithValue(const AsyncData(config)),
        ],
        child: ProfileTargetSheet(
          profileId: 3,
          title: 'Choose',
          selected: 'DIRECT',
          fixedTargets: const ['DIRECT'],
          acceptsTarget: isRuleTargetName,
          onSelected: (value) => chosen = value,
        ),
      ),
    );
    await tester.pumpAndSettle();
    expect(find.text('DIRECT'), findsOneWidget);
    expect(find.text('group'), findsOneWidget);
    expect(find.text('node'), findsOneWidget);
    expect(find.text('duplicate'), findsNothing);
    expect(find.text('bad,name'), findsNothing);
    await tester.tap(find.text('node'));
    expect(chosen, 'node');
    await tester.pumpWidget(const SizedBox.shrink());
  });
  testWidgets('rule form selects a node and stores its exact name', (
    tester,
  ) async {
    Rule? result;
    await tester.pumpWidget(
      TestApp(
        overrides: [
          clashConfigProvider(3).overrideWithValue(const AsyncData(config)),
        ],
        child: Builder(
          builder: (context) => FilledButton(
            onPressed: () async {
              result = await showFormSheet<Rule>(
                context: context,
                builder: (_) => const RuleForm(profileId: 3),
              );
            },
            child: const Text('open'),
          ),
        ),
      ),
    );
    await tester.tap(find.text('open'));
    await tester.pumpAndSettle();
    await tester.enterText(find.byType(TextFormField), 'example.com');
    await tester.tap(find.text(AppLocalizations.current.splitStrategy));
    await tester.pumpAndSettle();
    await tester.scrollUntilVisible(
      find.text('node'),
      200,
      scrollable: find
          .descendant(
            of: find.byType(ProfileTargetSheet),
            matching: find.byType(Scrollable),
          )
          .first,
    );
    await tester.tap(find.text('node').hitTestable());
    await tester.pumpAndSettle();
    await tester.tap(find.byTooltip(AppLocalizations.current.save));
    await tester.pumpAndSettle();
    expect(result?.ruleTarget, 'node');
    expect(result?.rawValue, 'DOMAIN,example.com,node');
    await tester.pumpWidget(const SizedBox.shrink());
  });

  testWidgets('loading and failed catalogs leave fixed targets available', (
    tester,
  ) async {
    String? chosen;
    Future<void> pump(AsyncValue<ClashConfig> state) async {
      await tester.pumpWidget(
        TestApp(
          overrides: [clashConfigProvider(3).overrideWithValue(state)],
          child: ProfileTargetSheet(
            profileId: 3,
            title: 'Choose',
            selected: 'missing',
            fixedTargets: const ['DIRECT'],
            onSelected: (value) => chosen = value,
          ),
        ),
      );
      await tester.pump();
    }

    await pump(const AsyncLoading());
    expect(find.text('DIRECT'), findsOneWidget);
    expect(find.text(AppLocalizations.current.loading), findsOneWidget);
    await tester.tap(find.text('DIRECT'));
    expect(chosen, 'DIRECT');
    await tester.pumpWidget(const SizedBox.shrink());
    await pump(AsyncError(Exception('unavailable'), StackTrace.current));
    expect(find.text('DIRECT'), findsOneWidget);
    expect(find.text(AppLocalizations.current.retry), findsOneWidget);
    await tester.pumpWidget(const SizedBox.shrink());
  });
  testWidgets('missing names are invalid only after a catalog is available', (
    tester,
  ) async {
    String? missing;
    String? unknown;
    String? valid;
    await tester.pumpWidget(
      TestApp(
        child: Builder(
          builder: (context) {
            const rule = Rule(content: 'example.com', ruleTarget: 'missing');
            missing = addedRuleTargetError(context, rule, config);
            unknown = addedRuleTargetError(context, rule, null);
            valid = addedRuleTargetError(
              context,
              rule.copyWith(ruleTarget: 'node'),
              config,
            );
            return const SizedBox.shrink();
          },
        ),
      ),
    );
    expect(missing, isNotNull);
    expect(unknown, isNull);
    expect(valid, isNull);
    await tester.pumpWidget(const SizedBox.shrink());
  });

  testWidgets(
    'catalog refresh removing target prevents Save without losing it',
    (tester) async {
      Rule? result;
      await tester.pumpWidget(
        TestApp(
          overrides: [
            clashConfigProvider(
              3,
            ).overrideWith((ref) async => ref.watch(_catalogProvider)),
          ],
          child: Builder(
            builder: (context) => FilledButton(
              onPressed: () async {
                result = await showFormSheet<Rule>(
                  context: context,
                  builder: (_) => const RuleForm(
                    profileId: 3,
                    rule: Rule(content: 'example.com', ruleTarget: 'node'),
                  ),
                );
              },
              child: const Text('open'),
            ),
          ),
        ),
      );
      await tester.tap(find.text('open'));
      await tester.pumpAndSettle();
      final container = ProviderScope.containerOf(
        tester.element(find.byType(RuleForm)),
      );
      container.read(_catalogProvider.notifier).clear();
      await tester.pumpAndSettle();
      await tester.tap(find.byTooltip(AppLocalizations.current.save));
      await tester.pumpAndSettle();
      expect(result, isNull);
      expect(find.byType(RuleForm), findsOneWidget);
      expect(find.text('node'), findsOneWidget);
      expect(
        find.text(AppLocalizations.current.invalidPolicy('node')),
        findsOneWidget,
      );
      await tester.pumpWidget(const SizedBox.shrink());
    },
  );

  for (final width in [320.0, 1200.0]) {
    testWidgets('rule target form and selector fit at $width dp', (
      tester,
    ) async {
      tester.view.physicalSize = Size(width, 800);
      tester.view.devicePixelRatio = 1;
      addTearDown(tester.view.resetPhysicalSize);
      addTearDown(tester.view.resetDevicePixelRatio);
      await tester.pumpWidget(
        TestApp(
          overrides: [
            clashConfigProvider(3).overrideWithValue(const AsyncData(config)),
          ],
          child: Builder(
            builder: (context) => FilledButton(
              onPressed: () => showFormSheet<Rule>(
                context: context,
                builder: (_) => const RuleForm(
                  profileId: 3,
                  rule: Rule(content: 'example.com', ruleTarget: 'missing'),
                ),
              ),
              child: const Text('open'),
            ),
          ),
        ),
      );
      await tester.tap(find.text('open'));
      await tester.pumpAndSettle();
      expect(find.text('missing'), findsOneWidget);
      await tester.tap(find.text(AppLocalizations.current.splitStrategy));
      await tester.pumpAndSettle();
      expect(find.text('DIRECT'), findsOneWidget);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
    });
  }

  for (final failed in [false, true]) {
    testWidgets(
      'unknown catalog preserves the old target on Save (error: $failed)',
      (tester) async {
        Rule? result;
        final AsyncValue<ClashConfig> state = failed
            ? AsyncError(Exception('unavailable'), StackTrace.current)
            : const AsyncLoading();
        await tester.pumpWidget(
          TestApp(
            overrides: [clashConfigProvider(3).overrideWithValue(state)],
            child: Builder(
              builder: (context) => FilledButton(
                onPressed: () async {
                  result = await showFormSheet<Rule>(
                    context: context,
                    builder: (_) => const RuleForm(
                      profileId: 3,
                      rule: Rule(
                        content: 'example.com',
                        ruleTarget: 'old-node',
                      ),
                    ),
                  );
                },
                child: const Text('open'),
              ),
            ),
          ),
        );
        await tester.tap(find.text('open'));
        await tester.pumpAndSettle();
        expect(find.text('old-node'), findsOneWidget);
        await tester.tap(find.byTooltip(AppLocalizations.current.save));
        await tester.pumpAndSettle();
        expect(result?.ruleTarget, 'old-node');
        await tester.pumpWidget(const SizedBox.shrink());
      },
    );
  }
}

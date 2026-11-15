import 'package:fl_clash/common/common.dart';
import 'package:fl_clash/enum/enum.dart';
import 'package:fl_clash/models/clash_config.dart';
import 'package:fl_clash/providers/providers.dart';
import 'package:flutter/widgets.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'selection_sheet.dart';

bool isRuleTargetName(String name) =>
    name.isNotEmpty &&
    name.trim() == name &&
    !name.contains(RegExp(r'[,\r\n]'));

/// Names shared by two root definitions cannot identify one outbound safely.
final _targetNames = Expando<Set<String>>();

Set<String> profileTargetNames(ClashConfig config) =>
    _targetNames[config] ??= _collectProfileTargetNames(config);

Set<String> _collectProfileTargetNames(ClashConfig config) {
  final counts = <String, int>{};
  for (final name in [
    ...config.proxyGroups.map((item) => item.name),
    ...config.proxies.map((item) => item.name),
  ]) {
    counts[name] = (counts[name] ?? 0) + 1;
  }
  return {
    for (final entry in counts.entries)
      if (entry.value == 1) entry.key,
  };
}

String? addedRuleTargetError(
  BuildContext context,
  Rule rule,
  ClashConfig? config,
) {
  final target = rule.ruleTarget ?? '';
  if (!isRuleTargetName(target) ||
      (config != null &&
          !RuleTarget.ruleTargets.contains(target) &&
          target != RuleAction.MATCH.value &&
          !profileTargetNames(config).contains(target))) {
    return context.appLocalizations.invalidPolicy(target);
  }
  return null;
}

class _TargetEntry {
  final String name;
  final String? subtitle;
  final bool retry;
  final bool status;

  const _TargetEntry(
    this.name, {
    this.subtitle,
    this.retry = false,
    this.status = false,
  });
}

class ProfileTargetSheet extends ConsumerWidget {
  final int? profileId;
  final String title;
  final String selected;
  final List<String> fixedTargets;
  final ValueChanged<String> onSelected;
  final String Function(String)? labelBuilder;
  final Set<String> excludedTargets;
  final bool Function(String)? acceptsTarget;

  const ProfileTargetSheet({
    super.key,
    required this.profileId,
    required this.title,
    required this.selected,
    required this.fixedTargets,
    required this.onSelected,
    this.labelBuilder,
    this.excludedTargets = const {},
    this.acceptsTarget,
  });

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final l10n = context.appLocalizations;
    final id = profileId;
    final state = id == null ? null : ref.watch(clashConfigProvider(id));
    final config = state != null && !state.isLoading && !state.hasError
        ? state.value
        : null;
    final names = config == null ? <String>{} : profileTargetNames(config);
    final seen = <String>{};
    bool accepted(String name) =>
        !excludedTargets.contains(name) && (acceptsTarget?.call(name) ?? true);
    final fixed = <_TargetEntry>[
      for (final name in fixedTargets)
        if (accepted(name) && seen.add(name)) _TargetEntry(name),
    ];
    final groups = <_TargetEntry>[
      for (final item in config?.proxyGroups ?? const <ProxyGroup>[])
        if (names.contains(item.name) &&
            accepted(item.name) &&
            seen.add(item.name))
          _TargetEntry(item.name, subtitle: item.type.name),
    ];
    final proxies = <_TargetEntry>[
      for (final item in config?.proxies ?? const <Proxy>[])
        if (names.contains(item.name) &&
            accepted(item.name) &&
            seen.add(item.name))
          _TargetEntry(item.name, subtitle: item.type),
    ];
    final status = <_TargetEntry>[
      if (state?.isLoading == true) _TargetEntry(l10n.loading, status: true),
      if (state?.hasError == true)
        _TargetEntry(l10n.retry, subtitle: l10n.error, retry: true),
    ];
    final entries = [...fixed, ...groups, ...proxies];
    return SelectionSheet<_TargetEntry>(
      title: title,
      sections: [
        SelectionSection(label: l10n.basicStrategy, items: fixed),
        SelectionSection(
          label: l10n.ruleTarget,
          items: groups,
          subtitleBuilder: (_, item) => item.subtitle,
        ),
        SelectionSection(
          label: l10n.proxies,
          items: proxies,
          subtitleBuilder: (_, item) => item.subtitle,
        ),
        SelectionSection(
          items: status,
          subtitleBuilder: (_, item) => item.subtitle,
        ),
      ],
      labelBuilder: (item) => item.status || item.retry
          ? item.name
          : (labelBuilder?.call(item.name) ?? item.name),
      selectedOf: (_) {
        for (final entry in entries) {
          if (entry.name == selected) return entry;
        }
        return null;
      },
      onSelected: (entry) {
        if (entry.retry && id != null) {
          ref.invalidate(clashConfigProvider(id));
        } else if (!entry.status) {
          onSelected(entry.name);
        }
      },
    );
  }
}

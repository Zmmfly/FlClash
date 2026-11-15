import 'package:fl_clash/common/common.dart';
import 'package:fl_clash/features/form/profile_target.dart';
import 'package:fl_clash/features/form/selection_sheet.dart';
import 'package:fl_clash/models/models.dart';
import 'package:fl_clash/providers/providers.dart';
import 'package:fl_clash/ui/icons/icons.dart';
import 'package:fl_clash/widgets/widgets.dart';
import 'package:flutter/widgets.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

enum _DialerChoice { inherit, clear, target }

const _dialingTypes = {
  'ss',
  'ssr',
  'socks5',
  'http',
  'vmess',
  'vless',
  'snell',
  'trojan',
  'hysteria',
  'hysteria2',
  'wireguard',
  'tuic',
  'shadowquic',
  'gost-relay',
  'ssh',
  'mieru',
  'anytls',
  'sudoku',
  'masque',
  'trusttunnel',
  'openvpn',
  'tailscale',
  'zerotier',
  'easytier',
};

class ProfileDialersView extends ConsumerWidget {
  final int profileId;

  const ProfileDialersView(this.profileId, {super.key});

  Future<void> _reset(BuildContext context, WidgetRef ref) async {
    final l10n = context.appLocalizations;
    final confirmed = await dialogs.showMessage(
      title: l10n.reset,
      message: TextSpan(text: l10n.profileDialersResetTip),
      destructive: true,
      confirmText: l10n.reset,
    );
    if (confirmed != true || !context.mounted) return;
    ref.read(profilesProvider.notifier).resetDialerOverrides(profileId);
  }

  Future<void> _select(
    BuildContext context,
    WidgetRef ref,
    String source, {
    required bool canDial,
    required bool canClear,
  }) async {
    final original = ref.read(profileProvider(profileId));
    if (original == null) return;
    final current = original.overrides.dialerProxies.validated[source];
    final l10n = context.appLocalizations;
    final choice = await showSheet<_DialerChoice>(
      context: context,
      builder: (context) => SelectionSheet<_DialerChoice>(
        title: l10n.dialerProxy,
        sections: [
          SelectionSection(
            items: [
              _DialerChoice.inherit,
              if (canClear) _DialerChoice.clear,
              if (canDial) _DialerChoice.target,
            ],
          ),
        ],
        labelBuilder: (choice) => switch (choice) {
          _DialerChoice.inherit => l10n.followProfile,
          _DialerChoice.clear => l10n.none,
          _DialerChoice.target => l10n.select,
        },
        selectedOf: (_) => current == null
            ? _DialerChoice.inherit
            : current.isEmpty
            ? _DialerChoice.clear
            : _DialerChoice.target,
        onSelected: (choice) => Navigator.of(context).pop(choice),
      ),
    );
    if (choice == null || !context.mounted) return;
    String? target;
    if (choice == _DialerChoice.target) {
      target = await showSheet<String>(
        context: context,
        builder: (context) => ProfileTargetSheet(
          profileId: profileId,
          title: l10n.dialerProxy,
          selected: current ?? '',
          fixedTargets: const ['DIRECT'],
          excludedTargets: {source},
          onSelected: (name) => Navigator.of(context).pop(name),
        ),
      );
      if (target == null || !context.mounted) return;
    } else if (choice == _DialerChoice.clear) {
      target = '';
    }
    ref.read(profilesProvider.notifier).updateProfile(profileId, (profile) {
      return profile.copyWith(
        overrides: profile.overrides.copyWith(
          dialerProxies: profile.overrides.dialerProxies.withTarget(
            source,
            target,
          ),
        ),
      );
    });
  }

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final l10n = context.appLocalizations;
    final profile = ref.watch(profileProvider(profileId));
    final stored =
        profile?.overrides.dialerProxies ?? const DialerProxyOverrides();
    final state = ref.watch(clashConfigProvider(profileId));
    final config = !state.isLoading && !state.hasError ? state.value : null;
    final overrides = stored.isValid
        ? stored.validated
        : const <String, String>{};
    final counts = <String, int>{};
    final proxies = <String, Proxy>{};
    for (final proxy in config?.proxies ?? const <Proxy>[]) {
      counts.update(proxy.name, (count) => count + 1, ifAbsent: () => 1);
      proxies[proxy.name] = proxy;
    }
    final names = {...proxies.keys, ...overrides.keys}.toList();
    final targets = config == null ? <String>{} : profileTargetNames(config);
    final invalid = !stored.isValid;
    return Scaffold(
      title: l10n.dialerProxy,
      body: Builder(
        builder: (context) => NullStatusSwitcher(
          isLoading: !invalid && state.isLoading,
          isEmpty:
              invalid || state.hasError || names.isEmpty || profile == null,
          isSearching: false,
          nullStatus: NullStatus(
            label: invalid
                ? l10n.profileDialersUnreadable
                : state.hasError
                ? l10n.error
                : l10n.nullTip(l10n.proxies),
            illustration: invalid || state.hasError
                ? NullStatusIllustration.error
                : NullStatusIllustration.data,
            action: invalid
                ? TonalButton(
                    size: TonalButtonSize.bar,
                    label: l10n.reset,
                    onPressed: () => _reset(context, ref),
                  )
                : state.hasError
                ? TonalButton(
                    size: TonalButtonSize.bar,
                    label: l10n.retry,
                    onPressed: () =>
                        ref.invalidate(clashConfigProvider(profileId)),
                  )
                : null,
          ),
          child: PrismListView(
            padding: EdgeInsets.fromLTRB(
              16,
              context.contentTopPadding,
              16,
              24 + context.bottomInset,
            ),
            itemCount: names.length + 1,
            itemBuilder: (context, index) {
              if (index == 0) {
                return Padding(
                  padding: const EdgeInsets.only(bottom: 16),
                  child: Text(
                    l10n.profileDialersDescription,
                    style: AppTextStyle.subheadline.of(context),
                  ),
                );
              }
              final name = names[index - 1];
              final proxy = proxies[name];
              final sourceExists = counts[name] == 1 && targets.contains(name);
              final canDial =
                  sourceExists &&
                  _dialingTypes.contains(proxy?.type.toLowerCase());
              final target = overrides[name];
              final sourceInvalid =
                  target != null &&
                  (!sourceExists || target.isNotEmpty && !canDial);
              final targetInvalid =
                  target != null &&
                  target.isNotEmpty &&
                  (target == name ||
                      target != 'DIRECT' && !targets.contains(target));
              final error = sourceInvalid
                  ? !sourceExists
                        ? l10n.profileDialerSourceMissing(name)
                        : l10n.invalidProxy(name)
                  : targetInvalid
                  ? l10n.invalidPolicy(target)
                  : null;
              final original = proxy?.dialerProxy;
              final label = target == null
                  ? original?.isNotEmpty == true
                        ? l10n.profileDialerOriginal(original!)
                        : l10n.followProfile
                  : target.isEmpty
                  ? l10n.none
                  : target;
              return ItemPositionProvider(
                key: ValueKey(name),
                position: ItemPosition.get(index - 1, names.length),
                child: DecorationListItem(
                  invalid: error != null,
                  onPressed: sourceExists || target != null
                      ? () => _select(
                          context,
                          ref,
                          name,
                          canDial: canDial,
                          canClear: sourceExists,
                        )
                      : null,
                  title: Text(
                    name,
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                  ),
                  subtitle: Text(
                    error ??
                        (!canDial && target == null
                            ? l10n.serviceUnavailable
                            : label),
                    maxLines: 2,
                    overflow: TextOverflow.ellipsis,
                    style: AppTextStyle.subheadline.of(context),
                  ),
                  trailing: error == null
                      ? null
                      : Tooltip(
                          message: error,
                          child: GlyphIcon(
                            AppGlyphs.warning,
                            color: context.colorScheme.error,
                          ),
                        ),
                ),
              );
            },
          ),
        ),
      ),
    );
  }
}

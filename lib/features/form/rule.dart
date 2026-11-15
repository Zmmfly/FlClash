library;

import 'package:fl_clash/common/common.dart';
import 'package:fl_clash/enum/enum.dart';
import 'package:fl_clash/models/clash_config.dart';
import 'package:fl_clash/state.dart';
import 'package:fl_clash/ui/icons/icons.dart';
import 'package:fl_clash/widgets/widgets.dart';
import 'package:flutter/widgets.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:fl_clash/providers/providers.dart';

import 'profile_target.dart';

import 'form_page.dart';
import 'form_row.dart';

const _ruleRowVerticalPadding = 7.0;

/// Rule lists run to thousands of rows, so a rule row is padded tighter than
/// other list rows.
double get ruleItemHeight =>
    globalState.measure.listTitleHeight +
    globalState.measure.listSubtitleHeight +
    2 * _ruleRowVerticalPadding;

class RuleItem extends StatelessWidget {
  static const _targetMaxWidthFactor = 0.5;
  static const _targetMinWidthFactor = 0.3;
  static const _labelTargetGap = 16.0;

  final bool isSelected;
  final bool isEditing;
  final Rule rule;
  final bool hasMatch;
  final void Function() onSelected;
  final void Function(Rule rule) onEdit;
  final String? Function(Rule rule)? invalidMessageOf;

  const RuleItem({
    super.key,
    required this.isSelected,
    required this.rule,
    required this.onSelected,
    required this.onEdit,
    this.invalidMessageOf,
    this.isEditing = false,
    this.hasMatch = false,
  });

  ({bool invalid, Color? color, String? message}) _checkInvalid(
    BuildContext context,
  ) {
    final message = invalidMessageOf?.call(rule);
    if (message == null && rule.ruleAction != RuleAction.SUB_RULE) {
      final ruleTarget = (rule.ruleTarget ?? '').toUpperCase();
      final color = switch (ruleTarget) {
        'DIRECT' || 'COMPATIBLE' => context.colorScheme.success,
        'REJECT' || 'REJECT-DROP' => context.colorScheme.warning,
        'PASS' || 'PASS-RULE' || 'GLOBAL' => context.colorScheme.tertiary,
        'MATCH' when hasMatch => context.colorScheme.tertiary,
        _ => null,
      };
      if (color != null) {
        return (invalid: false, color: color, message: null);
      }
    }
    final invalid = invalidMessageOf == null || message != null;
    return (
      invalid: invalid,
      color: invalid ? context.colorScheme.error : context.colorScheme.tertiary,
      message: message,
    );
  }

  Widget _buildInfoWidget(BuildContext context, String? message) {
    return MinIconButtonTheme(
      child: IconButton(
        tooltip: context.appLocalizations.tip,
        onPressed: () {
          dialogs.showAlert(
            message: TextSpan(
              text:
                  message ??
                  rule.targetErrorTip(
                    context.appLocalizations.invalidSubRule(rule.subRule ?? ''),
                    context.appLocalizations.invalidPolicy(
                      rule.ruleTarget ?? '',
                    ),
                  ),
            ),
          );
        },
        icon: GlyphIcon(
          AppGlyphs.info,
          fill: 1,
          size: 16.ap(context),
          color: context.colorScheme.error,
        ),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final checkResult = _checkInvalid(context);
    final invalid = checkResult.invalid;
    return SelectedDecorationListItem(
      minVerticalPadding: 0,
      isSelected: isSelected,
      isEditing: isEditing,
      horizontalTitleGap: 0,
      invalid: invalid,
      onSelected: () {
        onSelected();
      },
      title: Center(
        child: LayoutBuilder(
          builder: (context, constraints) {
            final labelStyle = DefaultTextStyle.of(
              context,
            ).style.toJetBrainsMono;
            final actionWidth = globalState.measure
                .computeTextSize(Text(rule.ruleAction.name, style: labelStyle))
                .width;
            final maxWidth = constraints.maxWidth;
            final targetMaxWidth = (maxWidth - _labelTargetGap - actionWidth)
                .clamp(
                  maxWidth * _targetMinWidthFactor,
                  maxWidth * _targetMaxWidthFactor,
                );
            return Row(
              mainAxisSize: MainAxisSize.max,
              mainAxisAlignment: MainAxisAlignment.spaceBetween,
              crossAxisAlignment: CrossAxisAlignment.center,
              children: [
                Expanded(
                  child: _RuleItemLabel(rule: rule, style: labelStyle),
                ),
                const SizedBox(width: _labelTargetGap),
                ConstrainedBox(
                  constraints: BoxConstraints(maxWidth: targetMaxWidth),
                  child: Row(
                    mainAxisSize: MainAxisSize.min,
                    children: [
                      if (invalid)
                        _buildInfoWidget(context, checkResult.message),
                      if (rule.realTarget != null)
                        Flexible(
                          child: TooltipText(
                            text: Text(
                              hasMatch &&
                                      rule.realTarget!.toUpperCase() ==
                                          RuleAction.MATCH.value
                                  ? context.appLocalizations.matchTarget
                                  : rule.realTarget!,
                              maxLines: 1,
                              overflow: TextOverflow.ellipsis,
                              style: AppTextStyle.subheadline
                                  .of(context)
                                  .toJetBrainsMono
                                  .copyWith(color: checkResult.color),
                            ),
                          ),
                        ),
                    ],
                  ),
                ),
              ],
            );
          },
        ),
      ),
      onPressed: () {
        onEdit(rule);
      },
    );
  }
}

class _RuleItemLabel extends StatelessWidget {
  const _RuleItemLabel({required this.rule, required this.style});

  final Rule rule;
  final TextStyle style;

  @override
  Widget build(BuildContext context) {
    final content = rule.realContent ?? '';
    return Column(
      mainAxisSize: MainAxisSize.min,
      mainAxisAlignment: MainAxisAlignment.center,
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(
          rule.ruleAction.name,
          maxLines: 1,
          overflow: TextOverflow.ellipsis,
          style: style,
        ),
        if (content.isNotEmpty)
          Flexible(
            child: TooltipText(
              text: Text(
                content,
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: context.listSubtitleStyle.toJetBrainsMono.copyWith(
                  color: style.color?.opacity60,
                ),
              ),
            ),
          ),
      ],
    );
  }
}

class RuleStatusItem extends StatelessWidget {
  final bool status;
  final Rule rule;
  final void Function(bool) onChange;

  const RuleStatusItem({
    super.key,
    required this.status,
    required this.rule,
    required this.onChange,
  });

  @override
  Widget build(BuildContext context) {
    return DecorationListItem(
      title: TooltipText(
        text: Text(
          rule.rawValue,
          maxLines: 2,
          overflow: TextOverflow.ellipsis,
          style: AppTextStyle.subheadline.of(context).toJetBrainsMono,
        ),
      ),
      trailing: Switch(value: status, onChanged: onChange),
      onPressed: () {
        onChange(!status);
      },
    );
  }
}

enum _RuleField { content, target }

class RuleForm extends ConsumerStatefulWidget {
  final Rule? rule;
  final int? profileId;

  const RuleForm({super.key, this.rule, this.profileId});

  @override
  ConsumerState<RuleForm> createState() => _RuleFormState();
}

class _RuleFormState extends ConsumerState<RuleForm> {
  late RuleAction _ruleAction;
  late String _ruleTarget;
  late final TextEditingController _contentController;
  late bool _noResolve;
  late bool _src;
  bool _attempted = false;

  @override
  void initState() {
    super.initState();
    final rule = widget.rule;
    _ruleAction = rule?.ruleAction ?? RuleAction.addedRuleActions.first;
    _ruleTarget = rule?.ruleTarget ?? RuleTarget.ruleTargetNames.first;
    _contentController = TextEditingController(text: rule?.content);
    _noResolve = rule?.noResolve ?? false;
    _src = rule?.src ?? false;
  }

  @override
  void dispose() {
    _contentController.dispose();
    super.dispose();
  }

  ({_RuleField field, String message})? _issue(ClashConfig? config) {
    final appLocalizations = context.appLocalizations;
    final content = _contentController.text;
    if (content.isEmpty) {
      return (
        field: _RuleField.content,
        message: appLocalizations.emptyTip(appLocalizations.content),
      );
    }
    final payloadError = Rule(
      ruleAction: _ruleAction,
      content: content,
    ).payloadError;
    if (payloadError != null) {
      return (
        field: _RuleField.content,
        message: payloadError.getMessage(context),
      );
    }
    final targetError = addedRuleTargetError(
      context,
      Rule(ruleTarget: _ruleTarget),
      config,
    );
    if (targetError != null) {
      return (field: _RuleField.target, message: targetError);
    }
    return null;
  }

  void _handleSubmit() {
    final id = widget.profileId;
    final state = id == null ? null : ref.read(clashConfigProvider(id));
    final config = state != null && !state.isLoading && !state.hasError
        ? state.value
        : null;
    if (_issue(config) != null) {
      setState(() {
        _attempted = true;
      });
      return;
    }
    Navigator.of(context).pop(
      (widget.rule ?? Rule(id: snowflake.id)).copyWith(
        ruleAction: _ruleAction,
        content: _contentController.text,
        ruleTarget: _ruleTarget,
        noResolve: _noResolve,
        src: _src,
      ),
    );
  }

  String _targetLabel(String target) => target == RuleAction.MATCH.value
      ? context.appLocalizations.matchTarget
      : target;

  Widget _buildSwitchRow({
    required String title,
    required bool value,
    required ValueChanged<bool> onChanged,
  }) {
    return FormRow(
      title: title,
      onPressed: () => onChanged(!value),
      trailing: Switch(value: value, onChanged: onChanged),
    );
  }

  Future<void> _selectTarget() async {
    final value = await Navigator.of(context).push<String>(
      PagedSheetRoute(
        builder: (context) => ProfileTargetSheet(
          profileId: widget.profileId,
          title: context.appLocalizations.splitStrategy,
          selected: _ruleTarget,
          fixedTargets: [...RuleTarget.ruleTargetNames, RuleAction.MATCH.value],
          acceptsTarget: (name) =>
              isRuleTargetName(name) &&
              (name == RuleAction.MATCH.value ||
                  name.toUpperCase() != RuleAction.MATCH.value),
          labelBuilder: _targetLabel,
          onSelected: (target) => Navigator.of(context).pop(target),
        ),
      ),
    );
    if (value != null && mounted) setState(() => _ruleTarget = value);
  }

  @override
  Widget build(BuildContext context) {
    final appLocalizations = context.appLocalizations;
    final id = widget.profileId;
    final state = id == null ? null : ref.watch(clashConfigProvider(id));
    final config = state != null && !state.isLoading && !state.hasError
        ? state.value
        : null;
    final issue = _attempted ? _issue(config) : null;
    final invalid =
        addedRuleTargetError(context, Rule(ruleTarget: _ruleTarget), config) !=
        null;
    return FormPage(
      title: widget.rule != null
          ? appLocalizations.editRule
          : appLocalizations.addRule,
      onSave: _handleSubmit,
      error: issue?.message,
      children: [
        generateSection(
          title: appLocalizations.basicInfo,
          items: [
            ListItem<RuleAction>.options(
              title: Text(appLocalizations.proxyType),
              options: RuleAction.addedRuleActions,
              value: _ruleAction,
              textBuilder: (action) => action.value,
              onChanged: (action) {
                setState(() {
                  _ruleAction = action;
                });
              },
            ),
            FormRow(
              title: appLocalizations.content,
              invalid: issue?.field == _RuleField.content,
              trailing: FormRowTextField(
                controller: _contentController,
                hintText: appLocalizations.inputRuleContent,
                autocorrect: false,
                inputFormatters: TextInputLimits.limit(TextInputLimits.rule),
                onChanged: (_) {
                  if (_attempted) {
                    setState(() {});
                  }
                },
                onFieldSubmitted: (_) => _handleSubmit(),
              ),
            ),
            FormRow(
              title: appLocalizations.splitStrategy,
              invalid: invalid,
              onPressed: _selectTarget,
              trailing: Text(_targetLabel(_ruleTarget)),
            ),
          ],
        ),
        if (_ruleAction.hasParams)
          generateSection(
            title: appLocalizations.additionalParameters,
            items: [
              _buildSwitchRow(
                title: appLocalizations.noResolveHostname,
                value: _noResolve,
                onChanged: (value) {
                  setState(() {
                    _noResolve = value;
                  });
                },
              ),
              _buildSwitchRow(
                title: appLocalizations.matchSourceIp,
                value: _src,
                onChanged: (value) {
                  setState(() {
                    _src = value;
                  });
                },
              ),
            ],
          ),
      ],
    );
  }
}

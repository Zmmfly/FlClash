"""Isolated generic navigation slots; native checkout and navigation data stay intact."""


def adapt_navigation(root, replace):
    sidebar = 'plugins/prism_ui/lib/sidebar.dart'
    replace(root, sidebar, "import 'package:prism_ui/typography.dart';",
            "import 'package:prism_ui/typography.dart';\nimport 'navigation_action.dart';\nimport 'loading.dart';")
    replace(root, sidebar, '    this.windowControls = Size.zero,',
            '    this.windowControls = Size.zero,\n    this.footerAction,')
    replace(root, sidebar, '  final Size windowControls;\n\n  @override',
            '  final Size windowControls;\n  final NavigationAction? footerAction;\n\n  @override')
    replace(root, sidebar, '  void _handleToggle() {', '''  NavigationAction? get _overlayFooterAction {
    final action = widget.footerAction;
    if (action == null) return null;
    return action.withOnPressed(action.onPressed == null ? null : () {
      _overlayExpansion.reverse().whenCompleteOrCancel(() {
        _hideIfDismissed();
        if (mounted) action.onPressed?.call();
      });
    });
  }

  void _handleToggle() {''')
    replace(root, sidebar, '              onToggle: _closeOverlay,',
            '              onToggle: _closeOverlay,\n              footerAction: _overlayFooterAction,')
    replace(root, sidebar, '            onToggle: _handleToggle,',
            '            onToggle: _handleToggle,\n            footerAction: widget.footerAction,')
    replace(root, sidebar, '    required this.onToggle,\n  });',
            '    required this.onToggle,\n    this.footerAction,\n  });')
    replace(root, sidebar, '  final VoidCallback onToggle;',
            '  final VoidCallback onToggle;\n  final NavigationAction? footerAction;')
    replace(root, sidebar, '''          const SizedBox(height: _verticalInset),
        ],
      ),
    );
  }
}

class _SidebarButton''', '''          if (footerAction case final action?) ...[
            const SizedBox(height: _toggleGap),
            _SidebarButton(
              colors: colors,
              height: itemHeight,
              sideInset: sideInset,
              tooltip: rowTooltip(action.label),
              onTap: action.isLoading ? null : action.onPressed,
              child: _DestinationRow(
                destination: SidebarDestination(glyph: action.glyph, label: action.label),
                selected: false,
                width: rowWidth,
                iconInset: iconInset,
                style: labelStyle,
                labelOpacity: labelOpacity,
                loading: action.isLoading,
              ),
            ),
          ],
          const SizedBox(height: _verticalInset),
        ],
      ),
    );
  }
}

class _SidebarButton''')
    replace(root, sidebar, '  final VoidCallback onTap;', '  final VoidCallback? onTap;')
    replace(root, sidebar, '        selected: selected,\n        child: FocusRing(',
            '        selected: selected,\n        enabled: onTap != null,\n        child: FocusRing(')
    replace(root, sidebar, '    required this.labelOpacity,\n  });',
            '    required this.labelOpacity,\n    this.loading = false,\n  });')
    replace(root, sidebar, '  final double labelOpacity;',
            '  final double labelOpacity;\n  final bool loading;')
    replace(root, sidebar, '            AnimatedGlyph(glyph: destination.glyph, filled: selected),',
            '            if (loading) const ActivityIndicator(size: _iconSize)\n            else AnimatedGlyph(glyph: destination.glyph, filled: selected),')

    dock = 'plugins/prism_ui/lib/navigation_dock.dart'
    replace(root, dock, "import 'liquid_glass.dart';",
            "import 'liquid_glass.dart';\nimport 'navigation_action.dart';")
    replace(root, dock, '''  const NavigationDock({
    super.key,
    required this.destinations,
    required this.selectedIndex,
    required this.onSelected,
  });''', '''  const NavigationDock({
    super.key,
    required this.destinations,
    required this.selectedIndex,
    required this.onSelected,
    this.action,
  });''')
    replace(root, dock, '  final ValueChanged<int> onSelected;\n\n  static double heightOf',
            '  final ValueChanged<int> onSelected;\n  final NavigationAction? action;\n\n  static double heightOf')
    replace(root, dock, '''  static double insetOf(BuildContext context) =>
      heightOf(context) + _bottomMarginOf(context);''', '''  static double insetOf(BuildContext context, {bool hasAction = false}) =>
      heightOf(context) + _bottomMarginOf(context) +
      (hasAction ? NavigationActionRow.heightOf(context) : 0);''')
    replace(root, dock, '      height: _shadowRoom + height + _bottomMarginOf(context),',
            '      height: _shadowRoom + height + _bottomMarginOf(context) +\n        (action == null ? 0 : NavigationActionRow.heightOf(context)),')
    replace(root, dock, '      child: RepaintBoundary(child: LayoutBuilder(builder: (_, _) => dock)),', '''      child: RepaintBoundary(child: LayoutBuilder(builder: (_, _) => Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          if (action != null) Padding(
            padding: const EdgeInsets.symmetric(horizontal: _edgeMargin),
            child: NavigationActionRow(action: action!),
          ),
          dock,
        ],
      ))),''')

    bar = 'plugins/prism_ui/lib/navigation_bar.dart'
    replace(root, bar, "import 'package:prism_ui/typography.dart';",
            "import 'package:prism_ui/typography.dart';\nimport 'navigation_action.dart';")
    replace(root, bar, '    required this.onDestinationSelected,\n  });',
            '    required this.onDestinationSelected,\n    this.action,\n  });')
    replace(root, bar, '  final ValueChanged<int> onDestinationSelected;',
            '  final ValueChanged<int> onDestinationSelected;\n  final NavigationAction? action;')
    replace(root, bar, '      child: SafeArea(\n        child: SizedBox(', '''      child: SafeArea(
        child: Column(mainAxisSize: MainAxisSize.min, children: [
          if (action != null) NavigationActionRow(action: action!),
          SizedBox(''')
    replace(root, bar, '''          ),
        ),
      ),
    );
  }
}

class _Destination''', '''          ),
        ),
        ]),
      ),
    );
  }
}

class _Destination''')

    replace(root, 'lib/application.dart', "import 'package:fl_clash/ui/ui.dart';",
            "import 'package:fl_clash/ui/ui.dart';\nimport 'package:fl_clash/web_auth/logout_navigation.dart';")
    replace(root, 'lib/application.dart',
            '              terminateSignals: _terminateSignals,\n              child: child!,',
            '              terminateSignals: _terminateSignals,\n              child: WebLogoutScope(child: child!),')
    replace(root, 'lib/manager/app_manager.dart', "import 'package:fl_clash/widgets/icon.dart';",
            "import 'package:fl_clash/widgets/icon.dart';\nimport 'package:fl_clash/web_auth/logout_navigation.dart';")
    replace(root, 'lib/manager/app_manager.dart',
            '      selectedIndex: currentIndex,\n      expanded:',
            '      selectedIndex: currentIndex,\n      footerAction: webLogoutNavigationAction(context),\n      expanded:')
    home = 'lib/pages/home.dart'
    replace(root, home, "import 'package:fl_clash/widgets/widgets.dart';",
            "import 'package:fl_clash/widgets/widgets.dart';\nimport 'package:fl_clash/web_auth/logout_navigation.dart';")
    replace(root, home, '''      appSettingProvider.select((state) => state.floatingNavigationBar),
    );''', '''      appSettingProvider.select((state) => state.floatingNavigationBar),
    );
    final navigationAction = webLogoutNavigationAction(context);''')
    replace(root, home, 'NavigationDock.insetOf(context)',
            'NavigationDock.insetOf(context, hasAction: navigationAction != null)')
    replace(root, home, '                        child: NavigationDock(',
            '                        child: NavigationDock(\n                          action: navigationAction,')
    replace(root, home, '              child: NavigationBar(',
            '              child: NavigationBar(\n                action: navigationAction,')

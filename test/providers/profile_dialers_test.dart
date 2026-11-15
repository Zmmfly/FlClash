import 'dart:io';

import 'package:drift/native.dart';
import 'package:fl_clash/common/common.dart';
import 'package:fl_clash/core/controller.dart';
import 'package:fl_clash/core/interface.dart';
import 'package:fl_clash/core/method.dart';
import 'package:fl_clash/database/database.dart' as db;
import 'package:fl_clash/enum/enum.dart';
import 'package:fl_clash/l10n/l10n.dart';
import 'package:fl_clash/models/models.dart';
import 'package:fl_clash/providers/providers.dart';
import 'package:fl_clash/state.dart';
import 'package:flutter/widgets.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:mocktail/mocktail.dart';
import 'package:package_info_plus/package_info_plus.dart';
import 'package:yaml/yaml.dart';

import '../helpers/app_paths.dart';
import '../helpers/test_profiles.dart';

class _Core extends Mock implements CoreHandlerInterface {
  _Core() {
    when(() => getProxies()).thenAnswer(
      (_) async => const ProxiesData(
        proxies: {
          'GLOBAL': {
            'type': 'Selector',
            'all': ['DIRECT'],
            'now': 'DIRECT',
          },
          'DIRECT': {'type': 'Direct'},
        },
        all: ['GLOBAL'],
      ),
    );
    when(() => getExternalProviders()).thenAnswer((_) async => []);
  }
}

const _empty = SetupState(
  profileId: null,
  profileLastUpdateDate: null,
  profileType: ProfileType.file,
  extendType: ExtendType.standard,
  rules: [],
  proxyGroups: [],
  addedRules: [],
  script: null,
  overrides: ProfileOverrides(),
);

class _CompleteProfile extends SetupAction {
  @override
  Future<BuiltProfile> getProfile({
    required SetupState setupState,
    required PatchClashConfig patchConfig,
  }) async => (
    yaml: 'proxies: []\nrules: []\n',
    md5: 'complete',
    counts: (groups: 0, proxies: 0, rules: 0),
    base: null,
  );
}

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  late db.Database database;
  late Directory directory;

  setUpAll(() {
    registerFallbackValue(
      const SetupParams(
        selectedMap: {},
        options: CoreOptions(testUrl: ''),
      ),
    );
    registerFallbackValue(const SetupBase(path: '', overlay: ''));
  });

  setUp(() async {
    database = db.Database(NativeDatabase.memory());
    db.database = database;
    directory = Directory.systemTemp.createTempSync('profile-dialers-');
    await useAppPaths(directory);
    await AppLocalizations.load(const Locale('en'));
    globalState.lastConfigMd5 = null;
    globalState.lastConfigCounts = null;
    globalState.packageInfo = PackageInfo(
      appName: 'FlClash',
      packageName: 'test',
      version: '0',
      buildNumber: '0',
    );
  });

  tearDown(() async {
    await database.close();
    await directory.delete(recursive: true);
    globalState.lastConfigMd5 = null;
    globalState.lastConfigCounts = null;
  });

  test(
    'standard setup merges only dialers, script retains but ignores them, custom keeps its own override path',
    () async {
      final own = ProfileOverrides(
        dialerProxies: DialerProxyOverrides.values({'A': 'B', 'B': ''}),
        ntp: const Ntp(enable: true),
        ntpOverrideKeys: {NtpOverrideKey.enable},
        dns: const Dns(enable: false),
        dnsOverrideKeys: {DnsOverrideKey.enable},
      );
      for (final type in [
        ProfileType.file,
        ProfileType.url,
        ProfileType.custom,
      ]) {
        for (final extend in [ExtendType.standard, ExtendType.script]) {
          final profile = Profile(
            id: 1,
            type: type,
            extendType: extend,
            autoUpdateDuration: Duration.zero,
            overrides: own,
          );
          final container = ProviderContainer(
            overrides: [
              profilesProvider.overrideWith(() => TestProfiles([profile])),
              patchClashConfigProvider.overrideWithBuild(
                (_, _) => const PatchClashConfig(
                  dns: Dns(enable: true),
                  dnsOverrideKeys: {DnsOverrideKey.enable},
                ),
              ),
            ],
          );
          final state = await container.read(setupStateProvider(1).future);
          if (type == ProfileType.custom) {
            expect(state.overrides.ntp.enable, isTrue);
            expect(state.overrides.dialerProxies, own.dialerProxies);
            expect(state.proxyDialers, isEmpty);
          } else {
            expect(state.overrides.ntp, const Ntp());
            expect(state.overrides.dns.enable, isTrue);
            expect(
              state.overrides.dialerProxies.validated,
              extend == ExtendType.standard ? {'A': 'B', 'B': ''} : isEmpty,
            );
          }
          expect(
            container.read(profileProvider(1))!.overrides.dialerProxies,
            own.dialerProxies,
          );
          container.dispose();
        }
      }
    },
  );

  test(
    'unreadable profile edits are explicitly rejected until an explicit reset',
    () async {
      final profile = Profile.normal(label: 'unreadable').copyWith(
        id: 1,
        overrides: const ProfileOverrides(
          dialerProxies: DialerProxyOverrides.invalidDocument('{broken'),
        ),
      );
      await database.profiles.put(profile.toCompanion());
      final container = ProviderContainer();
      addTearDown(container.dispose);
      container.listen(profilesProvider, (_, _) {});
      await container.read(profilesStreamProvider.future);
      final action = container.read(profilesProvider.notifier);
      expect(
        () => action.updateProfile(
          1,
          (profile) => profile.copyWith(label: 'changed'),
        ),
        throwsA(isA<OutboundPolicyException>()),
      );
      expect(
        () => action.put(profile.copyWith(label: 'changed')),
        throwsA(isA<OutboundPolicyException>()),
      );
      expect(container.read(profileProvider(1))!.label, 'unreadable');
      action.resetDialerOverrides(1);
      expect(
        container.read(profileProvider(1))!.overrides.dialerProxies.isValid,
        isTrue,
      );
      await pumpEventQueue();
      expect(
        (await database.profilesDao.get(1))!.overrides.dialerProxies.isValid,
        isTrue,
      );
    },
  );

  test(
    'explicitly resetting an invalid field keeps unrelated readable overrides',
    () async {
      final profile = Profile(
        id: 1,
        autoUpdateDuration: Duration.zero,
        overrides: ProfileOverrides.safeFromJson({
          'dialer-proxies': null,
          'ntp': {'enable': true},
        }),
      );
      await database.profiles.put(profile.toCompanion());
      final container = ProviderContainer();
      addTearDown(container.dispose);
      container.listen(profilesProvider, (_, _) {});
      await container.read(profilesStreamProvider.future);
      container.read(profilesProvider.notifier).resetDialerOverrides(1);
      final read = container.read(profileProvider(1))!;
      expect(read.overrides.dialerProxies.validated, isEmpty);
      expect(read.overrides.ntp.enable, isTrue);
      await pumpEventQueue();
    },
  );

  test(
    'base build keeps bulk sections in Go and carries mappings into preview and digest',
    () async {
      final core = _Core();
      when(() => core.getProfileBase(any())).thenAnswer(
        (_) async =>
            const ProfileBase(config: {}, digest: 'base', proxies: 10000),
      );
      final container = ProviderContainer(
        overrides: [
          profilesProvider.overrideWith(TestProfiles.new),
          coreHandlerProvider.overrideWithValue(CoreController.scoped(core)),
          networkSettingProvider.overrideWithBuild(
            (_, _) => const NetworkProps(bypassPrivateRoute: false),
          ),
        ],
      );
      addTearDown(container.dispose);
      final action = container.read(setupActionProvider.notifier);
      final state = _empty.copyWith(
        profileId: 1,
        overrides: ProfileOverrides(
          dialerProxies: DialerProxyOverrides.values({'A': 'B', 'B': ''}),
        ),
      );
      final built = await action.getProfile(
        setupState: state,
        patchConfig: const PatchClashConfig(),
      );
      expect(built.base!.dialerProxies, {'A': 'B', 'B': ''});
      expect(built.counts.proxies, 10000);
      verifyNever(() => core.readConfig(any()));
      final changed = await action.getProfile(
        setupState: state.copyWith(
          overrides: ProfileOverrides(
            dialerProxies: DialerProxyOverrides.values({'A': 'C'}),
          ),
        ),
        patchConfig: const PatchClashConfig(),
      );
      expect(changed.md5, isNot(built.md5));
      final script = await action.getProfile(
        setupState: state.copyWith(extendType: ExtendType.script),
        patchConfig: const PatchClashConfig(),
      );
      expect(script.base!.dialerProxies, isNull);
    },
  );

  test(
    'custom builds keep id-table dialers and ignore standard name overrides',
    () async {
      final core = _Core();
      final container = ProviderContainer(
        overrides: [
          profilesProvider.overrideWith(TestProfiles.new),
          coreHandlerProvider.overrideWithValue(CoreController.scoped(core)),
          networkSettingProvider.overrideWithBuild(
            (_, _) => const NetworkProps(bypassPrivateRoute: false),
          ),
        ],
      );
      addTearDown(container.dispose);
      final state = _empty.copyWith(
        profileId: 1,
        profileType: ProfileType.custom,
        appProxies: [
          const CustomProxy(id: 10, definition: {'name': 'A', 'type': 'ss'}),
        ],
        proxyDialers: {10: 'DIRECT'},
        proxyGroups: [
          const ProxyGroup(
            id: 1,
            name: 'Group',
            type: GroupType.Selector,
            proxies: ['A'],
          ),
        ],
        overrides: ProfileOverrides(
          dialerProxies: DialerProxyOverrides.values({'A': 'missing'}),
        ),
      );
      final built = await container
          .read(setupActionProvider.notifier)
          .getProfile(setupState: state, patchConfig: const PatchClashConfig());
      expect(built.base, isNull);
      final config = loadYaml(built.yaml) as YamlMap;
      expect(config['proxies'], [
        {'name': 'A', 'type': 'ss', 'dialer-proxy': 'DIRECT'},
      ]);
      expect(built.yaml, isNot(contains('missing')));
      verifyNever(() => core.getProfileBase(any()));
      verifyNever(() => core.readConfig(any()));
    },
  );

  test(
    'policy rejection restores last-good apply identity and never sends an empty fallback',
    () async {
      final profile = Profile.normal(label: 'profile').copyWith(
        id: 1,
        overrides: ProfileOverrides(
          dialerProxies: DialerProxyOverrides.values({'A': 'B'}),
        ),
      );
      final core = _Core();
      when(() => core.getProfileBase(any())).thenAnswer(
        (_) async => const ProfileBase(config: {}, digest: 'base', proxies: 2),
      );
      final pushed = <SetupParams>[];
      when(() => core.setupConfig(any())).thenAnswer((invocation) async {
        final params = invocation.positionalArguments.single as SetupParams;
        pushed.add(params);
        if (params.base?.dialerProxies?['A'] == 'A') {
          throw const CoreMethodException(
            code: 'outbound_policy_invalid',
            message: 'A -> A',
          );
        }
      });
      final container = ProviderContainer(
        overrides: [
          profilesProvider.overrideWith(() => TestProfiles([profile])),
          currentProfileIdProvider.overrideWithBuild((_, _) => 1),
          coreHandlerProvider.overrideWithValue(CoreController.scoped(core)),
          networkSettingProvider.overrideWithBuild(
            (_, _) => const NetworkProps(bypassPrivateRoute: false),
          ),
          setupStateProvider(1).overrideWith(
            (ref) async => _empty.copyWith(
              profileId: 1,
              overrides: ref.watch(profileProvider(1))!.overrides,
            ),
          ),
        ],
      );
      addTearDown(container.dispose);
      globalState.container = container;
      final action = container.read(setupActionProvider.notifier);
      expect(await action.applyProfile(force: true), isTrue);
      final digest = globalState.lastConfigMd5;
      final counts = globalState.lastConfigCounts;
      container
          .read(profilesProvider.notifier)
          .updateProfile(
            1,
            (profile) => profile.copyWith(
              overrides: ProfileOverrides(
                dialerProxies: DialerProxyOverrides.values({'A': 'A'}),
              ),
            ),
          );
      expect(await action.applyProfile(), isFalse);
      expect(pushed, hasLength(2));
      expect(
        pushed.every(
          (params) => params.base != null && params.configPath == null,
        ),
        isTrue,
      );
      expect(globalState.lastConfigMd5, digest);
      expect(globalState.lastConfigCounts, counts);
      container
          .read(profilesProvider.notifier)
          .updateProfile(1, (_) => profile);
      expect(await action.inputsApplied(), isTrue);
    },
  );

  for (final rejection in [false, true]) {
    test(
      'complete candidate ${rejection ? 'rejection' : 'success'} never overwrites last-good and cleans only its own file',
      () async {
        final core = _Core();
        final lastGood = File(await appPath.configFilePath);
        await lastGood.writeAsString('last-good');
        final unrelated = File('${directory.path}/unrelated');
        await unrelated.writeAsString('keep');
        String? candidate;
        when(() => core.setupConfig(any())).thenAnswer((invocation) async {
          final params = invocation.positionalArguments.single as SetupParams;
          candidate = params.configPath;
          expect(params.base, isNull);
          expect(candidate, isNot(lastGood.path));
          expect(
            await File(candidate!).readAsString(),
            'proxies: []\nrules: []\n',
          );
          expect(await lastGood.readAsString(), 'last-good');
          if (rejection) {
            throw const CoreMethodException(
              code: 'outbound_policy_invalid',
              message: 'loop',
            );
          }
        });
        final container = ProviderContainer(
          overrides: [
            profilesProvider.overrideWith(TestProfiles.new),
            currentProfileProvider.overrideWithValue(null),
            setupStateProvider.overrideWith((_, _) => _empty),
            coreHandlerProvider.overrideWithValue(CoreController.scoped(core)),
            setupActionProvider.overrideWith(_CompleteProfile.new),
          ],
        );
        addTearDown(container.dispose);
        globalState.container = container;
        globalState.lastConfigMd5 = 'last-good-md5';
        expect(
          await container
              .read(setupActionProvider.notifier)
              .applyProfile(force: true),
          !rejection,
        );
        expect(await lastGood.readAsString(), 'last-good');
        expect(File(candidate!).existsSync(), isFalse);
        expect(await unrelated.readAsString(), 'keep');
        expect(
          globalState.lastConfigMd5,
          rejection ? 'last-good-md5' : 'complete',
        );
      },
    );
  }

  test(
    'invalid stored policy is rejected before authorization, profile reads or empty fallback setup',
    () async {
      final core = _Core();
      final profile = Profile.normal(label: 'broken').copyWith(
        overrides: ProfileOverrides.safeFromJson({'dialer-proxies': null}),
      );
      final container = ProviderContainer(
        overrides: [
          profilesProvider.overrideWith(() => TestProfiles([profile])),
          currentProfileIdProvider.overrideWithBuild((_, _) => profile.id),
          coreHandlerProvider.overrideWithValue(CoreController.scoped(core)),
        ],
      );
      addTearDown(container.dispose);
      globalState.container = container;
      globalState.lastConfigMd5 = 'last-good';
      expect(
        await container
            .read(setupActionProvider.notifier)
            .applyProfile(force: true),
        isFalse,
      );
      verifyNever(() => core.setupConfig(any()));
      verifyNever(() => core.getProfileBase(any()));
      expect(globalState.lastConfigMd5, 'last-good');
    },
  );
}

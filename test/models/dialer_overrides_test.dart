import 'dart:convert';

import 'package:fl_clash/models/models.dart';
import 'package:flutter_test/flutter_test.dart';

Map<String, Object?> _roundTrip(ProfileOverrides overrides) =>
    jsonDecode(jsonEncode(overrides.toJson())) as Map<String, Object?>;

void main() {
  group('ProfileOverrides dialer-proxies', () {
    test('missing and empty maps inherit every original dialer', () {
      for (final json in <Map<String, Object?>>[
        {},
        {'dialer-proxies': {}},
      ]) {
        expect(
          ProfileOverrides.fromJson(json).dialerProxies.validated,
          isEmpty,
        );
        expect(
          ProfileOverrides.safeFromJson(json).dialerProxies.validated,
          isEmpty,
        );
      }
    });

    test(
      'clear and multi-hop targets preserve exact source and target names',
      () {
        const mapping = {' A ': ' B ', ' B ': 'C', 'C': ''};
        final overrides = ProfileOverrides.safeFromJson({
          'dialer-proxies': mapping,
        });
        expect(overrides.dialerProxies.validated, mapping);
        expect(_roundTrip(overrides)['dialer-proxies'], mapping);
        expect(overrides.dialerProxies.withTarget(' A ', null).validated, {
          ' B ': 'C',
          'C': '',
        });
        expect(
          overrides.dialerProxies.withTarget('C', 'D').validated['C'],
          'D',
        );
        expect(
          () => overrides.dialerProxies.validated['C'] = 'D',
          throwsUnsupportedError,
        );
      },
    );

    for (final value in <Object?>[
      null,
      7,
      [],
      {'A': 'B', 'C': 7},
    ]) {
      test(
        'invalid $value is retained by strict/safe decode, copy and JSON',
        () {
          for (final decode in [
            ProfileOverrides.fromJson,
            ProfileOverrides.safeFromJson,
          ]) {
            final overrides = decode({'dialer-proxies': value});
            expect(
              overrides.dialerProxies.status,
              DialerOverridesStatus.invalidField,
            );
            expect(
              () => overrides.dialerProxies.validated,
              throwsA(
                isA<OutboundPolicyException>().having(
                  (error) => error.path,
                  'path',
                  'profile.overrides.dialer-proxies',
                ),
              ),
            );
            final copied = overrides.copyWith(dns: const Dns(enable: true));
            expect(copied.dialerProxies, overrides.dialerProxies);
            expect(_roundTrip(copied)['dialer-proxies'], value);
            expect(
              ProfileOverrides.safeFromJson(_roundTrip(copied)).dialerProxies,
              overrides.dialerProxies,
            );
          }
        },
      );
    }

    test('legacy field salvage never salvages a mixed dialer map entry', () {
      final overrides = ProfileOverrides.safeFromJson({
        'dns': {'enable': true, 'listen': 7},
        'ntp': {'enable': true},
        'dialer-proxies': {'A': 'B', 'C': false},
      });
      expect(overrides.dns.enable, isTrue);
      expect(overrides.dns.listen, '');
      expect(overrides.ntp.enable, isTrue);
      expect(overrides.dialerProxies.isValid, isFalse);
      expect(_roundTrip(overrides)['dialer-proxies'], {'A': 'B', 'C': false});
    });

    test('profile JSON export/import and copying retain an invalid field', () {
      final profile = Profile(
        id: 1,
        autoUpdateDuration: Duration.zero,
        overrides: ProfileOverrides.safeFromJson({
          'dialer-proxies': {'A': 'B', 'B': null},
        }),
      );
      final imported = Profile.fromJson(
        jsonDecode(jsonEncode(profile.toJson())),
      );
      expect(
        imported.copyWith(id: 2).overrides.dialerProxies,
        profile.overrides.dialerProxies,
      );
      expect(imported.overrides.dialerProxies.toJson(), {'A': 'B', 'B': null});
    });

    test('invalid document cannot masquerade as ordinary JSON export', () {
      const dialers = DialerProxyOverrides.invalidDocument('{broken');
      expect(dialers.isUnreadable, isTrue);
      expect(() => dialers.toJson(), throwsA(isA<OutboundPolicyException>()));
      expect(
        () =>
            jsonEncode(const ProfileOverrides(dialerProxies: dialers).toJson()),
        throwsA(isA<OutboundPolicyException>()),
      );
    });

    test('valid JSON values never throw during dialer field decoding', () {
      for (final encoded in [
        'null',
        'false',
        '7',
        '"A"',
        '[]',
        '{}',
        '{"A":[1]}',
      ]) {
        expect(
          () => DialerProxyOverrides.fromJson(jsonDecode(encoded)),
          returnsNormally,
        );
      }
    });
  });

  test(
    'setup protocol separates base mappings and complete candidate paths',
    () {
      const base = SetupBase(
        path: '/profiles/1.yaml',
        overlay: 'mode: rule',
        dialerProxies: {'A': 'B', 'B': ''},
      );
      final json = jsonDecode(jsonEncode(base.toJson()));
      expect(json['dialer-proxies'], {'A': 'B', 'B': ''});
      expect(SetupBase.fromJson(json).dialerProxies, base.dialerProxies);
      const params = SetupParams(
        selectedMap: {},
        options: CoreOptions(testUrl: ''),
        configPath: '/tmp/candidate.yaml',
      );
      expect(params.toJson()['config-path'], '/tmp/candidate.yaml');
      expect(params.toJson().containsKey('base'), isFalse);
      expect(
        const SetupBase(
          path: '',
          overlay: '',
        ).toJson().containsKey('dialer-proxies'),
        isFalse,
      );
    },
  );
}

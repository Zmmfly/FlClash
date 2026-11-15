import 'dart:convert';

import 'package:drift/native.dart';
import 'package:fl_clash/database/database.dart';
import 'package:fl_clash/models/models.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  const converter = ProfileOverridesConverter();

  test('SQL NULL and legacy empty object remain valid empty overrides', () {
    expect(converter.fromSql(null), isNull);
    expect(converter.fromSql('{}')!.dialerProxies.validated, isEmpty);
    expect(converter.toSql(const ProfileOverrides()), isNull);
  });

  for (final raw in ['{"dialer-proxies":', 'null', '[]', '7', '"object"']) {
    test('unreadable SQL $raw remains byte-for-byte recoverable', () {
      final overrides = converter.fromSql(raw)!;
      expect(overrides.dialerProxies.isUnreadable, isTrue);
      expect(converter.toSql(overrides), raw);
      expect(converter.toSql(overrides.copyWith()), raw);
      expect(
        () => converter.toSql(overrides.copyWith(dns: const Dns(enable: true))),
        throwsA(isA<OutboundPolicyException>()),
      );
      expect(
        converter.toSql(
          overrides.copyWith(dialerProxies: const DialerProxyOverrides()),
        ),
        isNull,
      );
    });
  }

  test(
    'invalid field survives unrelated override edits and SQL round trips',
    () {
      final overrides = converter.fromSql(
        '{"dialer-proxies":null,"ntp":{"enable":true}}',
      )!;
      final changed = overrides.copyWith(dns: const Dns(enable: true));
      final encoded = converter.toSql(changed)!;
      expect(jsonDecode(encoded)['dialer-proxies'], isNull);
      final read = converter.fromSql(encoded)!;
      expect(read.dialerProxies.status, DialerOverridesStatus.invalidField);
      expect(read.ntp.enable, isTrue);
      expect(read.dns.enable, isTrue);
    },
  );

  test(
    'a damaged row stays listed and copying it does not erase the raw document',
    () async {
      final db = Database(NativeDatabase.memory());
      addTearDown(db.close);
      final profile = Profile.normal(label: 'damaged').copyWith(id: 11);
      await db.profiles.put(profile.toCompanion());
      const raw = '{"dialer-proxies":';
      await db.customStatement(
        'UPDATE profiles SET overrides = ? WHERE id = ?',
        [raw, 11],
      );
      final read = (await db.profilesDao.query().get()).single;
      expect(read.overrides.dialerProxies.isUnreadable, isTrue);
      await db.profiles.put(read.copyWith(id: 12).toCompanion());
      final rows = await db
          .customSelect('SELECT overrides FROM profiles ORDER BY id')
          .get();
      expect(rows.map((row) => row.read<String>('overrides')), [raw, raw]);
      await db.profiles.put(
        read.copyWith(overrides: const ProfileOverrides()).toCompanion(),
      );
      expect(
        (await db.profilesDao.get(11))!.overrides.dialerProxies.validated,
        isEmpty,
      );
    },
  );
}

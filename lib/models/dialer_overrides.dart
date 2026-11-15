import 'package:collection/collection.dart';

final class OutboundPolicyException implements Exception {
  final String path;
  final String reason;

  const OutboundPolicyException(this.path, this.reason);

  String get code => 'outbound_policy_invalid';

  @override
  String toString() => '$code: $path: $reason';
}

enum DialerOverridesStatus { valid, invalidField, invalidDocument }

final class DialerProxyOverrides {
  final DialerOverridesStatus status;
  final Map<String, String> _values;
  final Object? _rawField;
  final String? rawDocument;

  const DialerProxyOverrides()
    : status = DialerOverridesStatus.valid,
      _values = const {},
      _rawField = null,
      rawDocument = null;

  DialerProxyOverrides.values(Map<String, String> values)
    : status = DialerOverridesStatus.valid,
      _values = Map.unmodifiable(values),
      _rawField = null,
      rawDocument = null;

  DialerProxyOverrides._invalidField(Object? value)
    : status = DialerOverridesStatus.invalidField,
      _values = const {},
      _rawField = _freezeJson(value),
      rawDocument = null;

  const DialerProxyOverrides.invalidDocument(String document)
    : status = DialerOverridesStatus.invalidDocument,
      _values = const {},
      _rawField = null,
      rawDocument = document;

  factory DialerProxyOverrides.fromJson(Object? value) {
    if (value is DialerProxyOverrides) return value;
    if (value is Map &&
        value.entries.every(
          (entry) => entry.key is String && entry.value is String,
        )) {
      return DialerProxyOverrides.values(Map<String, String>.from(value));
    }
    return DialerProxyOverrides._invalidField(value);
  }

  bool get isValid => status == DialerOverridesStatus.valid;

  bool get isUnreadable => status == DialerOverridesStatus.invalidDocument;

  Map<String, String> get validated {
    if (!isValid) {
      throw OutboundPolicyException(
        isUnreadable ? 'profile.overrides' : 'profile.overrides.dialer-proxies',
        isUnreadable
            ? 'unreadable JSON document'
            : 'expected a string-to-string map',
      );
    }
    return _values;
  }

  DialerProxyOverrides withTarget(String source, String? target) {
    final next = Map<String, String>.from(validated);
    if (target == null) {
      next.remove(source);
    } else {
      next[source] = target;
    }
    return DialerProxyOverrides.values(next);
  }

  Object? toJson() {
    if (isUnreadable) {
      throw const OutboundPolicyException(
        'profile.overrides',
        'unreadable SQL document cannot be exported as JSON',
      );
    }
    return isValid ? _values : _rawField;
  }

  static const _equality = DeepCollectionEquality();

  @override
  bool operator ==(Object other) =>
      identical(this, other) ||
      other is DialerProxyOverrides &&
          status == other.status &&
          rawDocument == other.rawDocument &&
          _equality.equals(_values, other._values) &&
          _equality.equals(_rawField, other._rawField);

  @override
  int get hashCode => Object.hash(
    status,
    rawDocument,
    _equality.hash(_values),
    _equality.hash(_rawField),
  );
}

Object? _freezeJson(Object? value) => switch (value) {
  Map() => Map<Object?, Object?>.unmodifiable({
    for (final entry in value.entries) entry.key: _freezeJson(entry.value),
  }),
  List() => List<Object?>.unmodifiable(value.map(_freezeJson)),
  _ => value,
};

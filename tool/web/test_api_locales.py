"""Web consent/refusal copy contract; workflow clicks and routes need separate tests.

These fixtures verify text, not that any Web capability has been implemented.
"""

import json
from pathlib import Path
import re
import unittest


ROOT = Path(__file__).resolve().parents[2]
LOCALES = ('en', 'zh_CN', 'ja', 'ru', 'fr', 'es', 'ko')
ENGLISH = {
    'webServerNetworkTitle': 'Server network access',
    'webServerNetworkDescription': 'This accesses the network of the server or container, not this browser. Allow {host}:{port} for this source?',
    'webServerNetworkAddresses': 'Resolved addresses: {addresses}',
    'webServerNetworkRedirectTip': 'A redirect to a different destination requires separate permission.',
    'webBackupSensitiveTitle': 'Sensitive backup data',
    'webBackupSensitiveDescription': 'Backups can include proxy passwords and subscription tokens. Excluding WebDAV does not remove all sensitive data.',
    'webBackupIncludeDavCredentials': 'Include WebDAV connection and credentials',
    'webBackupRestoreDavCredentials': 'Restore WebDAV connection and credentials',
    'webBackupDavExcludedTip': 'The entire WebDAV connection is excluded. On restore, the current connection stays unchanged unless you explicitly choose to replace it.',
    'webBackupLegacyConversionTitle': 'Review legacy conversion',
    'webBackupLegacyConversionTip': 'Review every listed change before restoring this backup. Only confirm if you accept exactly these changes.',
    'webBackupConfirmConversion': 'Confirm listed changes and restore',
    'webApiConflictTitle': 'Data changed on the server',
    'webApiConflictTip': 'Your edit is still here. Reload the latest version before applying it again.',
    'webApiCommittedRefreshTip': 'The change was committed, but the latest view could not be loaded. Refresh without resubmitting the change.',
    'webApiUnknownResultTip': 'The result is not known yet. Resolve this operation before trying a new one.',
    'webApiResolveOperation': 'Resolve operation',
    'webApiReload': 'Reload latest version',
    'webDavRemoteMatchesTip': 'The remote bytes currently match this backup. This is an observation, not proof that the upload ran exactly once.',
    'webDavRemoteDiffersTip': 'The remote bytes do not match this backup. The original upload is still uncertain; do not retry it automatically.',
    'webProviderSavedApplyTip': 'The provider content was saved, but the running configuration has not changed. Rebuild and apply it now?',
    'webApiRecoveryTitle': 'Recover an unfinished operation',
    'webApiReadReceipt': 'Check operation receipt',
    'webApiRetrySameOperation': 'Retry the same operation',
    'webApiAcknowledgeUnrecoverable': 'Acknowledge as unrecoverable',
    'webApiAcknowledgeUnrecoverableTip': 'This does not undo any committed change. If the original store or session can no longer recover this operation, acknowledge it only after checking the server state. No request will be resubmitted.',
    'webApiRecoveryBlockedTip': 'This operation cannot be recovered in the current origin, store or session. Return to the original connection, or explicitly acknowledge it as unrecoverable.',
    'webUpdateDeploymentHint': 'The browser can only open the releases page. To update the service, redeploy the container or service; it cannot be installed in the browser.',
}


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f'duplicate key: {key}')
        result[key] = value
    return result


class WebApiLocaleTest(unittest.TestCase):
    def test_web_consent_and_refusal_copy_contract(self):
        reference_keys = None
        for locale in LOCALES:
            with self.subTest(locale=locale):
                path = ROOT / 'core' / 'webui' / f'intl_{locale}.arb'
                text = path.read_text(encoding='utf-8')
                self.assertTrue(text.endswith('\n'))
                self.assertTrue(all(line == line.rstrip() for line in text.splitlines()))
                data = json.loads(text, object_pairs_hook=unique_object)
                self.assertIsInstance(data, dict)
                self.assertTrue(all(not key.startswith('@') for key in data))
                self.assertTrue(all(isinstance(value, str) and value.strip() for value in data.values()))
                if reference_keys is None:
                    reference_keys = set(data)
                self.assertEqual(reference_keys, set(data))
                self.assertTrue(ENGLISH.keys() <= data.keys())
                for key, expected in ENGLISH.items():
                    placeholders = lambda value: sorted(re.findall(r'\{[^{}]+\}', value))
                    self.assertEqual(placeholders(expected), placeholders(data[key]), key)
                    self.assertNotIn('base64', data[key].lower(), key)
                    if locale == 'en':
                        self.assertEqual(expected, data[key], key)


if __name__ == '__main__':
    unittest.main()

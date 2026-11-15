#!/usr/bin/env python3
"""Check documented container contracts without touching a deployed Compose project.

WEBPANEL.md / README container claims are exercised through Compose config and,
unless --skip-image is explicit, a disposable authenticated image smoke test.
"""

import argparse
import base64
import copy
import hashlib
import http.client
import json
import os
from pathlib import Path
import secrets
import shutil
import subprocess
import sys
import tempfile
import time
import uuid
from http.cookies import SimpleCookie

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[1]
SERVICE = 'flclash'


class SmokeFailure(RuntimeError):
    pass


def require(condition, message):
    if not condition:
        raise SmokeFailure(message)


def command(arguments, *, cwd, env, timeout=60, allow_failure=False):
    try:
        result = subprocess.run(arguments, cwd=cwd, env=env, stdin=subprocess.DEVNULL,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                timeout=timeout, check=False)
    except subprocess.TimeoutExpired:
        raise SmokeFailure('A Docker/Compose operation timed out; command output withheld') from None
    except OSError:
        raise SmokeFailure('Cannot execute Docker/Compose') from None
    if result.returncode and not allow_failure:
        raise SmokeFailure('A Docker/Compose operation failed; command output withheld')
    return result


class ContainerCheck:
    def __init__(self, directory, image, timeout):
        self.directory = directory
        self.image = image
        self.timeout = timeout
        self.project = 'flclash-smoke-' + uuid.uuid4().hex
        self.started = False
        self.port = None
        self.env = {key: value for key, value in os.environ.items()
                    if not key.startswith(('FLCLASH_', 'COMPOSE_'))}
        self.env['COMPOSE_DISABLE_ENV_FILE'] = '1'
        self.empty_env = directory / 'empty.env'
        self.empty_env.touch(mode=0o600)
        self.compose_file = directory / 'compose.yaml'
        shutil.copyfile(ROOT / 'compose.yaml', self.compose_file)
        self.runtime_file = directory / 'runtime.json'
        self.username = 'admin'
        self.password = secrets.token_urlsafe(36)
        self.env.update(FLCLASH_IMAGE=image, FLCLASH_WEB_USERNAME=self.username,
                        FLCLASH_WEB_SECURE_COOKIE='false')
        uid = os.getuid() or 1000
        gid = os.getgid() if os.getuid() else 1000
        self.env.update(FLCLASH_UID=str(uid), FLCLASH_GID=str(gid))
        self.uid = uid
        data = directory / 'data'
        data.mkdir(mode=0o700)
        auth = directory / 'auth'
        auth.mkdir(mode=0o700)
        state = directory / 'state'
        state.mkdir(mode=0o700)
        secret_dir = directory / 'secrets'
        secret_dir.mkdir(mode=0o700)
        secret_file = secret_dir / 'web_password'
        descriptor = os.open(secret_file, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(descriptor, 'w', encoding='utf-8') as output:
            output.write(self.password)
        secret_file.chmod(0o600)
        if os.getuid() == 0:
            os.chown(data, uid, gid)
            os.chown(auth, uid, gid)
            os.chown(state, uid, gid)
            os.chown(secret_file, uid, gid)
        require(secret_file.stat().st_mode & 0o777 == 0o600,
                'Temporary password secret must have mode 0600')

    def compose(self, arguments, *, file=None, env=None, allow_failure=False, timeout=60):
        return command(['docker', 'compose', '--project-name', self.project,
                        '--project-directory', str(self.directory), '--env-file', str(self.empty_env),
                        '-f', str(file or self.compose_file), *arguments],
                       cwd=self.directory, env=env or self.env, timeout=timeout,
                       allow_failure=allow_failure)

    def inspect(self, container, projection):
        return command(['docker', 'inspect', '--format', projection, container],
                       cwd=self.directory, env=self.env).stdout.decode().strip()

    def template(self):
        missing = dict(self.env)
        missing.pop('FLCLASH_WEB_USERNAME', None)
        rejected = self.compose(['config', '--format', 'json'], env=missing, allow_failure=True)
        require(rejected.returncode != 0 and b'FLCLASH_WEB_USERNAME' in rejected.stderr,
                'Compose must reject a missing FLCLASH_WEB_USERNAME')
        defaults = dict(self.env)
        defaults.pop('FLCLASH_UID', None)
        defaults.pop('FLCLASH_GID', None)
        default_config = json.loads(self.compose(['config', '--format', 'json'], env=defaults).stdout)
        require(default_config.get('services', {}).get(SERVICE, {}).get('user') == '1000:1000',
                'Compose default UID/GID must remain 1000:1000')
        config = json.loads(self.compose(['config', '--format', 'json']).stdout)
        services = config.get('services', {})
        require(set(services) == {SERVICE}, 'Expected exactly the repository flclash Compose service')
        service = services[SERVICE]
        require(service.get('image') == self.image, 'Compose image selection was not honored')
        require(service.get('pull_policy') == 'never', 'Compose must not pull an unrequested image')
        require(service.get('read_only') is True, 'Compose root filesystem must be read-only')
        require(not service.get('privileged') and not service.get('cap_add') and not service.get('devices'),
                'Compose must not grant privileged mode, additional capabilities, or host devices')
        require('ALL' in service.get('cap_drop', []), 'Compose must drop all capabilities')
        require('no-new-privileges:true' in service.get('security_opt', []),
                'Compose must prevent privilege escalation')
        require(service.get('network_mode') != 'host' and service.get('pid') != 'host',
                'Compose must not use host network/PID namespaces')
        require(not service.get('container_name') and not service.get('external_links'),
                'Compose must not select an existing container')
        require(str(service.get('user', '')).split(':')[0] not in ('', '0', 'root'),
                'Compose must run as a non-root user')
        require(any(str(item).startswith('/tmp:') and all(flag in str(item) for flag in
                    ('nosuid', 'nodev', 'noexec')) for item in service.get('tmpfs', [])),
                'Compose must provide a restricted /tmp tmpfs')
        environment = service.get('environment', {})
        require(environment.get('FLCLASH_DATA_DIR') == '/data' and
                environment.get('FLCLASH_WEB_AUTH_FILE') == '/auth/credentials.json' and
                environment.get('FLCLASH_WEB_API_STORE_DIR') == '/state' and
                environment.get('FLCLASH_WEB_PASSWORD_FILE') == '/run/secrets/web_password' and
                environment.get('FLCLASH_WEB_LISTEN') == '0.0.0.0:8080',
                'Compose container data, secret, and listen paths must match their contract')
        require(environment.get('FLCLASH_WEB_USERNAME') == 'admin' and
                not environment.get('FLCLASH_WEB_PASSWORD'),
                'Compose must select the test username and a file-only password')
        mounts = [mount for mount in service.get('volumes', []) if mount.get('target') == '/data']
        require(len(mounts) == 1 and mounts[0].get('type') == 'bind' and
                Path(mounts[0]['source']).resolve() == self.directory / 'data' and
                mounts[0].get('bind', {}).get('create_host_path') is False,
                'Relative ./data must resolve inside the disposable project without auto-creation')
        auth_mounts = [mount for mount in service.get('volumes', []) if mount.get('target') == '/auth']
        require(len(auth_mounts) == 1 and auth_mounts[0].get('type') == 'bind' and
                Path(auth_mounts[0]['source']).resolve() == self.directory / 'auth' and
                auth_mounts[0].get('bind', {}).get('create_host_path') is False,
                'Relative ./auth must persist credentials separately from the managed data directory')
        state_mounts = [mount for mount in service.get('volumes', []) if mount.get('target') == '/state']
        require(len(state_mounts) == 1 and state_mounts[0].get('type') == 'bind' and
                Path(state_mounts[0]['source']).resolve() == self.directory / 'state' and
                state_mounts[0].get('bind', {}).get('create_host_path') is False,
                'Relative ./state must privately persist the API authority outside data and auth')
        require(Path(config['secrets']['web_password']['file']).resolve() ==
                self.directory / 'secrets/web_password',
                'Relative secret file must resolve inside the disposable project')
        ports = service.get('ports', [])
        web = [port for port in ports if int(port.get('target', 0)) == 8080]
        require(len(web) == 1 and str(web[0].get('published')) == '18080' and
                web[0].get('host_ip') == '127.0.0.1',
                'Default Web publish must be 127.0.0.1:18080 -> 8080')
        require(ports and all(port.get('host_ip') == '127.0.0.1' for port in ports),
                'Default published ports must remain loopback-only')
        for port in ports:
            port['published'] = '0'
            port['host_ip'] = '127.0.0.1'
        config['name'] = self.project
        service['environment']['FLCLASH_WEB_SAFE_MODE'] = 'true'
        self.runtime_file.write_text(json.dumps(config), encoding='utf-8')
        self.compose(['config', '--quiet'], file=self.runtime_file)
        self.config = config
        print('PASS: Compose interpolation, relative mounts, isolated ports, and security template')

    def available_image(self):
        result = command(['docker', 'image', 'inspect', '--format', '{{.Id}}', self.image],
                         cwd=self.directory, env=self.env, allow_failure=True)
        require(result.returncode == 0 and result.stdout.strip().startswith(b'sha256:'),
                'Requested image is not available locally; build it first (image checks were not skipped)')
        existing = self.compose(['ps', '--all', '--quiet'], file=self.runtime_file).stdout.strip()
        require(not existing, 'Disposable project unexpectedly contains an existing container; refusing to use it')

    def container_id(self, file=None):
        ids = self.compose(['ps', '--all', '--quiet', SERVICE], file=file or self.runtime_file).stdout.decode().split()
        require(len(ids) == 1, 'Expected one container in the disposable Compose project')
        return ids[0]

    def no_credentials(self):
        for missing in ('username', 'password'):
            config = copy.deepcopy(self.config)
            service = config['services'][SERVICE]
            service['restart'] = 'no'
            environment = service['environment']
            if missing == 'username':
                environment['FLCLASH_WEB_USERNAME'] = ''
            else:
                environment['FLCLASH_WEB_PASSWORD_FILE'] = ''
            environment['FLCLASH_WEB_PASSWORD'] = ''
            file = self.directory / f'no-{missing}.json'
            file.write_text(json.dumps(config), encoding='utf-8')
            self.started = True
            self.compose(['up', '--detach', '--no-build', '--pull', 'never', '--force-recreate', SERVICE], file=file)
            container = self.container_id(file)
            deadline = time.monotonic() + min(self.timeout, 20)
            while time.monotonic() < deadline:
                state = self.inspect(container, '{{.State.Status}} {{.State.ExitCode}}').split()
                if state and state[0] in ('exited', 'dead'):
                    require(len(state) == 2 and state[1] != '0',
                            'Container must fail closed when credentials are absent')
                    break
                time.sleep(0.25)
            else:
                raise SmokeFailure('Container did not fail closed when credentials were absent')
        print('PASS: image refuses missing username and missing password')

    def start(self, recreate=False):
        arguments = ['up', '--detach', '--no-build', '--pull', 'never']
        if recreate:
            arguments.append('--force-recreate')
        arguments.append(SERVICE)
        self.started = True
        self.compose(arguments, file=self.runtime_file)
        container = self.container_id()
        endpoint = self.compose(['port', SERVICE, '8080'], file=self.runtime_file).stdout.decode().strip()
        address, separator, port = endpoint.rpartition(':')
        require(separator and address == '127.0.0.1' and port.isdigit() and int(port) > 0,
                'Disposable service must publish on a randomly assigned IPv4 loopback port')
        self.port = int(port)
        deadline = time.monotonic() + self.timeout
        while time.monotonic() < deadline:
            health = self.inspect(container, '{{if .State.Health}}{{.State.Health.Status}}{{else}}missing{{end}}')
            require(health != 'missing', 'Image must define a Docker healthcheck')
            if health == 'healthy':
                status, _, _ = self.request('GET', '/login')
                require(status == 200, 'Healthy container must return HTTP 200 for /login')
                return container
            state = self.inspect(container, '{{.State.Status}}')
            require(state == 'running', 'Authenticated container stopped before becoming healthy')
            time.sleep(1)
        raise SmokeFailure('Container did not reach Docker healthy state before the deadline')

    def runtime_security(self, container):
        projection = ('{"privileged":{{json .HostConfig.Privileged}},'
                      '"readonly":{{json .HostConfig.ReadonlyRootfs}},'
                      '"cap_add":{{json .HostConfig.CapAdd}},'
                      '"cap_drop":{{json .HostConfig.CapDrop}},'
                      '"network":{{json .HostConfig.NetworkMode}},'
                      '"security":{{json .HostConfig.SecurityOpt}}}')
        observed = json.loads(self.inspect(container, projection))
        require(not observed['privileged'] and not observed['cap_add'] and observed['readonly'] and
                'ALL' in observed['cap_drop'] and observed['network'] != 'host' and
                'no-new-privileges:true' in observed['security'],
                'Running container must retain Compose hardening')
        uid = self.compose(['exec', '-T', SERVICE, 'id', '-u'], file=self.runtime_file).stdout.decode().strip()
        require(uid == str(self.uid) and uid != '0', 'Running container must have the selected non-root UID')
        print('PASS: Docker healthcheck, login page, and effective non-root runtime hardening')

    def evaluator_licenses(self):
        guest = ROOT / 'core/web_api_evaluate_guest'
        manifest_bytes = (guest / 'manifest.json').read_bytes()
        manifest = json.loads(manifest_bytes)
        expected = dict(manifest['licenses'])
        require(set(expected) == {'LICENSE', 'LICENSE.wasi-libc', 'LICENSE.musl', 'LICENSE.compiler-rt'},
                'The embedded evaluator must account for every bundled runtime license')
        expected['manifest.json'] = hashlib.sha256(manifest_bytes).hexdigest()
        for name, digest in expected.items():
            require(hashlib.sha256((guest / name).read_bytes()).hexdigest() == digest,
                    'An evaluator license does not match its source manifest')
            packaged = self.compose(['exec', '-T', SERVICE, 'cat',
                                     '/usr/share/licenses/flclash/evaluator/' + name],
                                    file=self.runtime_file).stdout
            require(hashlib.sha256(packaged).hexdigest() == digest,
                    'The image must preserve the embedded evaluator license and provenance bytes')
        print('PASS: embedded evaluator licenses and provenance are present in the runtime image')

    def request(self, method, path, body=None, cookie=None, headers=None, include_origin=True):
        request_headers = dict(headers or {})
        if include_origin:
            request_headers['Origin'] = f'http://127.0.0.1:{self.port}'
        if cookie:
            request_headers['Cookie'] = cookie
        connection = http.client.HTTPConnection('127.0.0.1', self.port, timeout=10)
        try:
            connection.request(method, path, body=body, headers=request_headers)
            response = connection.getresponse()
            content = response.read(2 * 1024 * 1024)
            return response.status, response.headers, content
        finally:
            connection.close()

    def api(self, method, path, value=None, cookie=None, include_origin=True):
        status, _, body = self.request(
            method, path, body=None if value is None else json.dumps(value), cookie=cookie,
            headers={'Content-Type': 'application/json'}, include_origin=include_origin)
        try:
            data = json.loads(body)
        except (ValueError, UnicodeDecodeError):
            raise SmokeFailure('A typed API response was not JSON; body withheld') from None
        require(isinstance(data, dict), 'Typed APIs must return an object envelope')
        return status, data, body

    def login(self, password=None):
        body = json.dumps({'username': self.username, 'password': password if password is not None else self.password})
        return self.request('POST', '/api/auth/login', body=body,
                            headers={'Content-Type': 'application/json'})

    def authenticated_cookie(self):
        status, headers, body = self.login()
        require(status == 200 and json.loads(body).get('ok') is True, 'Correct credentials must authenticate')
        jar = SimpleCookie()
        jar.load(headers.get('Set-Cookie', ''))
        morsel = jar.get('flclash_session')
        require(morsel is not None and bool(morsel['httponly']) and
                str(morsel['samesite']).lower() == 'strict' and morsel['path'] == '/',
                'Login must create an HttpOnly SameSite=Strict root-scoped session cookie')
        return f'{morsel.key}={morsel.value}'

    def require_idle(self, cookie):
        status, envelope, _ = self.api('GET', '/api/v1/runtime/listener', cookie=cookie)
        data = envelope.get('data', {})
        require(status == 200 and data.get('enabled') is False and data.get('safeMode') is True,
                'Disposable API smoke must have immutable server safe mode and no proxy listeners')

    def authentication_and_persistence(self):
        for method, path, body in (
            ('GET', '/api/v1/meta', None),
            ('GET', '/api/v1/rules?scope=global', None),
            ('POST', '/api/v1/rules:create', b'{}'),
            ('GET', '/ws/v1/events', None),
            ('GET', '/api/auth/session', None),
            ('POST', '/api/auth/credentials', b'{}'),
        ):
            headers = {'Content-Type': 'application/json'}
            if path == '/ws/v1/events':
                headers.update(Connection='Upgrade', Upgrade='websocket',
                               **{'Sec-WebSocket-Version': '13',
                                  'Sec-WebSocket-Key': base64.b64encode(secrets.token_bytes(16)).decode()})
            require(self.request(method, path, body=body, headers=headers)[0] == 401,
                    'Unauthenticated business APIs, events and account requests must be rejected')
        status, headers, _ = self.request('GET', '/')
        require(status == 303 and headers.get('Location') == '/login',
                'Unauthenticated pages must redirect to /login')
        status, headers, _ = self.login(secrets.token_urlsafe(36))
        require(status == 401 and not headers.get('Set-Cookie'),
                'Wrong credentials must not create a session')
        cookie = self.authenticated_cookie()
        self.require_idle(cookie)
        for method, path in (
            ('GET', '/api/fs/snapshot'),
            ('GET', '/api/fs/read?path=%2Fstate%2Fresources.db'),
            ('GET', '/api/fs/read?path=%2Fauth%2Fcredentials.json'),
            ('GET', '/api/fs/stat?path=%2Fdata%2Fdatabase.sqlite'),
            ('GET', '/ws/core'),
            ('POST', '/api/fs/write?path=%2Fdata%2Fdatabase.sqlite'),
            ('POST', '/api/fs/delete?path=%2Fstate%2Fresources.db'),
            ('POST', '/api/fs/rename'),
            ('POST', '/api/fs/copy'),
            ('POST', '/api/http-proxy'),
            ('POST', '/api/v1/query'),
            ('POST', '/api/v1/execute'),
        ):
            require(self.request(method, path, body=b'{}' if method == 'POST' else None,
                                 cookie=cookie, headers={'Content-Type': 'application/json'})[0] == 404,
                    'Legacy filesystem, generic SQL/HTTP and native RPC tunnels must not be exposed')
        status, headers, body = self.request('GET', '/sqlite3.wasm', cookie=cookie)
        require(status == 404 or (status == 200 and
                'text/html' in headers.get('Content-Type', '') and not body.startswith(b'\0asm')),
                'Web resources must not expose the legacy browser SQLite engine')
        status, meta, _ = self.api('GET', '/api/v1/meta', cookie=cookie)
        require(status == 200 and isinstance(meta.get('revision'), str),
                'API authority must return a canonical revision string')
        revision = meta['revision']
        store_id = meta.get('data', {}).get('storeId')
        require(isinstance(store_id, str) and bool(store_id), 'API store identity must be durable and nonempty')
        status, listed, _ = self.api('GET', '/api/v1/rules?scope=global', cookie=cookie)
        require(status == 200 and listed.get('data') == [], 'Disposable store must begin with no global rules')
        operation = 'container-rule-' + uuid.uuid4().hex
        rule = {'profileId': None, 'ruleAction': 'DOMAIN_SUFFIX',
                'content': uuid.uuid4().hex + '.invalid', 'ruleTarget': 'DIRECT',
                'noResolve': False, 'src': False}
        creation = {'expectedRevision': revision, 'operationId': operation, 'rule': rule}
        status, _, _ = self.api('POST', '/api/v1/rules:create', creation, cookie=cookie, include_origin=False)
        require(status == 403, 'Business mutations must require the browser origin boundary')
        invalid = copy.deepcopy(creation)
        invalid['rule']['sql'] = 'DELETE FROM profiles'
        status, _, _ = self.api('POST', '/api/v1/rules:create', invalid, cookie=cookie)
        require(status == 400, 'Business DTO validation must reject SQL/unknown fields before a transaction')
        status, blocked, _ = self.api('POST', '/api/v1/runtime/listener:set-enabled',
                                      {'expectedRevision': revision,
                                       'operationId': 'blocked-start-' + uuid.uuid4().hex,
                                       'enabled': True}, cookie=cookie)
        require(status == 409 and blocked.get('error', {}).get('code') == 'safe_mode',
                'Server safe mode must reject listener effects before queuing an intent')
        status, created, original_receipt = self.api('POST', '/api/v1/rules:create', creation, cookie=cookie)
        require(status == 200 and created.get('revision') != revision,
                'Creating a typed rule must commit exactly one new revision')
        canonical = created.get('data', {})
        identity = canonical.get('id')
        require(isinstance(identity, str) and identity.isascii() and identity.isdecimal() and
                not identity.startswith('0') and canonical.get('content') == rule['content'],
                'The server must allocate a canonical decimal-string rule identity')
        status, _, replay = self.api('POST', '/api/v1/rules:create', creation, cookie=cookie)
        require(status == 200 and replay == original_receipt, 'Repeating a successful operation must replay its receipt')
        second_cookie = self.authenticated_cookie()
        stale = copy.deepcopy(creation)
        stale['operationId'] = 'stale-rule-' + uuid.uuid4().hex
        stale['rule']['content'] = 'stale-' + uuid.uuid4().hex + '.invalid'
        status, conflict, _ = self.api('POST', '/api/v1/rules:create', stale, cookie=second_cookie)
        require(status == 409 and conflict.get('error', {}).get('code') == 'revision_conflict',
                'A second browser must not overwrite state using an obsolete revision')
        changed_rule = dict(rule, content='updated-' + uuid.uuid4().hex + '.invalid')
        status, changed, _ = self.api('POST', '/api/v1/rules:update',
                                      {'expectedRevision': created['revision'],
                                       'operationId': 'update-rule-' + uuid.uuid4().hex,
                                       'id': identity, 'rule': changed_rule}, cookie=cookie)
        require(status == 200 and changed.get('data', {}).get('content') == changed_rule['content'],
                'Updating a typed rule must return its canonical server value')
        final_revision = changed.get('revision')
        status, listed, _ = self.api('GET', '/api/v1/rules?scope=global', cookie=cookie)
        require(status == 200 and listed.get('revision') == final_revision and
                len(listed.get('data', [])) == 1 and listed['data'][0]['id'] == identity and
                listed['data'][0]['content'] == changed_rule['content'],
                'Rejected and replayed mutations must not create extra rows')
        database = self.directory / 'state/resources.db'
        require(database.is_file() and database.stat().st_mode & 0o777 == 0o600 and
                database.stat().st_uid == self.uid and
                (self.directory / 'state').stat().st_mode & 0o777 == 0o700,
                'The non-root API database must persist privately outside managed data')
        require(not (self.directory / 'data/database.sqlite').exists() and
                not (self.directory / 'data/shared_preferences.json').exists(),
                'Business API writes must not create a browser SQLite or preferences-file mirror')
        bootstrap_password = self.password
        cookie = self.change_credentials(cookie)
        old_container = self.container_id()
        new_container = self.start(recreate=True)
        require(old_container != new_container, 'Persistence test must actually recreate the container')
        require(self.request('GET', '/api/auth/session', cookie=cookie)[0] == 401,
                'Recreating the server must invalidate its old in-memory session')
        status, _, _ = self.request('POST', '/api/auth/login',
                                    body=json.dumps({'username': 'admin', 'password': bootstrap_password}),
                                    headers={'Content-Type': 'application/json'})
        require(status == 401, 'Bootstrap credentials must not override persisted account changes after restart')
        cookie = self.authenticated_cookie()
        status, meta, _ = self.api('GET', '/api/v1/meta', cookie=cookie)
        require(status == 200 and meta.get('data', {}).get('storeId') == store_id and
                meta.get('revision') == final_revision,
                'Store identity and committed revision must survive container force-recreation')
        status, listed, _ = self.api('GET', '/api/v1/rules?scope=global', cookie=cookie)
        require(status == 200 and len(listed.get('data', [])) == 1 and
                listed['data'][0]['id'] == identity and
                listed['data'][0]['content'] == changed_rule['content'],
                'Canonical business records must survive force-recreation')
        status, _, receipt = self.api('GET', '/api/v1/operations/' + operation, cookie=cookie)
        require(status == 200 and receipt == original_receipt,
                'Idempotency receipts must survive process replacement')
        status, _, replay = self.api('POST', '/api/v1/rules:create', creation, cookie=cookie)
        require(status == 200 and replay == original_receipt,
                'Receipt replay must precede CAS even after a later update and process restart')
        self.require_idle(cookie)
        require(self.request('POST', '/api/auth/logout', cookie=cookie)[0] == 204,
                'Authenticated logout must return HTTP 204')
        require(self.request('GET', '/api/v1/meta', cookie=cookie)[0] == 401,
                'Logout must revoke access to business APIs')
        print('PASS: typed APIs, strict DTOs, CAS/replay, private persistence, recreation, credentials, and logout')

    def change_credentials(self, cookie):
        status, _, body = self.request('GET', '/api/auth/session', cookie=cookie)
        require(status == 200 and json.loads(body).get('username') == self.username,
                'Authenticated session must expose the current account name')
        second_cookie = self.authenticated_cookie()
        new_username = 'container-admin'
        new_password = secrets.token_urlsafe(36)
        payload = {'currentPassword': 'wrong-' + secrets.token_urlsafe(36),
                   'username': new_username, 'newPassword': new_password}
        status, _, _ = self.request('POST', '/api/auth/credentials', json.dumps(payload), cookie=cookie,
                                    headers={'Content-Type': 'application/json'})
        require(status == 403 and self.request('GET', '/api/auth/session', cookie=cookie)[0] == 200,
                'Wrong current password must reject the change without invalidating the current session')
        payload['currentPassword'] = self.password
        status, _, body = self.request('POST', '/api/auth/credentials', json.dumps(payload), cookie=cookie,
                                       headers={'Content-Type': 'application/json'})
        require(status == 200 and json.loads(body).get('ok') is True,
                'Authenticated account update must persist successfully')
        for previous in (cookie, second_cookie):
            require(self.request('GET', '/api/auth/session', cookie=previous)[0] == 401,
                    'Account changes must revoke every existing session')
        credential_file = self.directory / 'auth/credentials.json'
        saved = credential_file.read_text()
        require(self.password not in saved and new_password not in saved,
                'Persistent account storage must never contain plaintext passwords')
        require(credential_file.stat().st_mode & 0o777 == 0o600,
                'Persistent account storage must remain owner-only')
        self.username, self.password = new_username, new_password
        current = self.authenticated_cookie()
        status, _, body = self.request('GET', '/api/auth/session', cookie=current)
        require(status == 200 and json.loads(body).get('username') == new_username,
                'Updated credentials must authenticate with the new account name')
        require(self.request('GET', '/api/fs/read?path=%2Fauth%2Fcredentials.json', cookie=current)[0] == 404,
                'Retired filesystem routes must not expose persistent account storage')
        return current

    def cleanup(self):
        if not self.started:
            return True
        result = self.compose(['down', '--remove-orphans', '--timeout', '5'],
                              file=self.runtime_file, allow_failure=True, timeout=45)
        return result.returncode == 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image', default='flclash-web:local', help='Already built local image; never pulled or built')
    parser.add_argument('--skip-image', action='store_true', help='Explicitly run Compose template checks only')
    parser.add_argument('--timeout', type=int, default=120, help='Maximum seconds per healthy-start phase')
    args = parser.parse_args()
    if args.timeout < 1:
        parser.error('--timeout must be positive')
    if not hasattr(os, 'getuid'):
        parser.error('This disposable bind-permission test requires a POSIX host')
    temporary = tempfile.TemporaryDirectory(prefix='flclash-container-smoke-')
    check = None
    failure = None
    try:
        check = ContainerCheck(Path(temporary.name), args.image, args.timeout)
        check.template()
        if args.skip_image:
            print('Image runtime checks explicitly not requested (--skip-image)')
        else:
            check.available_image()
            check.no_credentials()
            container = check.start()
            check.runtime_security(container)
            check.evaluator_licenses()
            check.authentication_and_persistence()
    except SmokeFailure as error:
        failure = str(error)
    except Exception as error:
        failure = f'Unexpected {type(error).__name__}; details withheld to protect credentials'
    finally:
        clean = False
        try:
            clean = check is None or check.cleanup()
        except Exception:
            pass
        if clean:
            temporary.cleanup()
        else:
            temporary._finalizer.detach()
            failure = ('Disposable project cleanup failed; retained only its private temporary files at ' +
                       temporary.name + (' (project ' + check.project + ')' if check else ''))
    if failure:
        print('FAIL: ' + failure, file=sys.stderr)
        return 1
    print('PASS: all requested container checks; only the disposable Compose project was cleaned up')
    return 0


if __name__ == '__main__':
    raise SystemExit(main())

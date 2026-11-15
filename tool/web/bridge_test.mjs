import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';

// WEBPANEL.md: cookie authentication, logout, filesystem bridge and cache policy.
const script = readFileSync(new URL('../../web/bridge.js', import.meta.url), 'utf8');

function browser(language = 'en-US') {
  const redirects = [], requests = [], xhrResponses = [], fetchResponses = [], sockets = [];
  const elements = new Map();
  const element = tag => ({ tag, style: {}, setAttribute(name, value) { this[name] = value; },
    click() {}, remove() { this.removed = true; } });
  for (const id of ['flclash-logout', 'flclash-auth-status', 'flclash-loader']) elements.set(id, element('div'));
  const body = { appendChild(node) { node.parentNode = body; }, removeChild(node) { node.parentNode = null; } };
  const context = {
    URL, Uint8Array, Blob, Promise, console, encodeURIComponent,
    setTimeout() {},
    btoa(value) { return Buffer.from(value, 'binary').toString('base64'); },
    atob(value) { return Buffer.from(value, 'base64').toString('binary'); },
    location: { href: 'https://example.test/', origin: 'https://example.test',
      replace(path) { redirects.push(path); }, reload() { context.reloaded = true; } },
    navigator: { language },
    document: { body, createElement: element, getElementById: id => elements.get(id) },
    addEventListener() {},
    async fetch(url, options) {
      requests.push({ url, options });
      const response = fetchResponses.shift();
      if (response instanceof Error) throw response;
      if (!response) throw new Error('Unexpected fetch: ' + url);
      return { status: response.status, text: async () => response.text || '' };
    },
    XMLHttpRequest: class {
      constructor() { this.headers = {}; }
      open(method, url, async) { Object.assign(this, { method, url, async }); }
      setRequestHeader(name, value) { this.headers[name] = value; }
      overrideMimeType(value) { this.mimeType = value; }
      send(body) {
        requests.push(this);
        this.body = body;
        const response = xhrResponses.shift();
        if (response instanceof Error) throw response;
        if (!response) throw new Error('Unexpected XHR: ' + this.url);
        this.status = response.status;
        this.responseText = response.text || '';
      }
    },
    WebSocket: class {
      constructor(url) { this.url = url; this.listeners = {}; sockets.push(this); }
      addEventListener(name, listener) { this.listeners[name] = listener; }
      emit(name) { this.listeners[name](); }
    },
  };
  context.window = context;
  vm.createContext(context);
  vm.runInContext(script, context);
  return { context, bridge: context.__flclashWeb, redirects, requests, xhrResponses, fetchResponses, sockets, elements };
}

for (const method of ['syncJsonRequest', 'syncBinaryWrite', 'syncReadBase64']) {
  test(`${method} reports a 401 and redirects rather than claiming success`, () => {
    const b = browser();
    b.xhrResponses.push({ status: 401 });
    const args = method === 'syncJsonRequest' ? ['GET', '/api/fs/snapshot', null]
      : method === 'syncBinaryWrite' ? ['/api/fs/write?path=%2Fdata%2Fx', 'AQI='] : ['/data/x'];
    const result = JSON.parse(b.bridge[method](...args));
    assert.equal(result.status, 401);
    assert.equal(result.ok, false);
    assert.deepEqual(b.redirects, ['/login']);
  });
}

test('async bridge sends same-origin cookie requests without caching and preserves 401', async () => {
  const b = browser();
  b.fetchResponses.push({ status: 401 });
  const result = JSON.parse(await b.bridge.asyncJsonRequest('POST', '/api/http-proxy', '{"url":"https://upstream.test"}'));
  assert.equal(result.status, 401);
  assert.equal(result.ok, false);
  assert.deepEqual(b.redirects, ['/login']);
  assert.equal(b.requests[0].options.credentials, 'same-origin');
  assert.equal(b.requests[0].options.cache, 'no-store');
  assert.equal(b.requests[0].options.redirect, 'error');
  assert.equal(b.requests[0].options.headers['Content-Type'], 'application/json');
});

test('async and sync network failures are explicit failures; foreign bridge URLs are refused', async () => {
  const b = browser();
  b.fetchResponses.push(new Error('offline'));
  assert.equal(JSON.parse(await b.bridge.asyncJsonRequest('GET', '/api/auth/session', null)).ok, false);
  b.xhrResponses.push(new Error('offline'));
  assert.equal(JSON.parse(b.bridge.syncJsonRequest('GET', '/api/fs/stat', null)).ok, false);
  assert.equal(JSON.parse(b.bridge.syncJsonRequest('GET', 'https://foreign.test/api', null)).ok, false);
  assert.equal(JSON.parse(await b.bridge.asyncJsonRequest('GET', 'https://foreign.test/api', null)).ok, false);
  assert.equal(b.requests.length, 2);
  assert.deepEqual(b.redirects, []);
});

test('VFS binary write preserves endpoint and bytes; unsuccessful writes remain failures', () => {
  const b = browser();
  b.xhrResponses.push({ status: 200, text: '{"ok":true,"size":3}' });
  const result = JSON.parse(b.bridge.syncBinaryWrite('/api/fs/write?path=%2Fdata%2Fx&mode=append', 'AP+A'));
  assert.equal(JSON.parse(result.text).ok, true);
  const request = b.requests[0];
  assert.equal(request.method, 'POST');
  assert.equal(request.async, false);
  assert.equal(request.headers['Content-Type'], 'application/octet-stream');
  assert.equal(request.headers['Cache-Control'], 'no-cache');
  assert.deepEqual(Array.from(request.body), [0, 255, 128]);
  b.xhrResponses.push({ status: 507, text: 'disk full' });
  assert.equal(JSON.parse(b.bridge.syncBinaryWrite('/api/fs/write?path=x', '')).status, 507);
});

test('VFS stat/read preserves bytes and distinguishes read failure from missing file', () => {
  const b = browser();
  b.xhrResponses.push({ status: 200, text: '{"exists":true,"isDir":false,"size":3}' },
    { status: 200, text: '\x00\xff\x80' });
  assert.equal(JSON.parse(b.bridge.syncReadBase64('/data/a b')).contentBase64, 'AP+A');
  assert.equal(b.requests[0].url, '/api/fs/stat?path=%2Fdata%2Fa%20b');
  assert.equal(b.requests[1].url, '/api/fs/read?path=%2Fdata%2Fa%20b');
  b.xhrResponses.push({ status: 200, text: '{"exists":true,"isDir":false}' }, { status: 401 });
  assert.equal(JSON.parse(b.bridge.syncReadBase64('/data/x')).status, 401);
  assert.deepEqual(b.redirects, ['/login']);
  b.xhrResponses.push({ status: 500, text: 'I/O error' });
  const failed = JSON.parse(b.bridge.syncReadBase64('/data/x'));
  assert.equal(failed.ok, false);
  assert.equal(failed.status, 500);
  assert.notEqual(failed.exists, false);
});

test('logout redirects only after its successful response is handled by the account UI', async () => {
  const b = browser();
  b.fetchResponses.push({ status: 500 });
  assert.equal(JSON.parse(await b.bridge.accountMutation('/api/auth/logout', null)).status, 500);
  assert.deepEqual(b.redirects, []);
  assert.throws(() => b.bridge.redirectAfterAuth(false), /successful account operation/);
  b.fetchResponses.push({ status: 204 });
  assert.equal(JSON.parse(await b.bridge.accountMutation('/api/auth/logout', null)).status, 204);
  assert.deepEqual(b.redirects, []);
  b.bridge.redirectAfterAuth(false);
  assert.equal(b.requests[1].url, '/api/auth/logout');
  assert.equal(b.requests[1].options.method, 'POST');
  assert.deepEqual(b.redirects, ['/login']);
});

for (const [status, error] of [[403, 'invalid_current_password'], [400, 'invalid_credentials'],
                              [500, 'credentials_save_failed'], [429, 'rate_limited']]) {
  test(`credentials failure ${status} preserves the session and permits a retry`, async () => {
    const b = browser();
    b.fetchResponses.push({ status, text: JSON.stringify({ error }) });
    const response = JSON.parse(await b.bridge.accountMutation('/api/auth/credentials', '{}'));
    assert.equal(response.status, status);
    assert.equal(JSON.parse(response.text).error, error);
    assert.deepEqual(b.redirects, []);
    b.fetchResponses.push({ status: 200, text: '{"ok":true}' });
    assert.equal(JSON.parse(await b.bridge.accountMutation('/api/auth/credentials', '{}')).status, 200);
    b.bridge.redirectAfterAuth(true);
    assert.deepEqual(b.redirects, ['/login?updated=1']);
  });
}

test('credential success wins races with revoked WS sessions and unrelated 401 responses', async () => {
  const b = browser();
  let complete;
  b.context.fetch = (url, options) => {
    b.requests.push({ url, options });
    return new Promise(resolve => { complete = resolve; });
  };
  const socket = new b.bridge.CoreWebSocket('wss://example.test/ws/core');
  const update = b.bridge.accountMutation('/api/auth/credentials', '{"username":"new-name"}');
  socket.emit('close');
  assert.equal(await b.bridge.checkSession(), false);
  assert.equal(b.requests.length, 1);
  b.xhrResponses.push({ status: 401 });
  assert.equal(JSON.parse(b.bridge.syncJsonRequest('GET', '/api/fs/stat', null)).status, 401);
  assert.deepEqual(b.redirects, []);
  assert.equal(JSON.parse(await b.bridge.accountMutation('/api/auth/logout', null)).status, 409);
  complete({ status: 200, text: async () => '{"ok":true}' });
  assert.equal(JSON.parse(await update).status, 200);
  socket.emit('error');
  b.xhrResponses.push({ status: 401 });
  b.bridge.syncJsonRequest('GET', '/api/fs/stat', null);
  assert.deepEqual(b.redirects, []);
  b.bridge.redirectAfterAuth(true);
  b.xhrResponses.push({ status: 401 });
  b.bridge.syncJsonRequest('GET', '/api/fs/stat', null);
  assert.deepEqual(b.redirects, ['/login?updated=1']);
  assert.equal(b.requests.filter(request => request.options).length, 1);
});

test('committed credentials with unconfirmed durability redirect to the localized warning, not ordinary success', async () => {
  const b = browser();
  b.fetchResponses.push({ status: 200, text: '{"ok":true,"warning":"durability_unconfirmed"}' });
  assert.equal(JSON.parse(await b.bridge.accountMutation('/api/auth/credentials', '{}')).status, 200);
  b.xhrResponses.push({ status: 401 });
  b.bridge.syncJsonRequest('GET', '/api/auth/session', null);
  assert.deepEqual(b.redirects, []);
  b.bridge.redirectAfterAuth(true);
  assert.deepEqual(b.redirects, ['/login?updated=1&warning=durability_unconfirmed']);
});

test('an unauthenticated credential mutation still redirects to login and reports failure', async () => {
  const b = browser();
  b.fetchResponses.push({ status: 401 });
  assert.equal(JSON.parse(await b.bridge.accountMutation('/api/auth/credentials', '{}')).status, 401);
  assert.deepEqual(b.redirects, ['/login']);
  assert.equal(JSON.parse(await b.bridge.accountMutation('https://foreign.test/api/auth/credentials', '{}')).ok, false);
});

test('HTML shell has no fixed logout control or account-status overlay', () => {
  const html = readFileSync(new URL('../../web/index.html', import.meta.url), 'utf8');
  assert.doesNotMatch(html, /flclash-logout|flclash-auth-status|mountShell/);
  assert.doesNotMatch(script, /mountShell|退出登录|Log out/);
});

for (const event of ['close', 'error']) {
  test(`WebSocket ${event} checks session, but does not log out a healthy session`, async () => {
    const b = browser();
    const socket = new b.bridge.CoreWebSocket('wss://example.test/ws/core');
    b.fetchResponses.push({ status: 200, text: '{"ok":true}' });
    socket.emit(event);
    assert.equal(await b.bridge.checkSession(), true);
    assert.equal(b.requests.length, 1);
    assert.equal(b.requests[0].url, '/api/auth/session');
    assert.deepEqual(b.redirects, []);
    b.fetchResponses.push({ status: 401 });
    socket.emit(event);
    assert.equal(await b.bridge.checkSession(), false);
    assert.deepEqual(b.redirects, ['/login']);
    assert.throws(() => new b.bridge.CoreWebSocket('wss://foreign.test/ws/core'), /same-origin/);
  });
}

test('boot unregisters old SW and removes only Flutter caches before checking authentication', async () => {
  const b = browser();
  let unregistered = 0;
  const removed = [];
  b.context.navigator.serviceWorker = { getRegistrations: async () => [
    { scope: 'https://example.test/', unregister: async () => { unregistered++; } },
    { scope: 'https://example.test/other/', unregister: async () => { throw new Error('wrong scope'); } },
  ] };
  b.context.caches = { delete: async name => { removed.push(name); } };
  b.fetchResponses.push({ status: 200, text: '{"ok":true}' });
  assert.equal(await b.bridge.prepareSession(), true);
  assert.equal(unregistered, 1);
  assert.deepEqual(removed.sort(), ['flutter-app-cache', 'flutter-app-manifest', 'flutter-temp-cache']);
  assert.equal(b.requests[0].url, '/api/auth/session');
});

test('boot reloads a formerly SW-controlled document without starting Flutter', async () => {
  const b = browser();
  b.context.navigator.serviceWorker = { controller: {}, getRegistrations: async () => [] };
  assert.equal(await b.bridge.prepareSession(), false);
  assert.equal(b.context.reloaded, true);
  assert.equal(b.requests.length, 0);
});

test('authenticated manifest link explicitly includes same-origin cookies', () => {
  const html = readFileSync(new URL('../../web/index.html', import.meta.url), 'utf8');
  assert.match(html, /<link\s+rel="manifest"[^>]*crossorigin="use-credentials"/);
});

test('custom Flutter bootstrap never asks the loader to register a service worker', () => {
  const bootstrap = readFileSync(new URL('../../web/flutter_bootstrap.js', import.meta.url), 'utf8');
  assert.match(bootstrap, /_flutter.loader.load\(\)/);
  assert.doesNotMatch(bootstrap, /serviceWorkerSettings/);
});

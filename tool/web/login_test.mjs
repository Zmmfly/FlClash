import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';

// WEBPANEL.md: the Go-owned login uses the same Web ARBs and never requires public app assets.
const html = readFileSync(new URL('../../core/webui/login.html', import.meta.url), 'utf8');
const locales = ['en', 'zh_CN', 'ja', 'ru', 'fr', 'es', 'ko'];
const messages = Object.fromEntries(locales.map(locale => [locale,
  JSON.parse(readFileSync(new URL(`../../core/webui/intl_${locale}.arb`, import.meta.url), 'utf8'))]));
const source = html.match(/<script>([\s\S]*?)<\/script>/)[1];

function loginPage(language = 'en-US', search = '') {
  const elements = new Map();
  const element = id => ({ id, value: '', hidden: true, type: id === 'password' ? 'password' : 'text',
    setAttribute(name, value) { this[name] = value; }, removeAttribute(name) { delete this[name]; },
    focus() { this.focused = true; }, reportValidity() { return true; } });
  for (const id of ['login', 'username', 'password', 'reveal', 'submit', 'error', 'status', 'title']) {
    elements.set(id, element(id));
  }
  const localized = Array.from(html.matchAll(/(?:id="([^"]+)"\s+)?data-message="(web\w+)"/g), ([, id, key]) => {
    const node = elements.get(id) || element(key);
    node.dataset = { message: key };
    return node;
  });
  const redirects = [], requests = [], responses = [];
  const document = { documentElement: {}, getElementById: id => elements.get(id), querySelectorAll: () => localized };
  const context = { document, navigator: { language }, URLSearchParams, console,
    location: { search, replace(path) { redirects.push(path); } },
    async fetch(url, options) {
      requests.push({ url, options });
      const response = responses.shift();
      if (response instanceof Error) throw response;
      if (!response) throw Error('Unexpected request');
      return { status: response.status, json: async () => response.body || {} };
    },
  };
  vm.createContext(context);
  vm.runInContext(source.replace('__FLCLASH_LOGIN_MESSAGES__', JSON.stringify(messages)), context);
  return { context, elements, localized, redirects, requests, responses,
    submit: () => elements.get('login').onsubmit({ preventDefault() {} }) };
}

test('all seven Web ARBs have identical typed keys; login has no public asset dependency or fake logo', () => {
  const keys = Object.keys(messages.en).sort();
  for (const locale of locales) {
    assert.deepEqual(Object.keys(messages[locale]).sort(), keys);
    assert.ok(Object.entries(messages[locale]).every(([key, value]) => key.startsWith('web') && typeof value === 'string' && value));
  }
  const view = readFileSync(new URL('overlay/lib/web_auth/account_view.dart.tmpl', import.meta.url), 'utf8');
  const client = readFileSync(new URL('overlay/lib/web_auth/auth_client.dart.tmpl', import.meta.url), 'utf8');
  const references = [...html.matchAll(/(?:text\.|data-message=")(web\w+)/g),
    ...view.matchAll(/\bl\.(web\w+)/g), ...client.matchAll(/\bl\.(web\w+)/g)];
  assert.ok(references.every(match => keys.includes(match[1])));
  assert.equal(html.split('__FLCLASH_LOGIN_MESSAGES__').length, 2);
  assert.doesNotMatch(html, /<img|<link|src=|linear-gradient|localStorage/);
  assert.match(html, /x1="59\.68" y1="100\.95" x2="180\.02" y2="31\.47"/);
  assert.match(html, /prefers-color-scheme: dark/);
  assert.match(html, /corner-shape: squircle/);
  assert.match(html, /--primary: #007aff/);
  assert.match(html, /--primary: #2f83ff/);
  assert.match(html, /label for="username"/);
  assert.match(html, /label for="password"/);
  assert.doesNotMatch(html, /maxlength="64"/);
  assert.match(html, /<form id="login" method="post" action="\/api\/auth\/login">/);
});

for (const [language, locale] of [['en-US', 'en'], ['zh-TW', 'zh_CN'], ['ja-JP', 'ja'], ['ru-RU', 'ru'],
                                 ['fr-FR', 'fr'], ['es-ES', 'es'], ['ko-KR', 'ko'], ['pt-BR', 'en']]) {
  test(`login ${language} uses one complete ${locale} translation`, () => {
    const page = loginPage(language);
    assert.equal(page.context.document.documentElement.lang, locale.replace('_', '-'));
    for (const node of page.localized) assert.equal(node.textContent, messages[locale][node.dataset.message]);
    assert.equal(page.elements.get('reveal')['aria-label'], messages[locale].webShowPassword);
  });
}

test('login password reveal uses accessible localized labels and native autofill conventions', () => {
  const page = loginPage('zh-CN');
  const reveal = page.elements.get('reveal');
  reveal.onclick();
  assert.equal(page.elements.get('password').type, 'text');
  assert.equal(reveal['aria-pressed'], 'true');
  assert.equal(reveal['aria-label'], messages.zh_CN.webHidePassword);
  reveal.onclick();
  assert.equal(page.elements.get('password').type, 'password');
  assert.match(html, /autocomplete="username"/);
  assert.match(html, /autocomplete="current-password"/);
});

test('login retains raw password semantics, sends cookies without caching, and redirects on verified success', async () => {
  const page = loginPage();
  page.elements.get('username').value = '  admin  ';
  page.elements.get('password').value = '  test password  ';
  page.responses.push({ status: 200, body: { ok: true } });
  await page.submit();
  const request = page.requests[0];
  assert.equal(request.url, '/api/auth/login');
  assert.deepEqual(JSON.parse(request.options.body), { username: 'admin', password: '  test password  ' });
  assert.equal(request.options.credentials, 'same-origin');
  assert.equal(request.options.cache, 'no-store');
  assert.equal(request.options.redirect, 'error');
  assert.deepEqual(page.redirects, ['/']);
  assert.equal(page.elements.get('password').value, '');
});

test('login blocks duplicate submit and always clears busy state after authentication failure', async () => {
  const page = loginPage('fr-FR');
  page.elements.get('username').value = 'admin';
  page.elements.get('password').value = 'test-password';
  let complete;
  page.context.fetch = (url, options) => {
    page.requests.push({ url, options });
    return new Promise(resolve => { complete = resolve; });
  };
  const first = page.submit();
  assert.equal(page.elements.get('submit').disabled, true);
  await page.submit();
  assert.equal(page.requests.length, 1);
  complete({ status: 403, json: async () => ({ error: 'invalid' }) });
  await first;
  assert.equal(page.elements.get('error').textContent, messages.fr.webLoginError);
  assert.equal(page.elements.get('error').hidden, false);
  assert.equal(page.elements.get('password')['aria-invalid'], 'true');
  assert.equal(page.elements.get('password').value, '');
  assert.equal(page.elements.get('submit').disabled, false);
  assert.equal(page.elements.get('password').readOnly, false);
  assert.deepEqual(page.redirects, []);
});

for (const length of [40, 64, 65]) {
  test(`login validates ${length} emoji by Unicode code points, not UTF-16 length`, async () => {
    const page = loginPage();
    page.elements.get('username').value = '😀'.repeat(length);
    page.elements.get('password').value = 'test-password';
    page.responses.push({ status: 200, body: { ok: true } });
    await page.submit();
    assert.equal(page.requests.length, length <= 64 ? 1 : 0);
    if (length <= 64) assert.equal(Array.from(JSON.parse(page.requests[0].options.body).username).length, length);
    else assert.equal(page.elements.get('error').textContent, messages.en.webInvalidUsername);
  });
}

test('login rejects raw CR/LF before trimming usernames', async () => {
  for (const username of ['\nadmin', 'admin\r', 'ad\nmin']) {
    const page = loginPage();
    page.elements.get('username').value = username;
    page.elements.get('password').value = 'test-password';
    await page.submit();
    assert.equal(page.requests.length, 0);
    assert.equal(page.elements.get('error').textContent, messages.en.webInvalidUsername);
  }
});

test('updated login distinguishes successful save from unconfirmed storage durability', () => {
  const normal = loginPage('en', '?updated=1');
  assert.equal(normal.elements.get('status').textContent, messages.en.webCredentialsUpdated);
  const warning = loginPage('zh-CN', '?updated=1&warning=durability_unconfirmed');
  assert.equal(warning.elements.get('status').textContent, messages.zh_CN.webDurabilityWarning);
  assert.equal(warning.elements.get('status').role, 'alert');
});

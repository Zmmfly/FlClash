import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {readFile, mkdtemp, rm} from 'node:fs/promises';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';

const compiled = process.argv[2];
if (!compiled) throw new Error('Usage: node api_redirect_fixture.mjs <compiled-browser-test.js> [chrome]');
const chrome = process.argv[3] || process.env.CHROME_EXECUTABLE || '/usr/bin/google-chrome';
const script = await readFile(compiled);
const state = {
  storeId: 'fixture-store-alpha', revision: 1, session: 1,
  receipts: new Map(), tombstones: new Set(), commits: new Map(),
  leakRequests: 0, refererRequests: 0, wrongStoreReceiptRequests: 0,
  slow: null, slowSeen: [], holdResolve: false, heldResolve: null, resolveSeen: [],
};
let originA;
let originB;
const scopeKey = (scope, operationId, store = state.storeId) => `${store}:${scope}:${operationId}`;
const envelope = (data, operationId, revision = state.revision) => ({
  revision: String(revision), data, ...(operationId ? {operationId} : {}),
});
const failure = (code, details) => ({
  revision: String(state.revision), error: {code, message: code, ...(details ? {details} : {})},
});
function reply(response, status, data, headers = {}) {
  const body = JSON.stringify(data);
  response.writeHead(status, {'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(body), ...headers});
  response.end(body);
}
function sessionOf(request) {
  return /(?:^|;\s*)fixture_session=([^;]+)/.exec(request.headers.cookie || '')?.[1] || '';
}
async function bodyOf(request) {
  const chunks = [];
  let length = 0;
  for await (const chunk of request) {
    length += chunk.length;
    if (length > 1024 * 1024) throw new Error('Fixture request exceeded its synthetic input limit');
    chunks.push(chunk);
  }
  return JSON.parse(Buffer.concat(chunks).toString('utf8') || '{}');
}
function receiptReply(request, response, scope, operationId, resolve) {
  const key = scopeKey(scope, operationId);
  const record = state.receipts.get(key);
  if (!record && [...state.receipts.keys()].some(value => value.endsWith(`:${scope}:${operationId}`))) {
    state.wrongStoreReceiptRequests++;
  }
  if (record && scope === 'source-operations' && record.session !== sessionOf(request)) {
    reply(response, 403, failure('source_session_mismatch', {state: 'origin_session_unrecoverable'}));
    return;
  }
  if (record) {
    reply(response, 200, record.receipt);
  } else if (resolve || state.tombstones.has(key)) {
    state.tombstones.add(key);
    reply(response, 409, failure('operation_not_committed', {state: 'not_committed'}));
  } else {
    reply(response, 404, failure('operation_not_found'));
  }
}

const second = http.createServer(async (request, response) => {
  state.leakRequests++;
  await bodyOf(request).catch(() => ({}));
  response.writeHead(204, {
    'Access-Control-Allow-Origin': originA,
    'Access-Control-Allow-Methods': 'POST, GET, OPTIONS',
    'Access-Control-Allow-Headers': 'content-type, accept',
  });
  response.end();
});
const first = http.createServer(async (request, response) => {
  try {
    const url = new URL(request.url, originA);
    if (url.pathname === '/') {
      response.writeHead(200, {'Content-Type': 'text/html', 'Cache-Control': 'no-store'});
      response.end('<!doctype html><body>running<script>globalThis.__authNotifications=0;globalThis.__flclashWeb={notifyUnauthorized(){globalThis.__authNotifications++;}};</script><script defer src="/client.js"></script></body>');
      return;
    }
    if (url.pathname === '/client.js') {
      response.writeHead(200, {'Content-Type': 'text/javascript', 'Cache-Control': 'no-store'});
      response.end(script);
      return;
    }
    if (!url.pathname.startsWith('/api/v1/')) {
      response.writeHead(404); response.end(); return;
    }
    if (request.headers.referer) state.refererRequests++;
    if (url.pathname === '/api/v1/meta') {
      reply(response, 200, envelope({storeId: state.storeId, schemaVersion: 1, revision: String(state.revision)}),
        {'Set-Cookie': `fixture_session=${state.session}; Path=/; HttpOnly; SameSite=Strict`});
      return;
    }
    if (url.pathname === '/api/v1/force-401') {
      reply(response, 401, failure('unauthenticated')); return;
    }
    if (url.pathname === '/api/v1/slow-read') {
      state.slow = response;
      for (const wake of state.slowSeen.splice(0)) wake();
      return;
    }
    if (url.pathname === '/api/v1/wait-for-slow') {
      if (!state.slow) await new Promise(resolve => state.slowSeen.push(resolve));
      reply(response, 200, envelope({started: true})); return;
    }
    const input = request.method === 'POST' ? await bodyOf(request) : {};
    if (url.pathname === '/api/v1/test:release-slow') {
      reply(state.slow, 200, envelope({oldEpochOnly: true})); state.slow = null;
      reply(response, 200, envelope({released: true})); return;
    }
    if (url.pathname === '/api/v1/test:switch-store') {
      state.storeId = 'fixture-store-beta'; state.revision = 0;
      reply(response, 200, envelope({switched: true})); return;
    }
    if (url.pathname === '/api/v1/test:login') {
      state.session++;
      reply(response, 200, envelope({reauthenticated: true}), {'Set-Cookie': `fixture_session=${state.session}; Path=/; HttpOnly; SameSite=Strict`});
      return;
    }
    if (url.pathname === '/api/v1/test:hold-resolve') {
      state.holdResolve = true;
      reply(response, 200, envelope({armed: true})); return;
    }
    if (url.pathname === '/api/v1/test:wait-resolve') {
      if (!state.heldResolve) await new Promise(resolve => state.resolveSeen.push(resolve));
      reply(response, 200, envelope({started: true})); return;
    }
    if (url.pathname === '/api/v1/test:release-resolve') {
      const held = state.heldResolve; state.heldResolve = null;
      receiptReply(held.request, held.response, held.scope, held.operationId, true);
      reply(response, 200, envelope({released: true})); return;
    }
    const receiptRoute = /^\/api\/v1\/(operations|cache-operations|source-operations)\/([^/]+?)(:resolve)?$/.exec(url.pathname);
    if (receiptRoute) {
      const [, scope, encoded, resolveSuffix] = receiptRoute;
      const operationId = decodeURIComponent(encoded);
      if (resolveSuffix) assert.deepEqual(input, {intent: 'resolve-unknown-result'});
      if (resolveSuffix && state.holdResolve) {
        state.holdResolve = false;
        state.heldResolve = {request, response, scope, operationId};
        for (const wake of state.resolveSeen.splice(0)) wake();
      } else {
        receiptReply(request, response, scope, operationId, Boolean(resolveSuffix));
      }
      return;
    }
    if (url.pathname === '/api/v1/test-stats') {
      reply(response, 200, envelope({
        leakRequests: state.leakRequests, refererRequests: state.refererRequests,
        wrongStoreReceiptRequests: state.wrongStoreReceiptRequests,
        committedWrites: [...state.commits.values()].reduce((a, b) => a + b, 0),
        duplicateCommits: [...state.commits.values()].filter(value => value !== 1).length,
      }));
      return;
    }
    assert.equal(request.method, 'POST');
    assert.equal(typeof input.operationId, 'string');
    assert.equal(input.expectedRevision, String(state.revision));
    const label = input.profile?.label || input.rule?.content || input.preparationId;
    if (label === 'redirect307' || label === 'redirect308') {
      response.writeHead(label === 'redirect307' ? 307 : 308, {'Location': `${originB}/must-never-receive-body`});
      response.end(); return;
    }
    const scope = url.pathname === '/api/v1/sources:authorize' ? 'source-operations' : 'operations';
    const key = scopeKey(scope, input.operationId);
    if (state.tombstones.has(key)) {
      reply(response, 409, failure('operation_not_committed', {state: 'not_committed'})); return;
    }
    if (label === 'never-dispatched') { request.socket.destroy(); return; }
    const existing = state.receipts.get(key);
    if (!existing) {
      if (scope !== 'source-operations') {
        state.revision++;
        state.commits.set(input.operationId, (state.commits.get(input.operationId) || 0) + 1);
      }
      const data = scope === 'source-operations'
        ? {authorizationId: 'synthetic-authorization', expiresAt: '2099-01-01T00:00:00Z'}
        : {id: '9007199254740993', label: 'committed'};
      state.receipts.set(key, {receipt: envelope(data, input.operationId), session: sessionOf(request)});
    }
    if (label?.startsWith('commit-loss') || label === 'lost-source') {
      request.socket.destroy(); return;
    }
    reply(response, 200, state.receipts.get(key).receipt);
  } catch (error) {
    reply(response, 500, {error: {code: 'fixture_failure', message: error.message}});
  }
});
async function listen(server) {
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  return `http://127.0.0.1:${server.address().port}`;
}
const directory = await mkdtemp(path.join(os.tmpdir(), 'flclash-api-browser-'));
let child;
try {
  originB = await listen(second);
  originA = await listen(first);
  child = spawn(chrome, ['--headless', '--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage',
    `--user-data-dir=${directory}`, '--dump-dom', '--virtual-time-budget=20000', `${originA}/`],
    {stdio: ['ignore', 'pipe', 'pipe'], detached: process.platform !== 'win32'});
  let stdout = ''; let stderr = '';
  child.stdout.on('data', chunk => { stdout += chunk.toString(); });
  child.stderr.on('data', chunk => { if (stderr.length < 65536) stderr += chunk.toString(); });
  const timer = setTimeout(() => {
    if (child.exitCode === null) {
      if (process.platform === 'win32') child.kill('SIGKILL');
      else process.kill(-child.pid, 'SIGKILL');
    }
  }, 120000);
  const code = await new Promise((resolve, reject) => { child.once('error', reject); child.once('exit', resolve); });
  clearTimeout(timer);
  assert.equal(code, 0, `Chrome failed: ${stderr}`);
  const raw = /<body[^>]*>([\s\S]*?)<\/body>/.exec(stdout)?.[1]
    .replaceAll('&lt;', '<').replaceAll('&gt;', '>').replaceAll('&quot;', '"').replaceAll('&#39;', "'").replaceAll('&amp;', '&');
  assert.ok(raw, `No browser result: ${stdout}`);
  let result;
  try { result = JSON.parse(raw); } catch { throw new Error(`Browser did not finish: ${raw}\n${stderr}`); }
  assert.equal(result.ok, true, JSON.stringify(result));
  assert.equal(state.leakRequests, 0, 'No second-origin request is allowed');
  console.log(JSON.stringify({ok: true, checks: result.checks, secondOriginRequests: state.leakRequests}));
} finally {
  if (child?.exitCode === null) {
    if (process.platform === 'win32') child.kill('SIGKILL');
    else process.kill(-child.pid, 'SIGKILL');
  }
  await Promise.all([first, second].map(server => new Promise(resolve => server.close(resolve))));
  await rm(directory, {recursive: true, force: true});
}

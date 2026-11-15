import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import vm from 'node:vm';

if (process.argv.length !== 3) {
  throw new Error('Usage: node tool/web/vfs_runtime_test.mjs <compiled vfs_test.js>');
}
const compiled = process.argv[2];
const stagedRoot = dirname(compiled);
const bridgePath = join(stagedRoot, 'web', 'bridge.js');
if (!existsSync(bridgePath)) {
  throw new Error(`Missing staged browser bridge: ${bridgePath}`);
}

const typedInterfaceResource = '/api/v1/runtime/network-interfaces';
const redirects = [];
const requests = [];
const downloads = [];
let xhrCount = 0;

globalThis.self = globalThis;
globalThis.window = globalThis;
if (!globalThis.navigator) {
  Object.defineProperty(globalThis, 'navigator', { value: {}, configurable: true });
}
globalThis.location = {
  href: 'https://panel.test/',
  origin: 'https://panel.test',
  protocol: 'https:',
  host: 'panel.test',
  replace(path) {
    redirects.push(path);
  },
};
globalThis.addEventListener = () => {};
globalThis.document = {
  currentScript: null,
  body: { appendChild() {}, removeChild() {} },
  getElementById: () => null,
  createElement: () => ({ style: {}, click() {} }),
};
if (typeof globalThis.Blob === 'undefined') globalThis.Blob = class Blob {};
globalThis.URL.createObjectURL = () => 'blob:flclash-test';
globalThis.URL.revokeObjectURL = () => {};
globalThis.fetch = async (url) => {
  const target = String(url);
  requests.push(target);
  if (target !== typedInterfaceResource) {
    return { status: 404, async text() { return '{}'; } };
  }
  return {
    status: 200,
    async text() {
      return JSON.stringify({ revision: '1', data: [{ name: 'eth0', addresses: ['10.0.0.2'] }] });
    },
  };
};
globalThis.XMLHttpRequest = class {
  constructor() {
    xhrCount += 1;
    throw new Error('Synchronous XHR must not be used by the Web panel');
  }
};

vm.runInThisContext(readFileSync(bridgePath, 'utf8'), { filename: 'bridge.js' });
const bridge = globalThis.__flclashWeb;
assert.ok(bridge, 'bridge.js must install window.__flclashWeb');
const download = bridge.triggerDownload;
bridge.triggerDownload = (name, base64) => {
  downloads.push({ name, bytes: Array.from(bridge.base64ToBytes(base64)) });
  return download(name, base64);
};

let settle;
const reported = new Promise((resolve) => {
  settle = resolve;
});
globalThis.__vfsTestReport = (message) => settle(String(message));
vm.runInThisContext(readFileSync(compiled, 'utf8'), { filename: 'vfs_test.js' });

let timer;
const outcome = await Promise.race([
  reported,
  new Promise((_, reject) => {
    timer = setTimeout(() => reject(new Error('Compiled VFS contract timed out')), 240000);
  }),
]);
clearTimeout(timer);
assert.ok(outcome.startsWith('pass'), outcome);

assert.equal(xhrCount, 0, 'The Web filesystem must not use XMLHttpRequest');
assert.deepEqual(requests, [typedInterfaceResource], 'Only the typed network-interface resource may be requested');
for (const url of requests) {
  assert.ok(url.startsWith('/api/v1/'), `Unexpected endpoint: ${url}`);
  assert.ok(!/\/api\/(fs|http-proxy)(\/|$)|\/ws\/core(\/|$)/.test(url), `Legacy transport endpoint: ${url}`);
}
assert.equal(downloads.length, 1, 'Only a /downloads write may trigger a browser download');
assert.equal(downloads[0].name, 'report.bin');
assert.deepEqual(downloads[0].bytes, [7, 8, 9]);
assert.deepEqual(redirects, [], 'No unauthorized navigation is expected');
console.log('Compiled Dart ephemeral VFS, budgets, persist negatives and typed-resource contracts passed');

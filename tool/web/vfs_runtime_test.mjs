import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';

if (process.argv.length !== 3) throw new Error('Usage: node tool/web/vfs_runtime_test.mjs <compiled vfs_test.js>');
const redirects = [];
let unauthorizedOperations = 0;
globalThis.self = globalThis;
globalThis.window = globalThis;
globalThis.location = { href: 'https://example.test/', origin: 'https://example.test', replace(path) { redirects.push(path); } };
globalThis.addEventListener = () => {};
globalThis.__webTestMode = 'success';
globalThis.__webTestRequests = 0;
globalThis.XMLHttpRequest = class {
  open(method, url) { this.method = method; this.url = url; }
  setRequestHeader() {}
  send() {
    globalThis.__webTestRequests++;
    this.status = 200;
    if (this.url.startsWith('/api/fs/snapshot')) {
      if (__webTestMode === 'snapshot-failure') {
        this.status = 500;
        this.responseText = 'unavailable';
      } else {
        this.responseText = JSON.stringify({ entries: [
          { path: '/data/existing', isDir: false, size: 1, contentBase64: 'AQ==' },
          { path: '/data/remote', isDir: false, size: 10 },
        ] });
      }
    } else if (this.url.startsWith('/api/fs/stat')) {
      if (__webTestMode === 'stat-failure') {
        this.status = 500;
        this.responseText = 'I/O error';
      } else {
        this.responseText = '{"exists":false}';
      }
    } else if (__webTestMode === 'unauthorized') {
      unauthorizedOperations++;
      this.status = 401;
      this.responseText = 'unauthorized';
    } else if (__webTestMode === 'failure') {
      this.status = 507;
      this.responseText = 'disk full';
    } else if (__webTestMode === 'malformed') {
      this.responseText = 'not JSON';
    } else {
      this.responseText = '{"ok":true}';
    }
  }
};
vm.runInThisContext(readFileSync(new URL('../../web/bridge.js', import.meta.url), 'utf8'), { filename: 'bridge.js' });
vm.runInThisContext(readFileSync(process.argv[2], 'utf8'), { filename: 'vfs_test.js' });
assert.ok(unauthorizedOperations >= 6, 'Every unauthorized filesystem mutation must be exercised');
assert.equal(redirects.length, 1, 'Unauthorized operations coalesce into a single immediate login navigation');
assert.ok(redirects.every(path => path === '/login'));
console.log('Compiled Dart VFS mutation/cache/401 contracts passed');

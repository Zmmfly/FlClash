'use strict';
(() => {
  let authMutation = false;
  let pendingAuthRedirect = null;
  let redirectedTo = null;
  let unauthorizedDuringMutation = false;
  function navigateTo(path) {
    if (redirectedTo !== null) return;
    redirectedTo = path;
    window.location.replace(path);
  }
  function sameOrigin(url) {
    if (new URL(url, window.location.href).origin !== window.location.origin) {
      throw new Error('Bridge requests must remain same-origin');
    }
  }
  function unauthorized() {
    if (authMutation || pendingAuthRedirect) {
      unauthorizedDuringMutation = true;
    } else {
      navigateTo('/login');
    }
    return JSON.stringify({ ok: false, status: 401, error: 'Authentication required' });
  }
  function failure(error) {
    return JSON.stringify({ ok: false, status: 500, error: String(error) });
  }
  function syncRequest(method, url, body, contentType) {
    try {
      sameOrigin(url);
      const xhr = new XMLHttpRequest();
      xhr.open(method, url, false);
      xhr.setRequestHeader('Cache-Control', 'no-cache');
      if (body !== undefined && body !== null) xhr.setRequestHeader('Content-Type', contentType);
      xhr.send(body === undefined ? null : body);
      if (xhr.status === 401) return unauthorized();
      return JSON.stringify({ status: xhr.status, text: xhr.responseText || '' });
    } catch (error) {
      return failure(error);
    }
  }
    window.__flclashWeb = {
      bytesToBase64: function(bytes) {
        let binary = '';
        const len = bytes.byteLength;
        const chunkSize = 0x8000;
        for (let i = 0; i < len; i += chunkSize) {
          binary += String.fromCharCode.apply(null, bytes.subarray(i, Math.min(i + chunkSize, len)));
        }
        return window.btoa(binary);
      },
      base64ToBytes: function(b64) {
        const binary = window.atob(b64);
        const len = binary.length;
        const bytes = new Uint8Array(len);
        for (let i = 0; i < len; i++) {
          bytes[i] = binary.charCodeAt(i);
        }
        return bytes;
      },
      asyncJsonRequest: async function(method, url, jsonBody) {
        try {
          const options = { method: method || 'GET', credentials: 'same-origin', cache: 'no-store', redirect: 'error' };
          sameOrigin(url);
          if (jsonBody !== undefined && jsonBody !== null) {
            options.headers = { 'Content-Type': 'application/json' };
            options.body = jsonBody;
          }
          const response = await fetch(url, options);
          if (response.status === 401) return unauthorized();
          return JSON.stringify({ status: response.status, text: await response.text() });
        } catch (error) {
          return failure(error);
        }
      },
      syncJsonRequest: function(method, url, jsonBody) {
        return syncRequest(method || 'GET', url, jsonBody, 'application/json');
      },
      syncBinaryWrite: function(url, base64Body) {
        return syncRequest('POST', url, this.base64ToBytes(base64Body || ''), 'application/octet-stream');
      },
      syncReadBase64: function(vpath) {
        try {
          const stat = JSON.parse(this.syncJsonRequest('GET', '/api/fs/stat?path=' + encodeURIComponent(vpath), null));
          if (stat.status < 200 || stat.status >= 300) return JSON.stringify({ ok: false, ...stat });
          const info = JSON.parse(stat.text);
          if (!info.exists || info.isDir) return JSON.stringify(info);
          const xhr = new XMLHttpRequest();
          xhr.open('GET', '/api/fs/read?path=' + encodeURIComponent(vpath), false);
          xhr.setRequestHeader('Cache-Control', 'no-cache');
          xhr.overrideMimeType('text/plain; charset=x-user-defined');
          xhr.send(null);
          if (xhr.status === 401) return unauthorized();
          if (xhr.status !== 200) return JSON.stringify({ ok: false, status: xhr.status, error: xhr.responseText });
          const bytes = Uint8Array.from(xhr.responseText, character => character.charCodeAt(0) & 0xff);
          info.contentBase64 = this.bytesToBase64(bytes);
          return JSON.stringify(info);
        } catch (error) {
          return failure(error);
        }
      },
      triggerDownload: function(filename, base64Data) {
        try {
          const bytes = window.__flclashWeb.base64ToBytes(base64Data);
          const blob = new Blob([bytes], { type: 'application/octet-stream' });
          const url = URL.createObjectURL(blob);
          const a = document.createElement('a');
          a.href = url;
          a.download = filename || 'download';
          document.body.appendChild(a);
          a.click();
          document.body.removeChild(a);
          setTimeout(() => URL.revokeObjectURL(url), 5000);
          return true;
        } catch (e) {
          console.error('[FlClash-web] triggerDownload error:', e);
          return false;
        }
      },
      pickFile: function() {
        return new Promise((resolve) => {
          const input = document.createElement('input');
          input.type = 'file';
          input.style.display = 'none';
          let settled = false;
          const finish = (val) => {
            if (settled) return;
            settled = true;
            if (input.parentNode) input.parentNode.removeChild(input);
            resolve(JSON.stringify(val));
          };
          input.onchange = async () => {
            if (!input.files || input.files.length === 0) {
              finish({ cancelled: true });
              return;
            }
            const file = input.files[0];
            const buf = await file.arrayBuffer();
            const b64 = window.__flclashWeb.bytesToBase64(new Uint8Array(buf));
            finish({
              cancelled: false,
              name: file.name,
              size: file.size,
              base64: b64
            });
          };
          input.oncancel = () => finish({ cancelled: true });
          document.body.appendChild(input);
          input.click();
        });
      },
      evalClashScript: function(scriptSource, configJsonStr, profileName) {
        try {
          const configObj = JSON.parse(configJsonStr);
          const fn = new Function('config', 'profileName', scriptSource + '\n; return main(config, profileName);');
          const res = fn(configObj, profileName || '');
          return JSON.stringify({ ok: true, data: JSON.stringify(res) });
        } catch (e) {
          return JSON.stringify({ ok: false, error: String(e) });
        }
      }
    };


  const bridge = window.__flclashWeb;
  let sessionCheck;
  bridge.checkSession = function() {
    if (authMutation || pendingAuthRedirect) return Promise.resolve(false);
    if (!sessionCheck) {
      sessionCheck = bridge.asyncJsonRequest('GET', '/api/auth/session', null)
        .then(raw => {
          const response = JSON.parse(raw);
          if (response.status !== 200) return false;
          return JSON.parse(response.text).ok === true;
        }).catch(() => false).finally(() => { sessionCheck = null; });
    }
    return sessionCheck;
  };
  bridge.CoreWebSocket = function(url) {
    const expected = new URL('/ws/core', window.location.href);
    expected.protocol = expected.protocol === 'https:' ? 'wss:' : 'ws:';
    if (url !== expected.href) throw new Error('Core WebSocket must remain same-origin');
    const socket = new WebSocket(url);
    socket.addEventListener('error', () => { void bridge.checkSession(); });
    socket.addEventListener('close', () => { void bridge.checkSession(); });
    return socket;
  };
  bridge.accountMutation = async function(url, jsonBody) {
    if (url !== '/api/auth/logout' && url !== '/api/auth/credentials') {
      return failure(new Error('Unsupported account operation'));
    }
    if (authMutation || pendingAuthRedirect) {
      return JSON.stringify({ ok: false, status: 409, error: 'operation_in_progress' });
    }
    authMutation = true;
    unauthorizedDuringMutation = false;
    try {
      const raw = await bridge.asyncJsonRequest('POST', url, jsonBody);
      const response = JSON.parse(raw);
      const success = url === '/api/auth/logout' ? response.status === 204 :
        response.status === 200 && JSON.parse(response.text || '{}').ok === true;
      if (success) {
        const warning = response.text && JSON.parse(response.text).warning === 'durability_unconfirmed';
        pendingAuthRedirect = url === '/api/auth/credentials'
          ? '/login?updated=1' + (warning ? '&warning=durability_unconfirmed' : '') : '/login';
      } else if (response.status === 401 || unauthorizedDuringMutation) {
        navigateTo('/login');
      }
      return raw;
    } catch (error) {
      if (unauthorizedDuringMutation) navigateTo('/login');
      return failure(error);
    } finally {
      authMutation = false;
    }
  };
  bridge.redirectAfterAuth = function(updated) {
    const expected = updated ? '/login?updated=1' : '/login';
    if (pendingAuthRedirect !== expected && !(updated &&
        pendingAuthRedirect === '/login?updated=1&warning=durability_unconfirmed')) {
      throw new Error('No matching successful account operation');
    }
    navigateTo(pendingAuthRedirect);
  };
  bridge.prepareSession = async function() {
    if (navigator.serviceWorker) {
      const registrations = await navigator.serviceWorker.getRegistrations();
      await Promise.all(registrations.filter(registration => window.location.href.startsWith(registration.scope))
        .map(registration => registration.unregister()));
    }
    if (window.caches) {
      await Promise.all(['flutter-app-cache', 'flutter-temp-cache', 'flutter-app-manifest']
        .map(name => window.caches.delete(name)));
    }
    if (navigator.serviceWorker && navigator.serviceWorker.controller) {
      window.location.reload();
      return false;
    }
    return bridge.checkSession();
  };
  window.addEventListener('flutter-first-frame', () => {
    const loader = document.getElementById('flclash-loader');
    if (loader) loader.remove();
  });
})();

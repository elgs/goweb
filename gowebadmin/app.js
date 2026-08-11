'use strict';

// ============================================
// goweb admin — application logic
// Plain JS, no build step, no dependencies.
//
// The UI is organized in three levels, routed
// by location.hash so the browser back/forward
// buttons and deep links work:
//   #/            server list (overview)
//   #/s/0         one server + its host list
//   #/s/0/h/2     one host
// ============================================

// ---- state ---------------------------------

let servers = [];        // working copy of the config
let route = { view: 'servers' };
let unapplied = false;   // differs from the running config
let unsaved = false;     // differs from the config file on disk
let busy = false;
let pendingDeleteSi = -1;
let focusNameOnRender = false;

const byId = id => document.getElementById(id);
const viewEl = () => byId('view');

// ---- helpers -------------------------------

function esc(v) {
  return String(v ?? '').replace(/[&<>"']/g, c => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  })[c]);
}

function token() {
  return sessionStorage.getItem('access_token') || localStorage.getItem('access_token') || '';
}

function toast(type, message) {
  let c = document.querySelector('.ui-toast-container');
  if (!c) {
    c = document.createElement('div');
    c.className = 'ui-toast-container bottom-right';
    c.setAttribute('aria-live', 'polite');
    document.body.appendChild(c);
  }
  const icons = {
    info: 'ui-icon-info', success: 'ui-icon-check-circle',
    danger: 'ui-icon-alert-circle', warning: 'ui-icon-alert-triangle',
  };
  const t = document.createElement('div');
  t.className = `ui-toast ${type}`;
  t.innerHTML = `<i class="ui-toast-icon ui-icon ${icons[type]}"></i>`
    + `<span class="ui-toast-body">${esc(message)}</span>`
    + `<button class="ui-toast-close" aria-label="Dismiss">&times;</button>`;
  t.querySelector('.ui-toast-close').onclick = () => t.remove();
  c.appendChild(t);
  setTimeout(() => t.parentNode && t.remove(), type === 'danger' ? 8000 : 4000);
}

// ---- api -----------------------------------

async function api(method, url, body) {
  let res;
  try {
    res = await fetch(url, {
      method,
      headers: {
        'authorization': token(),
        ...(body !== undefined ? { 'content-type': 'application/json' } : {}),
      },
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
  } catch {
    throw Object.assign(new Error('Cannot reach the goweb admin API.'), { status: 0 });
  }
  if (res.status === 401) {
    sessionStorage.removeItem('access_token');
    localStorage.removeItem('access_token');
    showLogin();
    throw Object.assign(new Error('Invalid access token.'), { status: 401 });
  }
  const data = await res.json().catch(() => ({}));
  if (!res.ok || data.err) {
    throw Object.assign(new Error(data.err || `HTTP ${res.status}`), { status: res.status });
  }
  return data;
}

// ---- config model --------------------------

function normalize(s) {
  const n = { ...s };
  n.hosts = Array.isArray(s.hosts) ? s.hosts.map(h => ({ ...h })) : [];
  n._origName = s.name || '';
  return n;
}

function cleanHost(server, host) {
  const h = { name: host.name || '' };
  if (server.type === 'tcp') {
    h.upstream = host.upstream || '';
  } else {
    h.type = host.type || 'serve_static';
    if (h.type === 'serve_static') {
      h.path = host.path || '';
      if (host.disable_dir_listing) h.disable_dir_listing = true;
    } else if (h.type === '301_redirect') {
      h.redirect_url = host.redirect_url || '';
    } else if (h.type === 'reverse_proxy') {
      h.forward_urls = host.forward_urls || '';
    }
    if (server.type === 'https') {
      h.cert_path = host.cert_path || '';
      h.key_path = host.key_path || '';
    }
    if (host.allowed_origins) h.allowed_origins = host.allowed_origins;
  }
  if (host.disabled) h.disabled = true;
  return h;
}

function cleanServer(s) {
  const out = { name: s.name || '', type: s.type || 'http', listen: s.listen || '' };
  if (s.disabled) out.disabled = true;
  if (s.access_log) out.access_log = true;
  out.hosts = (s.hosts || []).map(h => cleanHost(s, h));
  return out;
}

const serializeAll = () => servers.map(cleanServer);

function newHost(serverType) {
  return serverType === 'tcp'
    ? { name: '', upstream: '' }
    : { name: '', type: 'serve_static', path: '' };
}

// ---- routing -------------------------------

const serverHash = si => `#/s/${si}`;
const hostHash = (si, hi) => `#/s/${si}/h/${hi}`;

function parseHash() {
  const h = location.hash;
  if (!h || h === '#' || h === '#/') return { view: 'servers' };
  let m = h.match(/^#\/s\/(\d+)$/);
  if (m && servers[+m[1]]) return { view: 'server', si: +m[1] };
  m = h.match(/^#\/s\/(\d+)\/h\/(\d+)$/);
  if (m && servers[+m[1]] && servers[+m[1]].hosts[+m[2]]) {
    return { view: 'host', si: +m[1], hi: +m[2] };
  }
  return null;
}

function goTo(hash, replace) {
  if ((location.hash || '#/') === hash) {
    syncRoute();
    return;
  }
  if (replace) location.replace(hash);
  else location.hash = hash;
}

// Accept a token passed in the URL hash (#access_token=...): store it and
// remove it from the URL. Returns true when one was found.
function adoptTokenFromHash() {
  const m = location.hash.match(/access_token=([^&]+)/);
  if (!m) return false;
  sessionStorage.setItem('access_token', decodeURIComponent(m[1]));
  history.replaceState(null, '', location.pathname + location.search);
  return true;
}

// Re-parse the hash against the current data and render. Falls back to the
// nearest valid ancestor when the hash points at something that no longer
// exists (deleted entry, replaced config, stale deep link).
function syncRoute() {
  if (adoptTokenFromHash() && byId('app').hidden) {
    // a token URL was pasted into a tab showing the login screen — try it
    loadServers().then(showApp).catch(err => {
      showLogin();
      if (err.status !== 401) toast('danger', err.message);
    });
    return;
  }
  const r = parseHash();
  if (!r) {
    const m = location.hash.match(/^#\/s\/(\d+)/);
    location.replace(m && servers[+m[1]] ? serverHash(+m[1]) : '#/');
    return;
  }
  const moved = r.view !== route.view || r.si !== route.si || r.hi !== route.hi;
  route = r;
  renderView(moved);
}

// ---- rendering: building blocks ------------

const field = (label, inner, hint) =>
  `<div class="ui-field"><label>${label}</label>${inner}`
  + `${hint ? `<span class="ui-hint">${hint}</span>` : ''}</div>`;

const textInput = (f, v, ph) =>
  `<input class="ui-input" type="text" data-f="${f}" value="${esc(v)}" placeholder="${esc(ph || '')}">`;

const toggle = (f, checked, label, title) =>
  `<label class="ui-toggle" ${title ? `title="${esc(title)}"` : ''}>`
  + `<input type="checkbox" data-f="${f}" ${checked ? 'checked' : ''}>`
  + `<span class="ui-toggle-track"></span>${label || ''}</label>`;

function options(list, current) {
  let opts = list.map(([v, l]) =>
    `<option value="${esc(v)}" ${v === current ? 'selected' : ''}>${esc(l)}</option>`).join('');
  if (current && !list.some(([v]) => v === current)) {
    opts += `<option value="${esc(current)}" selected>${esc(current)}</option>`;
  }
  return opts;
}

function openURL(s, h) {
  const m = (s.listen || '').match(/:(\d+)\s*$/);
  const port = m ? m[1] : '';
  const std = (s.type === 'http' && port === '80') || (s.type === 'https' && port === '443');
  return `${s.type}://${h.name}${port && !std ? ':' + port : ''}`;
}

function hostTypeMeta(s, h) {
  if (s.type === 'tcp') return { label: 'tcp upstream', icon: 'ui-icon-plug', badge: 'secondary' };
  return {
    serve_static: { label: 'static files', icon: 'ui-icon-folder', badge: '' },
    '301_redirect': { label: 'redirect', icon: 'ui-icon-corner-up-right', badge: 'warning' },
    reverse_proxy: { label: 'reverse proxy', icon: 'ui-icon-shuffle', badge: 'secondary' },
  }[h.type] || { label: h.type || 'unknown', icon: 'ui-icon-globe', badge: 'danger' };
}

function hostSummary(s, h) {
  if (s.type === 'tcp') return h.upstream || 'no upstream set';
  if (h.type === '301_redirect') return h.redirect_url || 'no redirect URL set';
  if (h.type === 'reverse_proxy') return h.forward_urls || 'no forward URLs set';
  return h.path || 'no web root set';
}

const nameOr = (name, fallback) =>
  esc(name) || `<span class="ui-text-muted">${fallback}</span>`;

// items: [{label, href}] — the last item is the current page; labels are
// pre-escaped by the callers. actions: HTML for the right side of the bar.
function crumbBar(items, actions) {
  const lis = items.map((it, i) =>
    i === items.length - 1
      ? `<li aria-current="page"><span class="crumb-here" id="crumb-here">${it.label}</span></li>`
      : `<li><a href="${it.href}">${it.label}</a></li>`
  ).join('');
  return `
  <div class="crumb-bar">
    <nav aria-label="Breadcrumb"><ul class="ui-breadcrumb">${lis}</ul></nav>
    <div class="crumb-actions">${actions || ''}</div>
  </div>`;
}

// ---- rendering: level 1 — server list ------

function serverDot(s) {
  if (s.disabled) return 'ui-dot';
  if (s.status || (s.hosts || []).some(h => !h.disabled && h.status)) return 'ui-dot danger pulse';
  return 'ui-dot success';
}

function serverRow(s, si) {
  const typeBadge = { https: 'success', http: '', tcp: 'secondary' }[s.type] ?? 'danger';
  const n = (s.hosts || []).length;
  const nErr = ((s.hosts || []).filter(h => !h.disabled && h.status).length) + (s.status ? 1 : 0);
  return `
  <div class="row ${s.disabled ? 'off' : ''}" data-si="${si}" data-nav="${serverHash(si)}" role="link" tabindex="0">
    <span class="${serverDot(s)}"></span>
    <div class="row-main">
      <div class="row-title">
        <span class="r-name">${nameOr(s.name, 'unnamed server')}</span>
        <span class="ui-badge sm outline ${typeBadge}">${esc(s.type || '?')}</span>
      </div>
      <div class="row-sub">
        <code class="listen">${esc(s.listen) || '—'}</code>
        <span>${n} host${n === 1 ? '' : 's'}</span>
        ${nErr ? `<span class="err-note">${nErr} error${nErr === 1 ? '' : 's'}</span>` : ''}
      </div>
    </div>
    ${toggle('enabled', !s.disabled, '', 'Enabled')}
    <button class="ui-btn ghost icon sm" data-action="del-server" title="Delete server">
      <i class="ui-icon ui-icon-trash"></i>
    </button>
    <i class="ui-icon ui-icon-chevron-right row-go"></i>
  </div>`;
}

function viewServers() {
  if (servers.length === 0) {
    return `
    <div class="empty">
      <i class="ui-icon ui-icon-server"></i>
      <h2>No servers configured</h2>
      <p>Add your first server to start serving websites.</p>
      <button class="ui-btn" data-action="add-server"><i class="ui-icon ui-icon-plus"></i>Add server</button>
    </div>`;
  }
  return `
  <div class="page">
    <div class="page-head">
      <h2>Servers</h2>
      <span class="ui-badge secondary outline">${servers.length}</span>
      <span class="spacer"></span>
      <button class="ui-btn sm" data-action="add-server">
        <i class="ui-icon ui-icon-plus"></i><span class="btn-label">Add server</span>
      </button>
    </div>
    <div class="ui-card elevated rows-card">
      <div class="rows">${servers.map(serverRow).join('')}</div>
    </div>
  </div>`;
}

// ---- rendering: level 2 — one server -------

function hostRow(s, h, si, hi) {
  const meta = hostTypeMeta(s, h);
  const openable = s.type !== 'tcp' && h.name;
  const dot = h.disabled ? 'ui-dot' : (h.status ? 'ui-dot danger pulse' : 'ui-dot success');
  return `
  <div class="row ${h.disabled ? 'off' : ''}" data-hi="${hi}" data-nav="${hostHash(si, hi)}" role="link" tabindex="0">
    <span class="${dot}"></span>
    <i class="ui-icon ${meta.icon} r-icon"></i>
    <div class="row-main">
      <div class="row-title">
        <span class="r-name">${nameOr(h.name, 'unnamed host')}</span>
        <span class="ui-badge sm outline ${meta.badge}">${esc(meta.label)}</span>
      </div>
      <div class="row-sub"><span class="r-target">${esc(hostSummary(s, h))}</span></div>
    </div>
    ${openable ? `<a class="ui-btn ghost icon sm" href="${esc(openURL(s, h))}" target="_blank"
      rel="noopener" title="Open in browser"><i class="ui-icon ui-icon-external-link"></i></a>` : ''}
    ${toggle('enabled', !h.disabled, '', 'Enabled')}
    <button class="ui-btn ghost icon sm" data-action="del-host" title="Delete host">
      <i class="ui-icon ui-icon-trash"></i>
    </button>
    <i class="ui-icon ui-icon-chevron-right row-go"></i>
  </div>`;
}

function viewServer(si) {
  const s = servers[si];
  const renamed = !!s._origName && s.name !== s._origName;
  const n = (s.hosts || []).length;
  const rows = (s.hosts || []).map((h, hi) => hostRow(s, h, si, hi)).join('')
    || '<p class="rows-empty ui-text-muted">No hosts yet — add one to serve something.</p>';
  return `
  <div class="page" data-si="${si}">
    ${crumbBar(
      [{ label: 'Servers', href: '#/' }, { label: nameOr(s.name, 'unnamed server') }],
      `<button class="ui-btn ghost sm ui-tooltip bottom" data-action="apply-one" ${renamed ? 'disabled' : ''}
        data-tooltip="${renamed ? 'Renamed servers need a full Apply' : 'Apply only this server to the running config'}">
        <i class="ui-icon ui-icon-zap"></i><span class="btn-label">Apply server</span>
      </button>
      <button class="ui-btn ghost icon sm" data-action="del-server" title="Delete server">
        <i class="ui-icon ui-icon-trash"></i>
      </button>`
    )}
    <section class="ui-card elevated pane">
      <h3 class="pane-title">Server settings</h3>
      <div class="grid">
        ${field('Server name', textInput('name', s.name, 'my-server'))}
        ${field('Server type', `<select class="ui-select" data-f="type">${options([
          ['http', 'http'], ['https', 'https'], ['tcp', 'tcp'],
        ], s.type)}</select>`)}
        ${field('Listen on', textInput('listen', s.listen, '[::]:443'), 'host:port; use [::] for all interfaces.')}
        <div class="field-toggles">
          ${toggle('enabled', !s.disabled, 'Enabled')}
          ${toggle('access_log', !!s.access_log, 'Access log',
            'Write one record per request or connection to stdout')}
        </div>
      </div>
      ${s.status ? `<div class="ui-alert danger">${esc(s.status)}</div>` : ''}
    </section>
    <section class="ui-card elevated rows-card">
      <div class="pane-head">
        <h3 class="pane-title">Hosts</h3>
        <span class="ui-badge secondary outline">${n}</span>
        <span class="spacer"></span>
        <button class="ui-btn ghost sm" data-action="add-host">
          <i class="ui-icon ui-icon-plus"></i><span class="btn-label">Add host</span>
        </button>
      </div>
      <div class="rows">${rows}</div>
    </section>
  </div>`;
}

// ---- rendering: level 3 — one host ---------

function viewHost(si, hi) {
  const s = servers[si];
  const h = s.hosts[hi];
  const isWeb = s.type !== 'tcp';
  const openable = isWeb && h.name;

  let fields = field('Host name', textInput('name', h.name, isWeb ? 'example.com' : 'upstream-1'),
    isWeb ? 'Domain name matched against the request Host header.' : 'A label for this upstream.');
  let toggles = toggle('enabled', !h.disabled, 'Enabled');
  if (isWeb) {
    fields += field('Host type', `<select class="ui-select" data-f="type">${options([
      ['serve_static', 'Static files'],
      ['301_redirect', '301 redirect'],
      ['reverse_proxy', 'Reverse proxy'],
    ], h.type)}</select>`);
    if (h.type === '301_redirect') {
      fields += field('Redirect URL', textInput('redirect_url', h.redirect_url, 'https://example.com'),
        'The request path and query are appended.');
    } else if (h.type === 'reverse_proxy') {
      fields += field('Forward URLs', textInput('forward_urls', h.forward_urls, 'http://10.0.0.1:8080 http://10.0.0.2:8080'),
        'Space separated upstreams; clients stick to one by IP hash.');
    } else {
      fields += field('Web root path', textInput('path', h.path, '/path/to/webroot'));
    }
    if (s.type === 'https') {
      fields += field('Certificate path', textInput('cert_path', h.cert_path, '/path/to/cert.pem'));
      fields += field('Private key path', textInput('key_path', h.key_path, '/path/to/key.pem'));
    }
    fields += field('Allowed origins', textInput('allowed_origins', h.allowed_origins, '*'),
      'Access-Control-Allow-Origin header; empty to omit.');
    if (h.type === 'serve_static' || !h.type) {
      toggles += toggle('dirlist', !h.disable_dir_listing, 'Directory listing',
        'Show a directory listing when no index.html is present');
    }
  } else {
    fields += field('Upstream', textInput('upstream', h.upstream, '10.0.0.1:5432'),
      'TCP address (host:port) to forward connections to.');
  }
  fields += `<div class="field-toggles">${toggles}</div>`;

  return `
  <div class="page" data-si="${si}">
    ${crumbBar(
      [
        { label: 'Servers', href: '#/' },
        { label: nameOr(s.name, 'unnamed server'), href: serverHash(si) },
        { label: nameOr(h.name, 'unnamed host') },
      ],
      `${openable ? `<a class="ui-btn ghost sm" href="${esc(openURL(s, h))}" target="_blank" rel="noopener">
        <i class="ui-icon ui-icon-external-link"></i><span class="btn-label">Open</span></a>` : ''}
      <button class="ui-btn ghost icon sm" data-action="del-host" title="Delete host">
        <i class="ui-icon ui-icon-trash"></i>
      </button>`
    )}
    <section class="ui-card elevated pane" data-hi="${hi}">
      <h3 class="pane-title">Host settings</h3>
      <div class="grid">${fields}</div>
      ${h.status ? `<div class="ui-alert danger">${esc(h.status)}</div>` : ''}
    </section>
  </div>`;
}

// ---- rendering: dispatch -------------------

function renderView(scrollTop) {
  const el = viewEl();
  if (route.view === 'server') el.innerHTML = viewServer(route.si);
  else if (route.view === 'host') el.innerHTML = viewHost(route.si, route.hi);
  else el.innerHTML = viewServers();
  if (scrollTop) window.scrollTo(0, 0);
  if (focusNameOnRender) {
    focusNameOnRender = false;
    const inp = el.querySelector('input[data-f="name"]');
    if (inp) inp.focus();
  }
}

function updateBadges() {
  byId('badge-unapplied').hidden = !unapplied;
  byId('badge-unsaved').hidden = !unsaved;
  document.title = (unapplied || unsaved ? '• ' : '') + 'goweb admin';
}

function markDirty() {
  unapplied = unsaved = true;
  updateBadges();
}

// ---- views ---------------------------------

function showLogin() {
  byId('app').hidden = true;
  byId('login').hidden = false;
  setTimeout(() => byId('login-token').focus());
}

function showApp() {
  byId('login').hidden = true;
  byId('login-error').hidden = true;
  byId('app').hidden = false;
}

// ---- dialogs -------------------------------

const openDialog = id => byId(id).classList.add('open');
const closeDialog = id => byId(id).classList.remove('open');
const closeAllDialogs = () =>
  document.querySelectorAll('.ui-dialog-backdrop.open').forEach(b => b.classList.remove('open'));

// ---- actions -------------------------------

async function withBusy(btn, fn) {
  if (busy) return;
  busy = true;
  if (btn) {
    btn.classList.add('loading');
    btn.setAttribute('disabled', '');
  }
  try {
    await fn();
  } catch (err) {
    if (err.status !== 401) toast('danger', err.message || String(err));
  } finally {
    busy = false;
    if (btn) {
      btn.classList.remove('loading');
      btn.removeAttribute('disabled');
    }
  }
}

async function loadServers() {
  const data = await api('GET', '/api/servers/');
  servers = (data || []).map(normalize);
  unapplied = unsaved = false;
  syncRoute();
  updateBadges();
}

function applyAll(btn) {
  return withBusy(btn, async () => {
    await api('PATCH', '/api/servers/', serializeAll());
    const keepUnsaved = unsaved;
    await loadServers();
    unsaved = keepUnsaved;
    updateBadges();
    toast('success', 'Configuration applied to the running server.');
  });
}

function saveAll(btn) {
  return withBusy(btn, async () => {
    await api('POST', '/api/servers/', serializeAll());
    unsaved = false;
    updateBadges();
    toast('success', 'Configuration saved to disk.');
  });
}

function applyOne(btn, si) {
  const s = servers[si];
  return withBusy(btn, async () => {
    await api('POST', '/api/server/', cleanServer(s));
    s._origName = s.name;
    s.status = '';
    s.hosts.forEach(h => { h.status = ''; });
    renderView();
    toast('success', `Server “${s.name || 'unnamed'}” applied.`);
  });
}

function addServer() {
  servers.push(normalize({ name: '', type: 'http', listen: '[::]:80', hosts: [newHost('http')] }));
  markDirty();
  focusNameOnRender = true;
  goTo(serverHash(servers.length - 1));
}

function addHost(s, si) {
  s.hosts.push(newHost(s.type));
  markDirty();
  focusNameOnRender = true;
  goTo(hostHash(si, s.hosts.length - 1));
}

function login(btn) {
  const input = byId('login-token');
  const value = input.value.trim();
  if (!value) {
    input.focus();
    return;
  }
  return withBusy(btn, async () => {
    sessionStorage.setItem('access_token', value);
    try {
      await loadServers();
    } catch (err) {
      if (err.status === 401) { // reported inline; other errors toast via withBusy
        byId('login-error').hidden = false;
        input.focus();
        input.select();
      }
      throw err;
    }
    input.value = '';
    byId('login-error').hidden = true;
    showApp();
  });
}

function logout() {
  sessionStorage.removeItem('access_token');
  localStorage.removeItem('access_token');
  servers = [];
  unapplied = unsaved = false;
  updateBadges();
  showLogin();
}

function openJSON() {
  byId('json-text').value = JSON.stringify(serializeAll(), null, 2);
  byId('json-error').hidden = true;
  openDialog('dlg-json');
}

function useJSON() {
  const errEl = byId('json-error');
  try {
    const parsed = JSON.parse(byId('json-text').value);
    if (!Array.isArray(parsed) || !parsed.every(x => x && typeof x === 'object' && !Array.isArray(x))) {
      throw new Error('Config must be a JSON array of server objects.');
    }
    servers = parsed.map(normalize);
    closeDialog('dlg-json');
    markDirty();
    goTo('#/', true);
    toast('info', 'Config loaded into the editor — review, then Apply or Save.');
  } catch (err) {
    errEl.textContent = err.message;
    errEl.hidden = false;
  }
}

async function copyJSON(btn) {
  const text = byId('json-text');
  try {
    await navigator.clipboard.writeText(text.value);
  } catch {
    text.select();
    document.execCommand('copy');
  }
  btn.querySelector('.ui-icon').className = 'ui-icon ui-icon-check-circle';
  setTimeout(() => {
    btn.querySelector('.ui-icon').className = 'ui-icon ui-icon-copy';
  }, 1200);
}

function toggleTheme() {
  const next = document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark';
  document.documentElement.dataset.theme = next;
  localStorage.setItem('goweb-admin-theme', next);
  applyThemeIcon();
}

function applyThemeIcon() {
  const dark = document.documentElement.dataset.theme === 'dark';
  byId('btn-theme').querySelector('.ui-icon').className = `ui-icon ${dark ? 'ui-icon-sun' : 'ui-icon-moon'}`;
}

// ---- events --------------------------------

function setField(s, h, f, el) {
  const v = el.type === 'checkbox' ? el.checked : el.value;
  if (!h) {
    if (f === 'enabled') s.disabled = !v;
    else s[f] = v;
  } else {
    if (f === 'enabled') h.disabled = !v;
    else if (f === 'dirlist') h.disable_dir_listing = !v;
    else h[f] = v;
  }
}

// Find the server/host an element belongs to. List rows carry data-si /
// data-hi; on detail pages the page root carries data-si and the host pane
// data-hi, with the route filling in whatever the DOM doesn't provide
// (e.g. crumb-bar actions on the host page).
function resolveTarget(el) {
  const card = el.closest('[data-si]');
  const si = card ? +card.dataset.si : (route.view !== 'servers' ? route.si : -1);
  const s = si >= 0 ? servers[si] : null;
  const hostEl = el.closest('[data-hi]');
  const hi = hostEl ? +hostEl.dataset.hi : (route.view === 'host' ? route.hi : -1);
  const h = s && hi >= 0 ? s.hosts[hi] : null;
  return { s, h, si, hi };
}

function onInput(e) {
  const el = e.target;
  if (!el.dataset.f || el.matches('select, input[type="checkbox"]')) return;
  const { s, h } = resolveTarget(el);
  if (!s) return;
  setField(s, h, el.dataset.f, el);
  markDirty();
  // live updates without a re-render (keeps typing focus)
  if (el.dataset.f === 'name') {
    const here = byId('crumb-here');
    if (here) here.innerHTML = h ? nameOr(h.name, 'unnamed host') : nameOr(s.name, 'unnamed server');
    if (h) {
      const a = viewEl().querySelector('.crumb-actions a');
      if (a) a.href = openURL(s, h);
    } else {
      const applyBtn = viewEl().querySelector('[data-action="apply-one"]');
      if (applyBtn) {
        const renamed = !!s._origName && s.name !== s._origName;
        applyBtn.toggleAttribute('disabled', renamed);
        applyBtn.dataset.tooltip = renamed
          ? 'Renamed servers need a full Apply'
          : 'Apply only this server to the running config';
      }
    }
  }
}

function onChange(e) {
  const el = e.target;
  if (!el.dataset.f || !el.matches('select, input[type="checkbox"]')) return;
  const { s, h } = resolveTarget(el);
  if (!s) return;
  setField(s, h, el.dataset.f, el);
  markDirty();
  renderView();
}

function onClick(e) {
  if (e.target.classList.contains('ui-dialog-backdrop')) {
    e.target.classList.remove('open');
    return;
  }
  const btn = e.target.closest('[data-action]');
  if (!btn) {
    // whole-row navigation, except on interactive children (toggles, links)
    const row = e.target.closest('[data-nav]');
    if (row && !e.target.closest('a, button, label, input, select')) {
      goTo(row.dataset.nav);
    }
    return;
  }
  const { s, si, hi } = resolveTarget(btn);

  switch (btn.dataset.action) {
    case 'login': login(byId('login-btn')); break;
    case 'logout': logout(); break;
    case 'theme': toggleTheme(); break;
    case 'add-server': addServer(); break;
    case 'apply': applyAll(byId('btn-apply')); break;
    case 'save': saveAll(byId('btn-save')); break;
    case 'json': openJSON(); break;
    case 'json-use': useJSON(); break;
    case 'json-copy': copyJSON(btn); break;
    case 'dlg-close': btn.closest('.ui-dialog-backdrop').classList.remove('open'); break;
    case 'apply-one': applyOne(btn, si); break;
    case 'add-host': addHost(s, si); break;
    case 'del-host':
      s.hosts.splice(hi, 1);
      markDirty();
      if (route.view === 'host') goTo(serverHash(si), true);
      else renderView();
      break;
    case 'del-server': {
      pendingDeleteSi = si;
      const n = (s.hosts || []).length;
      byId('confirm-text').textContent =
        `Delete server “${s.name || 'unnamed'}” with ${n} host${n === 1 ? '' : 's'}? `
        + 'This only changes the editor until you apply or save.';
      openDialog('dlg-confirm');
      break;
    }
    case 'confirm-del':
      if (pendingDeleteSi >= 0) {
        servers.splice(pendingDeleteSi, 1);
        pendingDeleteSi = -1;
        markDirty();
        if (route.view === 'servers') renderView();
        else goTo('#/', true);
      }
      closeDialog('dlg-confirm');
      break;
  }
}

function onKey(e) {
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 's' && !byId('app').hidden) {
    e.preventDefault();
    saveAll(byId('btn-save'));
    return;
  }
  if ((e.key === 'Enter' || e.key === ' ') && e.target.matches?.('[data-nav]')) {
    e.preventDefault();
    goTo(e.target.dataset.nav);
    return;
  }
  if (e.key === 'Escape') {
    if (document.querySelector('.ui-dialog-backdrop.open')) {
      closeAllDialogs();
    } else if (route.view === 'host') {
      goTo(serverHash(route.si));
    } else if (route.view === 'server') {
      goTo('#/');
    }
  }
}

// ---- init ----------------------------------

function init() {
  document.addEventListener('click', onClick);
  document.addEventListener('keydown', onKey);
  viewEl().addEventListener('input', onInput);
  viewEl().addEventListener('change', onChange);
  window.addEventListener('hashchange', syncRoute);
  byId('login-token').addEventListener('keydown', e => {
    if (e.key === 'Enter') login(byId('login-btn'));
  });
  window.addEventListener('beforeunload', e => {
    if (unapplied || unsaved) {
      e.preventDefault();
      e.returnValue = '';
    }
  });
  applyThemeIcon();

  adoptTokenFromHash();

  if (token()) {
    loadServers().then(showApp).catch(err => {
      showLogin();
      if (err.status !== 401) toast('danger', err.message);
    });
  } else {
    showLogin();
  }
}

init();

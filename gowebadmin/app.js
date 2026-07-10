'use strict';

// ============================================
// goweb admin — application logic
// Plain JS, no build step, no dependencies.
// ============================================

// ---- state ---------------------------------

let servers = [];        // working copy of the config
let unapplied = false;   // differs from the running config
let unsaved = false;     // differs from the config file on disk
let busy = false;
let pendingDeleteSi = -1;

const byId = id => document.getElementById(id);
const serversEl = () => byId('servers');

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
  n._collapsed = false;
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
  out.hosts = (s.hosts || []).map(h => cleanHost(s, h));
  return out;
}

const serializeAll = () => servers.map(cleanServer);

function newHost(serverType) {
  return serverType === 'tcp'
    ? { name: '', upstream: '' }
    : { name: '', type: 'serve_static', path: '' };
}

// ---- rendering -----------------------------

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

function hostCard(s, h, si, hi) {
  const meta = hostTypeMeta(s, h);
  const isWeb = s.type !== 'tcp';
  const openable = isWeb && h.name;

  let fields = field('Host name', textInput('name', h.name, isWeb ? 'example.com' : 'upstream-1'),
    isWeb ? 'Domain name matched against the request Host header.' : 'A label for this upstream.');
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
      fields += `<div class="field-toggles">${toggle('dirlist', !h.disable_dir_listing, 'Directory listing',
        'Show a directory listing when no index.html is present')}</div>`;
    }
  } else {
    fields += field('Upstream', textInput('upstream', h.upstream, '10.0.0.1:5432'),
      'TCP address (host:port) to forward connections to.');
  }

  return `
  <div class="host ${h.disabled ? 'off' : ''}" data-hi="${hi}">
    <div class="host-head">
      <i class="ui-icon ${meta.icon} h-icon"></i>
      <span class="h-name">${esc(h.name) || '<span class="ui-text-muted">unnamed host</span>'}</span>
      <span class="ui-badge sm outline ${meta.badge}">${esc(meta.label)}</span>
      <div class="head-controls">
        ${openable ? `<a class="ui-btn ghost icon sm" href="${esc(openURL(s, h))}" target="_blank"
          rel="noopener" title="Open in browser"><i class="ui-icon ui-icon-external-link"></i></a>` : ''}
        ${toggle('enabled', !h.disabled, '', 'Enabled')}
        <button class="ui-btn ghost icon sm" data-action="del-host" title="Delete host">
          <i class="ui-icon ui-icon-trash"></i>
        </button>
      </div>
    </div>
    <div class="host-fields grid">${fields}</div>
    ${h.status ? `<div class="ui-alert danger">${esc(h.status)}</div>` : ''}
  </div>`;
}

function serverCard(s, si) {
  const dot = s.disabled ? 'ui-dot' : (s.status ? 'ui-dot danger pulse' : 'ui-dot success');
  const typeBadge = { https: 'success', http: '', tcp: 'secondary' }[s.type] ?? 'danger';
  const renamed = s._origName && s.name !== s._origName;

  return `
  <section class="ui-card elevated server ${s.disabled ? 'off' : ''} ${s._collapsed ? 'collapsed' : ''}" data-si="${si}">
    <div class="server-head">
      <div class="head-main" data-action="collapse">
        <i class="ui-icon ui-icon-chevron-down caret"></i>
        <span class="${dot}"></span>
        <span class="s-name">${esc(s.name) || '<span class="ui-text-muted">unnamed server</span>'}</span>
        <span class="ui-badge outline ${typeBadge}">${esc(s.type || '?')}</span>
        <code class="listen s-listen">${esc(s.listen) || '—'}</code>
      </div>
      <div class="head-controls">
        ${toggle('enabled', !s.disabled, '', 'Enabled')}
        <button class="ui-btn ghost icon sm ui-tooltip bottom" data-action="apply-one" ${renamed ? 'disabled' : ''}
          data-tooltip="${renamed ? 'Renamed servers need a full Apply' : 'Apply only this server'}">
          <i class="ui-icon ui-icon-zap"></i>
        </button>
        <button class="ui-btn ghost icon sm" data-action="del-server" title="Delete server">
          <i class="ui-icon ui-icon-trash"></i>
        </button>
      </div>
    </div>
    <div class="server-body" ${s._collapsed ? 'hidden' : ''}>
      <div class="grid">
        ${field('Server name', textInput('name', s.name, 'my-server'))}
        ${field('Server type', `<select class="ui-select" data-f="type">${options([
          ['http', 'http'], ['https', 'https'], ['tcp', 'tcp'],
        ], s.type)}</select>`)}
        ${field('Listen on', textInput('listen', s.listen, '[::]:443'), 'host:port; use [::] for all interfaces.')}
      </div>
      ${s.status ? `<div class="ui-alert danger">${esc(s.status)}</div>` : ''}
      <div class="hosts">
        <div class="hosts-head">
          <h3>Hosts</h3>
          <button class="ui-btn ghost sm" data-action="add-host"><i class="ui-icon ui-icon-plus"></i>Add host</button>
        </div>
        ${(s.hosts || []).map((h, hi) => hostCard(s, h, si, hi)).join('')}
        ${(s.hosts || []).length === 0 ? '<p class="ui-text-muted">No hosts yet — add one to serve something.</p>' : ''}
      </div>
    </div>
  </section>`;
}

function renderServers() {
  if (servers.length === 0) {
    serversEl().innerHTML = `
    <div class="empty">
      <i class="ui-icon ui-icon-server"></i>
      <h2>No servers configured</h2>
      <p>Add your first server to start serving websites.</p>
      <button class="ui-btn" data-action="add-server"><i class="ui-icon ui-icon-plus"></i>Add server</button>
    </div>`;
    return;
  }
  serversEl().innerHTML = servers.map(serverCard).join('');
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
  renderServers();
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
    renderServers();
    toast('success', `Server “${s.name || 'unnamed'}” applied.`);
  });
}

function addServer() {
  servers.push(normalize({ name: '', type: 'http', listen: '[::]:80', hosts: [newHost('http')] }));
  markDirty();
  renderServers();
  const card = serversEl().querySelector(`[data-si="${servers.length - 1}"]`);
  card.scrollIntoView({ behavior: 'smooth', block: 'start' });
  card.querySelector('input[data-f="name"]').focus();
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
    renderServers();
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

function resolveTarget(el) {
  const card = el.closest('[data-si]');
  if (!card) return {};
  const s = servers[+card.dataset.si];
  const hostEl = el.closest('[data-hi]');
  const h = hostEl && s ? s.hosts[+hostEl.dataset.hi] : null;
  return { card, hostEl, s, h, si: +card.dataset.si, hi: hostEl ? +hostEl.dataset.hi : -1 };
}

function onInput(e) {
  const el = e.target;
  if (!el.dataset.f || el.matches('select, input[type="checkbox"]')) return;
  const { card, hostEl, s, h } = resolveTarget(el);
  if (!s) return;
  setField(s, h, el.dataset.f, el);
  markDirty();
  // live header updates without a re-render (keeps typing focus)
  if (!h && el.dataset.f === 'name') {
    card.querySelector('.s-name').innerHTML = esc(s.name) || '<span class="ui-text-muted">unnamed server</span>';
    const applyBtn = card.querySelector('[data-action="apply-one"]');
    const renamed = !!s._origName && s.name !== s._origName;
    applyBtn.toggleAttribute('disabled', renamed);
    applyBtn.dataset.tooltip = renamed ? 'Renamed servers need a full Apply' : 'Apply only this server';
  } else if (!h && el.dataset.f === 'listen') {
    card.querySelector('.s-listen').textContent = s.listen || '—';
  } else if (h && el.dataset.f === 'name') {
    hostEl.querySelector('.h-name').innerHTML = esc(h.name) || '<span class="ui-text-muted">unnamed host</span>';
    const a = hostEl.querySelector('.head-controls a');
    if (a) a.href = openURL(s, h);
  }
}

function onChange(e) {
  const el = e.target;
  if (!el.dataset.f || !el.matches('select, input[type="checkbox"]')) return;
  const { s, h } = resolveTarget(el);
  if (!s) return;
  setField(s, h, el.dataset.f, el);
  markDirty();
  renderServers();
}

function onClick(e) {
  if (e.target.classList.contains('ui-dialog-backdrop')) {
    e.target.classList.remove('open');
    return;
  }
  const btn = e.target.closest('[data-action]');
  if (!btn) return;
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
    case 'collapse':
      s._collapsed = !s._collapsed;
      btn.closest('.server').classList.toggle('collapsed', s._collapsed);
      btn.closest('.server').querySelector('.server-body').hidden = s._collapsed;
      break;
    case 'apply-one': applyOne(btn, si); break;
    case 'add-host':
      s.hosts.push(newHost(s.type));
      markDirty();
      renderServers();
      serversEl().querySelector(`[data-si="${si}"] [data-hi="${s.hosts.length - 1}"] input[data-f="name"]`).focus();
      break;
    case 'del-host':
      s.hosts.splice(hi, 1);
      markDirty();
      renderServers();
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
        renderServers();
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
  if (e.key === 'Escape') closeAllDialogs();
}

// ---- init ----------------------------------

function init() {
  document.addEventListener('click', onClick);
  document.addEventListener('keydown', onKey);
  serversEl().addEventListener('input', onInput);
  serversEl().addEventListener('change', onChange);
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

  // accept a token passed in the URL hash, then remove it from the URL
  const m = location.hash.match(/access_token=([^&]+)/);
  if (m) {
    sessionStorage.setItem('access_token', decodeURIComponent(m[1]));
    history.replaceState(null, '', location.pathname + location.search);
  }

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

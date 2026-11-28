import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';

const indexHTML = await readFile(new URL('./static/index.html', import.meta.url), 'utf8');
const loginHTML = await readFile(new URL('./static/login.html', import.meta.url), 'utf8');
const iconsSVG = await readFile(new URL('./static/icons.svg', import.meta.url), 'utf8');
const appSource = await readFile(new URL('./static/app.js', import.meta.url), 'utf8');
const appNoBoot = appSource.replace(/\nboot\(\);\s*$/, '\n');

function createHarness({pathname = '/', fetchImpl, hasEventSource = true} = {}) {
  const elements = new Map();
  const storage = new Map();
  const requests = [];
  const streams = [];

  function makeNode(tag = 'div', fragment = false) {
    let value = '';
    let inner = '';
    const listeners = {};
    const nodeObj = {
      tagName: tag.toUpperCase(),
      fragment,
      children: [],
      dataset: {},
      style: {setProperty() {}},
      className: '',
      id: '',
      textContent: '',
      hidden: false,
      disabled: false,
      checked: false,
      indeterminate: false,
      open: false,
      clientWidth: 1000,
      listeners,
      get value() { return value || this.children[0]?.value || ''; },
      set value(next) { value = next; },
      set selectedIndex(index) { value = this.children[index]?.value || ''; },
      get options() { return this.children; },
      get innerHTML() { return inner; },
      set innerHTML(html) { inner = String(html); },
      append(...items) {
        for (const item of items) {
          this.children.push(...(item?.fragment ? item.children : [item]));
        }
      },
      replaceChildren(...items) {
        this.children = [];
        value = '';
        this.append(...items);
      },
      insertAdjacentHTML(_pos, html) { inner += String(html); },
      add(option) { this.append(option); },
      setAttribute() {},
      removeAttribute() {},
      closest() { return null; },
      querySelector(sel) {
        if (sel.startsWith('#')) return getEl(sel);
        return this.children.find(c => c.dataset?.day && sel.includes(c.dataset.day)) || null;
      },
      querySelectorAll(sel) {
        if (sel === '.view') return ['calendar-view', 'downloads-view', 'era5-view', 'catalog-view', 'monitor-view', 'access-view'].map(id => {
          const el = getEl('#' + id);
          el.id = id;
          return el;
        });
        if (sel === '.nav-item') return [
          ['calendar-view', '/calendar'], ['downloads-view', '/downloads'], ['era5-view', '/era5'],
          ['catalog-view', '/catalog'], ['monitor-view', '/monitor'], ['access-view', '/access'],
        ].map(([view, href]) => {
          const el = getEl('a[data-view="' + view + '"]');
          el.dataset.view = view;
          el.href = href;
          return el;
        });
        return [];
      },
      addEventListener(name, cb) { listeners[name] = cb; },
      close() { this.open = false; },
      showModal() { this.open = true; },
    };
    nodeObj.classList = {
      contains(cls) { return nodeObj.className.split(/\s+/).includes(cls); },
      toggle(cls, enabled) {
        const set = new Set(nodeObj.className.split(/\s+/).filter(Boolean));
        const next = enabled ?? !set.has(cls);
        if (next) set.add(cls); else set.delete(cls);
        nodeObj.className = [...set].join(' ');
      },
      add(...classes) { classes.forEach(c => this.toggle(c, true)); },
      remove(...classes) { classes.forEach(c => this.toggle(c, false)); },
    };
    return nodeObj;
  }

  function getEl(selector) {
    if (!elements.has(selector)) elements.set(selector, makeNode());
    return elements.get(selector);
  }

  class MockStream {
    constructor(url) { this.url = url; this.listeners = {}; this.closed = false; this.readyState = 1; streams.push(this); }
    addEventListener(name, cb) { this.listeners[name] = cb; }
    close() { this.closed = true; this.readyState = 2; }
    emit(name, payload) { this.listeners[name]?.({data: JSON.stringify(payload)}); }
  }

  const docEl = {dataset: {view: 'calendar-view'}};
  const doc = {
    documentElement: docEl,
    body: makeNode('body'),
    hidden: false,
    querySelector(sel) {
      if (sel === '#') throw new DOMException('Invalid selector', 'SyntaxError');
      return getEl(sel);
    },
    querySelectorAll: sel => getEl('#calendar-view').querySelectorAll(sel),
    getElementById: id => getEl('#' + id),
    createElement: tag => makeNode(tag),
    createDocumentFragment: () => makeNode('fragment', true),
    addEventListener() {},
  };

  const timers = new Set();
  const trackSetTimeout = (fn, ms, ...args) => {
    const id = setTimeout(() => { timers.delete(id); fn(...args); }, ms);
    if (typeof id.unref === 'function') id.unref();
    timers.add(id);
    return id;
  };
  const trackClearTimeout = id => { timers.delete(id); clearTimeout(id); };

  const context = vm.createContext({
    document: doc,
    window: {addEventListener() {}, location: {pathname, replace() {}}, matchMedia: () => ({matches: false})},
    location: {pathname, replace() {}},
    history: {pushState() {}, replaceState() {}},
    Option: function(text, value) { this.text = text; this.value = value; },
    localStorage: {getItem: k => storage.get(k) ?? null, setItem: (k, v) => storage.set(k, v)},
    navigator: {},
    TextDecoder,
    TextEncoder,
    URLSearchParams,
    URL,
    AbortController,
    setTimeout: trackSetTimeout, clearTimeout: trackClearTimeout, console,
    ...(hasEventSource ? {EventSource: MockStream} : {}),
    fetch: async (url, options = {}) => {
      requests.push({url: String(url), ...options});
      if (fetchImpl) return fetchImpl(String(url), options);
      if (String(url).startsWith('/api/v1/auth/session')) {
        return {ok: true, status: 200, headers: new Headers({'content-type': 'application/json'}), json: async () => ({user: {username: 'alice', role: 'user'}})};
      }
      if (String(url).startsWith('/api/v1/sources')) {
        return {ok: true, status: 200, headers: new Headers({'content-type': 'application/json'}), json: async () => ({sources: [{id: 'goes18', name: 'GOES-18', capabilities: ['index', 'download']}]})};
      }
      if (String(url).startsWith('/api/v1/download-tasks')) {
        return {ok: true, status: 200, headers: new Headers({'content-type': 'application/json'}), json: async () => ({tasks: [], page: 1, pages: 1, total: 0, has_more: false, next_cursor: '', cursor: 'c0'})};
      }
      if (String(url).startsWith('/api/v1/monitor/downloads')) {
        return {ok: true, status: 200, headers: new Headers({'content-type': 'application/json'}), json: async () => ({window: '1h', bucket_seconds: 15, providers: []})};
      }
      if (String(url).startsWith('/api/v1/downloads/catalog')) {
        return {ok: true, status: 200, headers: new Headers({'content-type': 'application/json'}), json: async () => ({products: []})};
      }
      if (String(url).startsWith('/api/products')) {
        return {ok: true, status: 200, headers: new Headers({'content-type': 'application/json'}), json: async () => ([
          {sources:[{id:'goes18',name:'GOES-18'}],family:'goes'},
          {product_key: 'goes18:ABI-L1b-RadC', source_id: 'goes18', satellite: 'GOES-18', instrument: 'ABI', level: 'L1b', data_type: 'ABI-L1b-RadC', metadata: {time_start: '2024-01-01'}},
          {done:true},
        ])};
      }
      if (String(url).startsWith('/api/coverage/year')) {
        return {ok: true, status: 200, headers: new Headers({'content-type': 'application/json'}), json: async () => ({v: 1, bands: [2], days: []})};
      }
      return {ok: true, status: 200, headers: new Headers({'content-type': 'application/json'}), json: async () => ({})};
    },
  });
  vm.runInContext(appNoBoot, context);
  const cleanup = () => { for (const id of timers) clearTimeout(id); timers.clear(); for (const s of streams) s.close(); };
  return {context, doc, docEl, element: getEl, requests, streams, cleanup};
}

test('modernized HTML keeps unified icons, native overlays, and pre-paint view activation script', () => {
  assert.match(indexHTML, /<html lang="zh-CN" data-view="calendar-view">/);
  assert.match(indexHTML, /<head>[\s\S]*document\.documentElement\.dataset\.view=m\[location\.pathname\]\|\|'calendar-view'/);
  assert.match(indexHTML, /<dialog id="download-confirm"[^>]*closedby="any"/);
  assert.match(indexHTML, /id="download-load-more"/);
  for (const guideID of ['calendar-legend-guide', 'era5-guide', 'downloads-guide', 'monitor-guide', 'access-guide']) {
    assert.match(indexHTML, new RegExp(`id="${guideID}" popover="auto" class="guide-popover"`));
    assert.match(indexHTML, new RegExp(`popovertarget="${guideID}"`));
  }
  for (const sym of ['menu', 'calendar', 'download', 'layers', 'book', 'activity', 'key', 'help', 'globe']) {
    assert.match(iconsSVG, new RegExp(`id="${sym}"`));
  }
  assert.match(loginHTML, /href="\/styles\.css\?v=38"/);
  assert.match(indexHTML, /href="\/styles\.css\?v=38"/);
});

test('egress stream applies fresh metrics for an active primary proxy', () => {
  const h = createHarness({pathname: '/access'});
  try {
    h.element('#access-view').classList.add('active');
    vm.runInContext("state.access.section = 'egress'; state.access.user = {role: 'admin'};", h.context);
    h.context.ensureAccessEgressStream();
    const [stream] = h.streams;
    const badge = h.element('#access-egress-live');
    stream.emit('open');
    stream.emit('error');
    assert.ok(badge.classList.contains('reconnecting'));
    stream.emit('sync');
    assert.ok(!badge.classList.contains('reconnecting'));

    const routes = [
      {id: 'vwan1', active_transfers: 2, download_mbps: 3, smoothed_mbps: 2},
      {id: 'east-hpc', active_transfers: 8, download_mbps: 104.23, smoothed_mbps: 79.6,
        active_by_source: {viirs: 8}, rates_by_source: {viirs: {download_mbps: 104.23, smoothed_mbps: 79.6}}},
    ];
    const proxy = {nodes: [{protocol: 'vless', active_connections: 4, download_mbps: 50, smoothed_mbps: 45}]};
    const frames = [];
    h.context.renderAccessEgress = () => frames.push(vm.runInContext(
      '({groups: egressSummary(accessEgressRoutes, ""), nodes: egressProxyRows(accessEgressProxy), cursor: state.access.egressCursor})', h.context));
    stream.emit('egress', {routes, proxy, cursor: 'egress-1'});
    stream.emit('egress', {routes: [routes[0], {...routes[1], active_transfers: 6, download_mbps: 92}], proxy, cursor: 'egress-2'});
    assert.equal(frames.length, 2);
    const [first, second] = frames;
    assert.equal(first.groups[1].active, 2);
    assert.equal(first.groups[2].active, 8);
    assert.equal(first.groups[2].live, 104.23);
    assert.equal(first.groups[2].smooth, 79.6);
    assert.equal(first.nodes[0].label, 'VLESS');
    assert.equal(second.groups[2].active, 6);
    assert.equal(second.groups[2].live, 92);
    assert.equal(second.cursor, 'egress-2');
  } finally {
    h.cleanup();
  }
});

test('boot lazily skips calendar product and year fetches on direct non-calendar routes', async () => {
  const dl = createHarness({pathname: '/downloads'});
  try {
    await dl.context.boot();
    assert.equal(dl.docEl.dataset.view, 'downloads-view');
    const dlURLs = dl.requests.map(r => r.url);
    assert.ok(dlURLs.includes('/api/v1/auth/session'));
    assert.ok(dlURLs.includes('/api/v1/sources'));
    assert.ok(!dlURLs.some(u => u.startsWith('/api/products')), `unexpected calendar products request on /downloads: ${dlURLs}`);
    assert.ok(!dlURLs.some(u => u.startsWith('/api/coverage/year') || u.startsWith('/api/year')), `unexpected calendar year request on /downloads: ${dlURLs}`);
  } finally {
    dl.cleanup();
  }

  const cal = createHarness({pathname: '/calendar'});
  try {
    await cal.context.boot();
    assert.equal(cal.docEl.dataset.view, 'calendar-view');
    assert.equal(cal.requests.filter(r => r.url.startsWith('/api/products?bootstrap=1')).length, 1);
    assert.ok(!cal.requests.some(r => r.url === '/api/v1/sources' || r.url.startsWith('/api/coverage/year')));
  } finally {
    cal.cleanup();
  }
});

test('streamNDJSON parses multi-chunk streams and falls back to JSON payloads', async () => {
  const h = createHarness({
    fetchImpl: async url => {
      if (url === '/ndjson') {
        const encoder = new TextEncoder();
        const lines = Array.from({length: 30}, (_, i) => JSON.stringify({id: i + 1})).join('\n') + '\n';
        const mid = Math.floor(lines.length / 2);
        const chunks = [encoder.encode(lines.slice(0, mid)), encoder.encode(lines.slice(mid))];
        let idx = 0;
        return {
          ok: true,
          status: 200,
          headers: new Headers({'content-type': 'application/x-ndjson; charset=utf-8'}),
          body: {
            getReader() {
              return {
                async read() {
                  if (idx < chunks.length) return {value: chunks[idx++], done: false};
                  return {value: undefined, done: true};
                },
              };
            },
          },
        };
      }
      return {
        ok: true,
        status: 200,
        headers: new Headers({'content-type': 'application/json'}),
        json: async () => ({products: [{id: 99}]}),
      };
    },
  });

  const ndjsonBatches = [];
  const all = await h.context.streamNDJSON('/ndjson', chunk => ndjsonBatches.push(chunk.length));
  assert.equal(all.length, 30);
  assert.deepEqual(ndjsonBatches, [24, 6]);

  const jsonChunks = [];
  const fallback = await h.context.streamNDJSON('/json', chunk => jsonChunks.push(chunk));
  assert.equal(fallback.length, 1);
  assert.equal(jsonChunks[0][0].id, 99);
});

test('renderProductCoverageViz and applyDownloadPage cursor state update DOM accurately', () => {
  const h = createHarness();
  vm.runInContext(`
    state.selected = {id: 'goes18:GLM', cadence: 'event', metadata: {}};
    state.coverage.supported = false;
    state.dayState.set('2026-01-01', {day: '2026-01-01', status: 'complete'});
    state.dayState.set('2026-01-02', {day: '2026-01-02', status: 'hourly_complete'});
    state.dayState.set('2026-01-03', {day: '2026-01-03', status: 'partial'});
    renderProductCoverageViz();
  `, h.context);

  const viz = h.element('#product-coverage-viz');
  assert.equal(viz.classList.contains('hidden'), false);
  assert.match(viz.innerHTML, /完整度 <b>67%<\/b>/);
  assert.match(viz.innerHTML, /viz-seg complete/);
  assert.match(viz.innerHTML, /viz-seg partial/);

  h.context.applyDownloadPage({
    tasks: [],
    page: 1,
    pages: 2,
    total: 35,
    has_more: true,
    next_cursor: 'cur-next',
    cursor: 'sync-cur',
  });
  assert.equal(vm.runInContext('state.downloads.hasMore', h.context), true);
  assert.equal(vm.runInContext('state.downloads.nextCursor', h.context), 'cur-next');
  assert.equal(vm.runInContext('state.downloads.syncCursor', h.context), 'sync-cur');
  assert.equal(h.element('#download-load-more').classList.contains('hidden'), false);
});

test('era5-inventory replaces hard 100 cap with 50-row progressive 加载更多 pagination', async () => {
  const invSource = await readFile(new URL('./static/era5-inventory.js', import.meta.url), 'utf8');
  assert.match(invSource, /const pageSize\s*=\s*50/);
  assert.match(invSource, /id="era5-inventory-more"/);
  assert.match(invSource, /加载更多/);
});

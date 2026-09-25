const $ = selector => document.querySelector(selector);
const sourceEl = $('#source');
const satelliteEl = $('#satellite');
const instrumentEl = $('#instrument');
const levelEl = $('#level');
const productEl = $('#product');
const yearEl = $('#year');
const monthEl = $('#month');
const timezoneEl = $('#timezone');
const gridEl = $('#year-grid');
const tipEl = $('#tip');
const detailEl = $('#day-detail');
const state = {
  sources: [],
  collections: [],
  selected: null,
  dayState: new Map(),
  yearPoll: 0,
  dayPoll: 0,
  monitor: {
    window: localStorage.getItem('satellite-data-monitor-window') || '1h',
    selected: localStorage.getItem('satellite-data-monitor-source') || 'earthcare',
    payload: null,
    poll: 0,
  },
  access: {user: null, credentials: [], proxies: [], accounts: []},
  displayZone: localStorage.getItem('satellite-data-zone') === 'BJT' ? 'BJT' : 'UTC',
};

const SIDEBAR_KEY = 'satellite-data-sidebar-expanded';
const FILTER_KEY = 'satellite-data-filters-v1';
const tierLabels = {anonymous: '匿名直连', token: '登录 / Token', special: '特殊访问'};
const statusLabels = {
  complete: '全部时次完整',
  complete_official_gap: '全部时次完整 · 官方有缺时次',
  hourly_complete: '整点完整',
  hourly_complete_official_gap: '整点完整 · 官方有缺时次',
  missing: '本地缺失',
  error: '错误',
  processing: '处理中',
  official_missing: '官方缺失',
  unknown: '待索引',
};

async function json(url, options = {}) {
  const response = await fetch(url, {
    ...options,
    headers: {'Accept': 'application/json', ...(options.headers || {})},
  });
  let payload = {};
  try { payload = await response.json(); } catch {}
  if (!response.ok) {
    const error = new Error(payload.error || `HTTP ${response.status}`);
    error.status = response.status;
    throw error;
  }
  return payload;
}

function esc(value) {
  return String(value ?? '')
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;');
}

function setSidebar(expanded, persist = false) {
  document.body.classList.toggle('sidebar-collapsed', !expanded);
  const button = $('.sidebar-toggle');
  button.setAttribute('aria-expanded', String(expanded));
  button.setAttribute('aria-label', expanded ? '折叠功能导航' : '展开功能导航');
  if (persist) localStorage.setItem(SIDEBAR_KEY, expanded ? '1' : '0');
}

function setView(id) {
  if (id !== 'monitor-view') clearTimeout(state.monitor.poll);
  document.querySelectorAll('.view').forEach(node => node.classList.toggle('active', node.id === id));
  document.querySelectorAll('.nav-item').forEach(node => node.classList.toggle('active', node.dataset.view === id));
}

function selectedSource() {
  return state.sources.find(item => item.id === sourceEl.value);
}

function selectedCollection() {
  return state.collections.find(item => item.id === productEl.value) || null;
}

function uniqueSorted(values) {
  return [...new Set(values.filter(Boolean))].sort((a, b) => String(a).localeCompare(String(b), undefined, {numeric: true}));
}

function fillSelect(node, values, allLabel = '') {
  const previous = node.value;
  node.replaceChildren();
  if (allLabel) {
    const option = new Option(allLabel, '');
    node.add(option);
  }
  for (const value of values) node.add(new Option(value, value));
  if ([...node.options].some(option => option.value === previous)) node.value = previous;
}

function filteredCollections(stage = 4) {
  return state.collections.filter(item => {
    if (stage > 0 && satelliteEl.value && item.satellite !== satelliteEl.value) return false;
    if (stage > 1 && instrumentEl.value && item.instrument !== instrumentEl.value) return false;
    if (stage > 2 && levelEl.value && item.level !== levelEl.value) return false;
    return true;
  });
}

function rebuildFilters(changed = 'source', update = true) {
  if (changed === 'source') {
    fillSelect(satelliteEl, uniqueSorted(state.collections.map(item => item.satellite)), '全部卫星');
  }
  if (changed === 'source' || changed === 'satellite') {
    fillSelect(instrumentEl, uniqueSorted(filteredCollections(1).map(item => item.instrument)), '全部传感器');
  }
  if (changed === 'source' || changed === 'satellite' || changed === 'instrument') {
    fillSelect(levelEl, uniqueSorted(filteredCollections(2).map(item => item.level)), '全部级别');
  }
  const products = filteredCollections(3);
  const previous = productEl.value;
  productEl.replaceChildren();
  for (const item of products) {
    const title = item.metadata?.title && item.metadata.title !== item.product
      ? `${item.product} · ${item.metadata.title}`
      : item.product;
    productEl.add(new Option(title, item.id));
  }
  if ([...productEl.options].some(option => option.value === previous)) productEl.value = previous;
  else if (productEl.options.length) productEl.selectedIndex = 0;
  if (update) updateSelectedProduct();
}

function defaultYear() {
  return new Date().getUTCFullYear();
}

function fillYears() {
  const now = defaultYear();
  const years = [];
  for (let value = now; value >= 2000; value--) years.push(String(value));
  fillSelect(yearEl, years);
  const saved = readFilters();
  if (saved.year && years.includes(saved.year)) yearEl.value = saved.year;
  else yearEl.value = String(now);
}

function fillMonths() {
  monthEl.replaceChildren();
  for (let month = 0; month < 12; month++) {
    monthEl.add(new Option(`${month + 1} 月`, String(month)));
  }
  monthEl.value = String(new Date().getUTCMonth());
}

function updateSelectedProduct() {
  state.selected = selectedCollection();
  state.dayState.clear();
  closeDetail();
  renderCalendar();
  const product = state.selected;
  if (!product) {
    $('#product-title').textContent = state.collections.length ? '当前筛选无产品' : '读取产品目录…';
    $('#product-meta').textContent = '';
    return;
  }
  $('#product-title').textContent = [product.satellite, product.instrument, product.level, product.product].filter(Boolean).join(' · ');
  const source = selectedSource();
  const metadata = [];
  if (product.cadence) metadata.push(`时次 ${product.cadence}`);
  if (product.metadata?.spatial_resolution) metadata.push(product.metadata.spatial_resolution);
  if (product.metadata?.version) metadata.push(`版本 ${product.metadata.version}`);
  if (source) metadata.push(tierLabels[source.tier] || source.tier);
  metadata.push(product.metadata?.local_covered ? '本地已覆盖' : '本地未覆盖');
  $('#product-meta').textContent = metadata.join(' · ');
  persistFilters();
  loadYear();
}

function renderCalendar() {
  const year = Number(yearEl.value || defaultYear());
  gridEl.classList.remove('placeholder-grid');
  gridEl.replaceChildren();
  const start = new Date(Date.UTC(year, 0, 1));
  const end = new Date(Date.UTC(year, 11, 31));
  for (let i = 0; i < start.getUTCDay(); i++) {
    const spacer = document.createElement('i');
    spacer.className = 'c spacer';
    gridEl.append(spacer);
  }
  for (let date = new Date(start); date <= end; date.setUTCDate(date.getUTCDate() + 1)) {
    const day = date.toISOString().slice(0, 10);
    const status = state.dayState.get(day)?.status || 'unknown';
    const cell = document.createElement('i');
    cell.className = `c ${status}`;
    cell.dataset.day = day;
    cell.dataset.status = status;
    cell.tabIndex = 0;
    cell.setAttribute('aria-label', dayTip(day));
    cell.addEventListener('mouseenter', event => showTip(event, day));
    cell.addEventListener('mousemove', moveTip);
    cell.addEventListener('mouseleave', hideTip);
    cell.addEventListener('click', () => queryDay(day));
    cell.addEventListener('keydown', event => {
      if (event.key === 'Enter' || event.key === ' ') queryDay(day);
    });
    gridEl.append(cell);
  }
}

function updateCell(day) {
  const cell = gridEl.querySelector(`[data-day="${day}"]`);
  if (!cell) return;
  const status = state.dayState.get(day)?.status || 'unknown';
  cell.className = `c ${status}`;
  cell.dataset.status = status;
  cell.setAttribute('aria-label', dayTip(day));
}

function dayTip(day) {
  const item = state.dayState.get(day);
  if (!item) return `${day}\n待索引`;
  const lines = [`${day} · ${statusLabels[item.status] || item.status} · ${state.displayZone}`];
  if (item.indexing) lines.push('官方索引中');
  lines.push(`全部官方时次 ${item.local_ok_count || 0} / ${item.official_count || 0}`);
  if (item.hourly_official_count) lines.push(`整点官方时次 ${item.hourly_local_ok_count || 0} / ${item.hourly_official_count}`);
  if (item.missing_count) lines.push(`本地缺失 ${item.missing_count}`);
  if (item.processing_count) lines.push(`处理中 ${item.processing_count}`);
  if (item.error_count) lines.push(`错误 ${item.error_count}`);
  if (item.official_gap_count) lines.push(`固定时序官方缺时次 ${item.official_gap_count}`);
  if (item.updated_at) lines.push(`索引更新 ${String(item.updated_at).replace('T',' ').slice(0,19)} UTC`);
  return lines.join('\n');
}

async function loadYear(schedulePoll = true) {
  clearTimeout(state.yearPoll);
  if (!state.selected) return;
  const product = state.selected.id;
  const year = Number(yearEl.value || defaultYear());
  try {
    const payload = await json(`/api/year?product=${encodeURIComponent(product)}&year=${year}`);
    if (state.selected?.id !== product || Number(yearEl.value) !== year) return;
    state.dayState.clear();
    for (const day of payload.days || []) state.dayState.set(day.day, day);
    renderCalendar();
    if (schedulePoll && (payload.days || []).some(day => day.indexing)) {
      state.yearPoll = setTimeout(() => loadYear(true), 6000);
    }
  } catch (error) {
    $('#product-meta').textContent = `年历索引读取失败 · ${error.message}`;
  }
}

async function queryDay(day) {
  await openDay(day, true);
}

async function queryMonth() {
  if (!state.selected) return;
  const year = Number(yearEl.value);
  const month = Number(monthEl.value || 0);
  const start = new Date(Date.UTC(year, month, 1));
  const end = new Date(Date.UTC(year, month + 1, 1));
  const params = new URLSearchParams({
    product: state.selected.id,
    start: start.toISOString().slice(0, 10),
    end: end.toISOString().slice(0, 10),
  });
  await json('/api/index?' + params, {method: 'POST'});
  loadYear(true);
}

function displayDay(timestamp) {
  const value = new Date(timestamp);
  if (state.displayZone === 'BJT') value.setUTCHours(value.getUTCHours() + 8);
  return value.toISOString().slice(0, 10);
}

function displayTime(timestamp) {
  const value = new Date(timestamp);
  if (state.displayZone === 'BJT') value.setUTCHours(value.getUTCHours() + 8);
  return value.toISOString().slice(11, 19);
}

async function openDay(day, schedulePoll = false) {
  clearTimeout(state.dayPoll);
  if (!state.selected) return;
  const product = state.selected.id;
  let item;
  try {
    item = await json(`/api/day?product=${encodeURIComponent(product)}&day=${day}`);
  } catch (error) {
    detailEl.classList.remove('hidden');
    $('#detail-day').textContent = `${day} · ${state.displayZone}`;
    $('#detail-summary').textContent = `读取失败 · ${error.message}`;
    $('#timeline').replaceChildren();
    $('#granule-list').replaceChildren();
    return;
  }
  if (state.selected?.id !== product) return;
  detailEl.classList.remove('hidden');
  $('#detail-day').textContent = `${day} · ${state.displayZone}`;
  const official = item.official_day;
  const localFiles = (item.slots || []).reduce((sum, slot) => sum + (slot.local?.length || 0), 0);
  $('#detail-summary').textContent = official
    ? `${item.slots.length} 个时次 · 官方资产 ${official.asset_count} · 本地文件 ${localFiles}${item.official_gap_count ? ` · 官方缺时次 ${item.official_gap_count}` : ''}`
    : (item.indexing ? '官方索引中…' : '尚未生成官方索引');
  renderTimeline(item);
  renderGranules(item.slots || []);
  if (schedulePoll && item.indexing && !official) state.dayPoll = setTimeout(() => openDay(day, true), 3000);
}

function renderTimeline(day) {
  const timeline = $('#timeline');
  timeline.replaceChildren();
  const interval = Number(day.cadence_minutes || 0);
  const slots = day.slots || [];
  if (day.timeline_mode === 'grid' && interval > 0 && 1440 % interval === 0) {
    const total = 1440 / interval;
    const by = new Map();
    for (const slot of slots) {
      const date = new Date(slot.slot);
      const minute = date.getUTCHours() * 60 + date.getUTCMinutes();
      by.set(Math.round(minute / interval) % total, slot);
    }
    const grid = document.createElement('div');
    grid.className = 'slots';
    grid.style.gridTemplateColumns = `repeat(${total},minmax(4px,1fr))`;
    for (let i = 0; i < total; i++) {
      const slot = by.get(i);
      const cell = document.createElement('i');
      const hourly = i % (60 / interval) === 0;
      const status = slot?.status || (day.official_day?.state === 'present' ? 'official_missing' : 'unknown');
      cell.className = `slot c ${status}${hourly ? ' hourly' : ''}`;
      const minute = i * interval;
      cell.title = `${String(Math.floor(minute / 60)).padStart(2,'0')}:${String(minute % 60).padStart(2,'0')} · ${statusLabels[status] || status}`;
      grid.append(cell);
    }
    timeline.append(grid);
    return;
  }
  for (const slot of slots) {
    const [hour, minute, second] = displayTime(slot.slot).split(':').map(Number);
    const pos = (hour * 3600 + minute * 60 + second) / 86400 * 100;
    const marker = document.createElement('i');
    const hourly = Math.min(minute, 60 - minute) <= 3;
    marker.className = `event-slot c ${slot.status}${hourly ? ' hourly' : ''}`;
    marker.style.left = `${Math.max(0, Math.min(100, pos)).toFixed(4)}%`;
    marker.title = `${displayTime(slot.slot)} · ${statusLabels[slot.status] || slot.status}`;
    timeline.append(marker);
  }
}

function renderGranules(slots) {
  const list = $('#granule-list');
  list.replaceChildren();
  for (const slot of slots.slice(0, 80)) {
    const first = slot.official?.[0];
    const local = slot.local?.length || 0;
    const officialAssets = (slot.official || []).reduce((sum, row) => sum + (row.asset_count || 0), 0);
    const row = document.createElement('div');
    row.className = 'granule-row';
    row.innerHTML = `<time>${esc(displayTime(slot.slot))}</time><span>${local} / ${officialAssets} 文件</span><code title="${esc(first?.filename || '')}">${esc(first?.filename || statusLabels[slot.status] || slot.status)}</code>`;
    list.append(row);
  }
  if (slots.length > 80) {
    const row = document.createElement('div');
    row.className = 'granule-row';
    row.textContent = `另有 ${slots.length - 80} 个时次未展开显示`;
    list.append(row);
  }
}

function closeDetail() {
  clearTimeout(state.dayPoll);
  detailEl.classList.add('hidden');
}

function showTip(event, day) {
  tipEl.textContent = dayTip(day);
  tipEl.classList.remove('hidden');
  moveTip(event);
}

function moveTip(event) {
  tipEl.style.left = `${event.clientX + 12}px`;
  tipEl.style.top = `${event.clientY + 12}px`;
}

function hideTip() {
  tipEl.classList.add('hidden');
}

function renderSources() {
  const node = $('#sources');
  node.replaceChildren();
  const counts = {anonymous: 0, token: 0, special: 0};
  for (const source of state.sources) {
    counts[source.tier] = (counts[source.tier] || 0) + 1;
    const row = document.createElement('article');
    row.className = 'source-row';
    row.innerHTML = `
      <strong>${esc(source.name)}</strong>
      <span class="tier ${esc(source.tier)}">${esc(tierLabels[source.tier] || source.tier)}</span>
      <p>${esc(source.description || '')}</p>
      <small>${esc(source.method || '')}</small>`;
    node.append(row);
  }
  for (const [tier, count] of Object.entries(counts)) {
    const target = document.querySelector(`#tier-${tier}`);
    if (target) target.textContent = count;
  }
}

async function loadSources() {
  const payload = await json('/api/v1/sources');
  state.sources = payload.sources || [];
  renderSources();
  const saved = readFilters();
  const previous = sourceEl.value;
  sourceEl.replaceChildren();
  for (const source of state.sources) sourceEl.add(new Option(source.name, source.id));
  if (state.sources.some(item => item.id === previous)) sourceEl.value = previous;
  if (saved.source && state.sources.some(item => item.id === saved.source)) sourceEl.value = saved.source;
  else if (state.sources.some(item => item.id === 'goes18')) sourceEl.value = 'goes18';
  await loadCatalog();
}

async function loadCatalog() {
  $('#product-title').textContent = '读取产品目录…';
  $('#product-meta').textContent = '';
  state.collections = [];
  state.selected = null;
  gridEl.classList.add('placeholder-grid');
  gridEl.replaceChildren();
  const source = sourceEl.value;
  if (!source) return;
  try {
    const payload = await json('/api/products?source=' + encodeURIComponent(source));
    state.collections = (payload || []).map(product => ({
      id: product.product_key,
      source_id: product.source_id,
      satellite: product.satellite,
      instrument: product.instrument,
      level: product.level,
      product: product.data_type,
      cadence: product.cadence,
      metadata: {
        ...(product.metadata || {}),
        official_id: product.official_id || '',
        title: product.display_name || product.data_type,
        spatial_resolution: product.resolution || '',
        version: product.version || '',
        area_type: product.area_type || '',
        catalog_source: product.catalog_source || '',
        product_count: product.product_count ? String(product.product_count) : '',
        local_covered: Boolean(product.local_covered),
      },
    }));
    const saved = readFilters();
    rebuildFilters('source', false);
    if (saved.source === source) {
      restoreSelect(satelliteEl, saved.satellite);
      rebuildFilters('satellite', false);
      restoreSelect(instrumentEl, saved.instrument);
      rebuildFilters('instrument', false);
      restoreSelect(levelEl, saved.level);
      rebuildFilters('level', false);
      restoreSelect(productEl, saved.product);
    }
    updateSelectedProduct();
  } catch (error) {
    gridEl.classList.remove('placeholder-grid');
    $('#product-title').textContent = '产品目录读取失败';
    $('#product-meta').textContent = error.message;
  }
}

function restoreSelect(node, value) {
  if (value && [...node.options].some(option => option.value === value)) node.value = value;
}

function readFilters() {
  try { return JSON.parse(localStorage.getItem(FILTER_KEY) || '{}'); } catch { return {}; }
}

function persistFilters() {
  localStorage.setItem(FILTER_KEY, JSON.stringify({
    source: sourceEl.value,
    satellite: satelliteEl.value,
    instrument: instrumentEl.value,
    level: levelEl.value,
    product: productEl.value,
    year: yearEl.value,
  }));
}

function resetFilters() {
  localStorage.removeItem(FILTER_KEY);
  if (state.sources.some(item => item.id === 'goes18')) sourceEl.value = 'goes18';
  else sourceEl.selectedIndex = 0;
  yearEl.value = String(defaultYear());
  loadCatalog();
}

function monitorNumber(object, key) {
  const value = object?.[key];
  return typeof value === 'number' && Number.isFinite(value) ? value : null;
}

function monitorValue(value, unit = '', digits = 0) {
  if (!Number.isFinite(value)) return '—';
  return value.toFixed(digits) + unit;
}

function monitorMean(values) {
  const valid = values.filter(Number.isFinite);
  if (!valid.length) return null;
  return valid.reduce((sum, value) => sum + value, 0) / valid.length;
}

function monitorMedian(values) {
  const valid = values.filter(Number.isFinite).sort((a, b) => a - b);
  if (!valid.length) return null;
  const middle = Math.floor(valid.length / 2);
  return valid.length % 2 ? valid[middle] : (valid[middle - 1] + valid[middle]) / 2;
}

function monitorTone(link) {
  const latest = link?.latest;
  if (!latest?.sampled_at) return 'stale';
  const age = Date.now() - new Date(latest.sampled_at).getTime();
  if (!Number.isFinite(age) || age > 90_000) return 'stale';
  const loss = monitorNumber(latest, 'loss_pct');
  const latency = monitorNumber(latest, 'latency_ms');
  const jitter = monitorNumber(latest, 'jitter_ms');
  if ((loss != null && loss >= 25) || (latency != null && latency >= 900)) return 'bad';
  if ((loss != null && loss > 0) || (latency != null && latency >= 450) || (jitter != null && jitter >= 100)) return 'warn';
  return 'ok';
}

function monitorToneLabel(tone) {
  return {ok: '正常', warn: '波动', bad: '异常', stale: '待采样'}[tone] || tone;
}

function monitorClock(value) {
  if (!value) return '—';
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return '—';
  if (state.displayZone === 'BJT') date.setUTCHours(date.getUTCHours() + 8);
  return date.toISOString().slice(11, 16);
}

function monitorSpark(points = []) {
  const values = points.map(point => monitorNumber(point, 'latency_ms')).filter(Number.isFinite);
  if (values.length < 2) {
    return '<svg viewBox="0 0 240 36" preserveAspectRatio="none"><line class="spark-base" x1="0" y1="30" x2="240" y2="30"/></svg>';
  }
  const width = 240;
  const height = 36;
  const min = Math.min(...values);
  const max = Math.max(...values);
  const span = Math.max(1, max - min);
  const coords = values.map((value, index) => {
    const x = index * width / Math.max(1, values.length - 1);
    const y = 29 - ((value - min) / span) * 23;
    return [x, y];
  });
  const line = coords.map(([x, y], index) => (index ? 'L' : 'M') + x.toFixed(1) + ',' + y.toFixed(1)).join(' ');
  const area = line + ' L' + width + ',32 L0,32 Z';
  const last = coords[coords.length - 1];
  return '<svg viewBox="0 0 ' + width + ' ' + height + '" preserveAspectRatio="none">' +
    '<line class="spark-base" x1="0" y1="30" x2="' + width + '" y2="30"/>' +
    '<path class="spark-area" d="' + area + '"/>' +
    '<path class="spark-line" d="' + line + '"/>' +
    '<circle class="spark-last" cx="' + last[0].toFixed(1) + '" cy="' + last[1].toFixed(1) + '" r="2.3"/>' +
    '</svg>';
}

function monitorChart(points, key, fixedMax = null) {
  const entries = (points || []).map(point => ({
    time: new Date(point.sampled_at).getTime(),
    value: monitorNumber(point, key),
  })).filter(item => Number.isFinite(item.time) && Number.isFinite(item.value));
  if (!entries.length) return '<div class="pool-empty">等待历史样本</div>';
  const width = 600;
  const height = 148;
  const left = 32;
  const right = 8;
  const top = 8;
  const bottom = 21;
  const plotWidth = width - left - right;
  const plotHeight = height - top - bottom;
  const t0 = entries[0].time;
  const t1 = Math.max(t0 + 1, entries[entries.length - 1].time);
  const observedMax = Math.max(...entries.map(item => item.value));
  const maxValue = fixedMax ?? Math.max(1, observedMax * 1.15);
  const bucketMS = Math.max(1, Number(state.monitor.payload?.bucket_seconds || 15) * 1000);
  let path = '';
  let previousTime = null;
  let lastCoord = null;
  for (const item of entries) {
    const x = left + ((item.time - t0) / (t1 - t0)) * plotWidth;
    const y = top + (1 - Math.min(maxValue, Math.max(0, item.value)) / maxValue) * plotHeight;
    const command = previousTime != null && item.time - previousTime <= bucketMS * 2.5 ? 'L' : 'M';
    path += command + x.toFixed(1) + ',' + y.toFixed(1) + ' ';
    previousTime = item.time;
    lastCoord = [x, y];
  }
  const grids = [0, .25, .5, .75, 1].map(ratio => {
    const y = top + ratio * plotHeight;
    return '<line class="chart-grid" x1="' + left + '" y1="' + y.toFixed(1) + '" x2="' + (width-right) + '" y2="' + y.toFixed(1) + '"/>';
  }).join('');
  const topLabel = fixedMax === 100 ? '100%' : Math.round(maxValue) + ' ms';
  const startLabel = monitorClock(new Date(t0).toISOString());
  const endLabel = monitorClock(new Date(t1).toISOString());
  const dot = lastCoord
    ? '<circle class="chart-dot" cx="' + lastCoord[0].toFixed(1) + '" cy="' + lastCoord[1].toFixed(1) + '" r="3"/>'
    : '';
  return '<svg viewBox="0 0 ' + width + ' ' + height + '" preserveAspectRatio="none">' +
    grids +
    '<text class="chart-label" x="1" y="' + (top + 4) + '">' + esc(topLabel) + '</text>' +
    '<text class="chart-label" x="' + left + '" y="' + (height - 4) + '">' + esc(startLabel) + '</text>' +
    '<text class="chart-label" text-anchor="end" x="' + (width-right) + '" y="' + (height - 4) + '">' + esc(endLabel) + '</text>' +
    '<path class="chart-line" d="' + path.trim() + '"/>' + dot + '</svg>';
}

function renderMonitorDetail(link) {
  const node = $('#monitor-detail');
  if (!link) {
    node.classList.add('hidden');
    return;
  }
  const tone = monitorTone(link);
  const latest = link.latest || {};
  node.classList.remove('hidden');
  $('#monitor-detail-title').textContent = link.name;
  $('#monitor-detail-target').textContent = 'c2ln1 → ' + link.target + ':' + link.port + (latest.resolved_ip ? ' · ' + latest.resolved_ip : '');
  const stateNode = $('#monitor-detail-state');
  stateNode.className = 'monitor-state-pill ' + tone;
  stateNode.textContent = monitorToneLabel(tone);
  const latencyValue = monitorNumber(latest, 'latency_ms');
  const jitterValue = monitorNumber(latest, 'jitter_ms');
  const lossValue = monitorNumber(latest, 'loss_pct');
  $('#monitor-detail-latency').textContent = monitorValue(latencyValue, ' ms', 1);
  $('#monitor-detail-jitter').textContent = monitorValue(jitterValue, ' ms', 1);
  $('#monitor-detail-loss').textContent = monitorValue(lossValue, '%', lossValue != null && lossValue % 1 ? 1 : 0);
  const latency = $('#monitor-latency-chart');
  const jitter = $('#monitor-jitter-chart');
  const loss = $('#monitor-loss-chart');
  latency.className = 'monitor-chart latency';
  jitter.className = 'monitor-chart jitter';
  loss.className = 'monitor-chart loss';
  latency.innerHTML = monitorChart(link.points, 'latency_ms');
  jitter.innerHTML = monitorChart(link.points, 'jitter_ms');
  loss.innerHTML = monitorChart(link.points, 'loss_pct', 100);
}

function renderMonitor() {
  const payload = state.monitor.payload;
  if (!payload) return;
  $('#monitor-origin-name').textContent = payload.origin || 'c2ln1';
  const links = payload.links || [];
  if (!links.some(link => link.source_id === state.monitor.selected)) {
    state.monitor.selected = links.find(link => link.source_id === 'earthcare')?.source_id || links[0]?.source_id || '';
  }
  localStorage.setItem('satellite-data-monitor-source', state.monitor.selected);
  const tones = links.map(monitorTone);
  const healthy = tones.filter(tone => tone === 'ok').length;
  const latest = links.map(link => link.latest).filter(Boolean);
  const medianLatency = monitorMedian(latest.map(item => monitorNumber(item, 'latency_ms')));
  const meanJitter = monitorMean(latest.map(item => monitorNumber(item, 'jitter_ms')));
  const meanLoss = monitorMean(latest.map(item => monitorNumber(item, 'loss_pct')));
  $('#monitor-healthy').textContent = healthy + '/' + links.length;
  $('#monitor-link-count').textContent = latest.length ? latest.length + ' 条已有样本' : '等待首轮采样';
  $('#monitor-latency').textContent = monitorValue(medianLatency, ' ms', 0);
  $('#monitor-jitter').textContent = monitorValue(meanJitter, ' ms', 1);
  $('#monitor-loss').textContent = monitorValue(meanLoss, '%', meanLoss != null && meanLoss > 0 && meanLoss < 10 ? 1 : 0);

  const grid = $('#monitor-links');
  grid.innerHTML = links.map(link => {
    const tone = monitorTone(link);
    const current = link.latest || {};
    const latencyValue = monitorNumber(current, 'latency_ms');
    const jitterValue = monitorNumber(current, 'jitter_ms');
    const lossValue = monitorNumber(current, 'loss_pct');
    const latency = monitorValue(latencyValue, ' ms', 0);
    const jitter = monitorValue(jitterValue, ' ms', 1);
    const loss = monitorValue(lossValue, '%', lossValue != null && lossValue % 1 ? 1 : 0);
    const target = link.target + (current.resolved_ip ? ' · ' + current.resolved_ip : '');
    return '<button type="button" class="monitor-link-card' + (state.monitor.selected === link.source_id ? ' selected' : '') +
      '" data-monitor-source="' + esc(link.source_id) + '" data-tone="' + tone + '">' +
      '<div class="monitor-link-head">' +
      '<div class="monitor-link-title"><strong><i></i>' + esc(link.name) + '</strong><small>' + esc(target) + '</small></div>' +
      '<div class="monitor-link-primary"><strong>' + esc(latency) + '</strong><small>' + esc(monitorToneLabel(tone)) + '</small></div>' +
      '</div><div class="monitor-link-meta">' +
      '<span>Jitter <b>' + esc(jitter) + '</b></span><span>Loss <b>' + esc(loss) + '</b></span>' +
      '</div><div class="monitor-spark">' + monitorSpark(link.points) + '</div></button>';
  }).join('');
  renderMonitorDetail(links.find(link => link.source_id === state.monitor.selected));
}

function syncMonitorWindowButtons() {
  document.querySelectorAll('[data-monitor-window]').forEach(button => {
    button.classList.toggle('active', button.dataset.monitorWindow === state.monitor.window);
  });
}

async function loadMonitor(schedule = true) {
  clearTimeout(state.monitor.poll);
  syncMonitorWindowButtons();
  try {
    const payload = await json('/api/v1/monitor/links?window=' + encodeURIComponent(state.monitor.window));
    state.monitor.payload = payload;
    $('#monitor-error').textContent = '';
    renderMonitor();
  } catch (error) {
    $('#monitor-error').textContent = '链路监控读取失败 · ' + error.message;
  }
  if (schedule && $('#monitor-view').classList.contains('active')) {
    state.monitor.poll = setTimeout(() => loadMonitor(true), 15_000);
  }
}

const accessKindLabels = {
  laads: 'LAADS', earthdata: 'Earthdata', earthcare: 'ESA MAAP', eumetsat: 'EUMETSAT', cloudsat: 'CloudSat DPC',
};
const providerKinds = {modis: 'laads', viirs: 'laads', mtg: 'eumetsat', earthcare: 'earthcare', calipso: 'earthdata', cloudsat: 'cloudsat'};
const credentialGuides = {
  laads: {
    title: 'LAADS Download Token',
    summary: '用于 MODIS 与 VIIRS。优先使用 LAADS 自己生成的下载 Token，而不是通用 Earthdata Token。',
    steps: ['登录 NASA LAADS DAAC。', '在 Token / Download Token 区域生成或复制当前 Token。', '把完整 Token 粘贴到下方 Token 输入框；保存后网页只保留掩码。'],
    note: '一条 LAADS 凭据可以分别绑定 MODIS、VIIRS 两个账号路由。下载器会优先读取 LAADS Token 自带的 exp 到期字段；只有 Token 本身不提供 exp 时才显示“官网未提供”。',
    url: 'https://ladsweb.modaps.eosdis.nasa.gov/',
    link: '打开 LAADS DAAC',
  },
  earthdata: {
    title: 'NASA Earthdata User Token',
    summary: '当前用于 CALIPSO。需要 Earthdata Login 账号生成 User Token。',
    steps: ['登录 Earthdata Login。', '进入 Generate Token / User Token 页面并生成 Token。', '点击 Show Token，复制完整值后粘贴到下方。'],
    note: 'Earthdata User Token 当前有效 60 天；保存后下载器会自动读取 Token 的 exp 字段显示到期时间，无需手填。',
    url: 'https://urs.earthdata.nasa.gov/',
    link: '打开 Earthdata Login',
  },
  earthcare: {
    title: 'ESA MAAP EarthCARE Token',
    summary: '用于 EarthCARE 数据访问。需要 ESA EO / MAAP 账号，并生成 EarthCARE MAAP 长期访问 Token。',
    steps: ['登录 ESA MAAP，并确保账号有 EarthCARE 数据访问权限。', '打开 MAAP Token 页面生成长期 Token。', '复制生成的 Token 粘贴到下方；不要填写示例里的 CLIENT_SECRET。'],
    note: 'offline token 当前有效 90 天；下载器会自动读取 Token 的 exp 字段。部分 Cal/Val 或 Commissioning 集合仍取决于你的 ESA 账号权限。',
    url: 'https://portal.maap.eo.esa.int/ini/services/auth/token/90dToken.php',
    link: '生成 ESA MAAP Token',
  },
  eumetsat: {
    title: 'EUMETSAT API 凭据 · Consumer Key / Consumer Secret',
    summary: '用于 MTG。这里只保存 API Key Management 提供的 Consumer Key + Consumer Secret；下载器会用 OAuth2 client_credentials 自动换取短期 Access Token。',
    steps: ['登录 EUMETSAT Data Store / User Portal。', '进入 API Key Management，在 User credentials 区域找到 Consumer key 与 Consumer secret。', '只把这两个值分别复制到下方两个输入框；页面下方的一小时 API token 不需要复制。'],
    note: '本凭据表单只接收 Consumer Key + Consumer Secret。下载器会自动向 EUMETSAT /token 端点申请并续期 Bearer Access Token。',
    url: 'https://api.eumetsat.int/api-key/',
    link: '打开 EUMETSAT API Key',
  },
  cloudsat: {
    title: 'CloudSat DPC THREDDS 账号',
    summary: '用于 CloudSat THREDDS / HTTPServer 下载，用户名与普通 DPC 网站登录并不一定是同一套密码。',
    steps: ['先注册并登录 CloudSat DPC 网站账号。', '按官方 THREDDS 指引向 cloudsat@colostate.edu 申请 THREDDS 访问。', '收到授权后，用户名通常是你的邮箱；把邮件提供的 THREDDS 密码填入下方。'],
    note: '如果你只有 CloudSat 网站密码、尚未申请 THREDDS 密码，下载仍可能认证失败。官方未提供这组长期凭据的固定到期字段。',
    url: 'https://www.cloudsat.cira.colostate.edu/order/THREDDS-OpeNDAP-access',
    link: '查看 CloudSat THREDDS 指引',
  },
};

async function loadAccess() {
  $('#access-login-error').textContent = '';
  try {
    const session = await json('/api/v1/access/session');
    state.access.user = session.user;
    $('#access-user').textContent = session.user?.username ? `${session.user.username} · admin` : '管理员';
    $('#access-login').classList.add('hidden');
    $('#access-manager').classList.remove('hidden');
    $('#logout-access').classList.remove('hidden');
    $('#refresh-access').classList.remove('hidden');
    await loadAccessPools();
  } catch (error) {
    state.access.user = null;
    $('#access-user').textContent = '';
    $('#access-manager').classList.add('hidden');
    $('#access-login').classList.remove('hidden');
    $('#logout-access').classList.add('hidden');
  }
}

async function loadAccessPools() {
  const payload = await json('/api/v1/access/pools');
  state.access.credentials = payload.credentials || [];
  state.access.proxies = payload.proxies || [];
  state.access.accounts = payload.accounts || [];
  $('#access-account-count').textContent = state.access.accounts.length;
  $('#access-credential-count').textContent = state.access.credentials.length;
  $('#access-proxy-count').textContent = state.access.proxies.length;
  renderCredentialList();
  renderAccountList();
  renderProxyList();
  rebuildAccessSelects();
}

function poolStatePill(item) {
  const stateName = item.enabled === false ? 'disabled' : (item.state || 'ready');
  const label = {ready: '可用', auth_required: '需认证', degraded: '降级', disabled: '已停用'}[stateName] || stateName;
  return `<span class="pool-pill ${esc(stateName)}">${esc(label)}</span>`;
}

function renderCredentialList() {
  const node = $('#credential-list');
  node.replaceChildren();
  if (!state.access.credentials.length) {
    node.innerHTML = '<div class="pool-empty">暂无凭据</div>';
    return;
  }
  for (const item of state.access.credentials) {
    const row = document.createElement('div');
    row.className = 'pool-row';
    row.innerHTML = `
      <div class="pool-row-main"><div class="pool-row-title"><strong>${esc(item.alias)}</strong><small>${esc(accessKindLabels[item.kind] || item.kind)} · ${esc(item.secret_hint || 'Secret 已保存')}</small></div>
      <div class="pool-actions"><button type="button" data-edit-credential="${item.id}">编辑</button><button type="button" data-delete-credential="${item.id}">删除</button></div></div>
      <div class="pool-meta">${poolStatePill(item)}${credentialExpiryPill(item)}<span class="pool-pill">优先级 ${item.priority || 0}</span>${item.last_success_at ? `<span>成功 ${esc(formatAccessTime(item.last_success_at))}</span>` : ''}</div>
      ${item.last_error ? `<small class="form-error">${esc(item.last_error)}</small>` : ''}`;
    node.append(row);
  }
}

function credentialExpiryPill(item) {
  const status = String(item?.expiry_status || 'unknown');
  const source = String(item?.expiry_source || '');
  const sourceTitle = source ? ` title="${esc(source)}"` : '';
  if (status === 'not_provided') return `<span class="pool-pill expiry-unknown"${sourceTitle}>官网未提供到期字段</span>`;
  if (status === 'check_failed') return `<span class="pool-pill expired"${sourceTitle}>有效期读取失败</span>`;
  if (status !== 'known' || !item?.expires_at) return `<span class="pool-pill expiry-unknown"${sourceTitle}>有效期待同步</span>`;
  const expiry = new Date(String(item.expires_at));
  if (!Number.isFinite(expiry.getTime())) return `<span class="pool-pill expiry-unknown"${sourceTitle}>到期 ${esc(item.expires_at)}</span>`;
  const now = new Date();
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const expiryDay = new Date(expiry.getFullYear(), expiry.getMonth(), expiry.getDate());
  const days = Math.round((expiryDay.getTime() - today.getTime()) / 86400000);
  const date = expiry.toISOString().slice(0, 10);
  if (days < 0) return `<span class="pool-pill expired"${sourceTitle}>已过期 · ${esc(date)}</span>`;
  if (days === 0) return `<span class="pool-pill expiring"${sourceTitle}>今天到期 · ${esc(date)}</span>`;
  if (days <= 30) return `<span class="pool-pill expiring"${sourceTitle}>${days} 天后到期 · ${esc(date)}</span>`;
  return `<span class="pool-pill"${sourceTitle}>到期 ${esc(date)}</span>`;
}

function renderAccountList() {
  const node = $('#account-list');
  node.replaceChildren();
  if (!state.access.accounts.length) {
    node.innerHTML = '<div class="pool-empty">暂无账号路由</div>';
    return;
  }
  for (const item of state.access.accounts) {
    const route = item.proxy_mode === 'direct' ? '直连' : item.proxy_mode === 'auto' ? '代理池' : `固定 · ${item.proxy_alias || '—'}`;
    const row = document.createElement('div');
    row.className = 'pool-row';
    row.innerHTML = `
      <div class="pool-row-main"><div class="pool-row-title"><strong>${esc(item.alias)}</strong><small>${esc(item.source_id)} · ${esc(item.credential_alias || '匿名')} · ${esc(route)}</small></div>
      <div class="pool-actions"><button type="button" data-edit-account="${item.id}">编辑</button><button type="button" data-delete-account="${item.id}">删除</button></div></div>
      <div class="pool-meta">${poolStatePill(item)}<span class="pool-pill">优先级 ${item.priority || 0}</span>${item.last_success_at ? `<span>成功 ${esc(formatAccessTime(item.last_success_at))}</span>` : ''}</div>
      ${item.last_error ? `<small class="form-error">${esc(item.last_error)}</small>` : ''}`;
    node.append(row);
  }
}

function renderProxyList() {
  const node = $('#proxy-list');
  node.replaceChildren();
  if (!state.access.proxies.length) {
    node.innerHTML = '<div class="pool-empty">暂无代理</div>';
    return;
  }
  for (const item of state.access.proxies) {
    const scope = item.providers?.length ? item.providers.join(', ') : '*';
    const row = document.createElement('div');
    row.className = 'pool-row';
    row.innerHTML = `
      <div class="pool-row-main"><div class="pool-row-title"><strong>${esc(item.alias)}</strong><small>${esc(item.scheme)} · ${esc(item.endpoint_hint)} · ${esc(scope)}</small></div>
      <div class="pool-actions"><button type="button" data-edit-proxy="${item.id}">编辑</button><button type="button" data-delete-proxy="${item.id}">删除</button></div></div>
      <div class="pool-meta">${poolStatePill(item)}<span class="pool-pill">优先级 ${item.priority || 0}</span>${item.last_success_at ? `<span>成功 ${esc(formatAccessTime(item.last_success_at))}</span>` : ''}</div>
      ${item.last_error ? `<small class="form-error">${esc(item.last_error)}</small>` : ''}`;
    node.append(row);
  }
}

function formatAccessTime(value) {
  return String(value || '').replace('T', ' ').slice(0, 19) + 'Z';
}

function credentialFields() {
  const kind = $('#credential-kind').value;
  const node = $('#credential-secret-fields');
  const note = $('#credential-secret-note');
  const password = (id, label, placeholder = '') => `<label>${label}<input id="${id}" type="password" autocomplete="new-password" placeholder="${placeholder}"></label>`;
  if (['laads','earthdata','earthcare'].includes(kind)) {
    node.innerHTML = `<div class="secret-grid">${password('credential-token','Token','编辑时留空即保留')}</div>`;
    note.textContent = '新增必须填写 Token；编辑时留空表示保留原 Token。';
  } else if (kind === 'eumetsat') {
    node.innerHTML = `<div class="secret-grid">${password('credential-consumer-key','Consumer Key','从 EUMETSAT API Key Management 复制')}${password('credential-consumer-secret','Consumer Secret','从 EUMETSAT API Key Management 复制')}</div>`;
    note.textContent = '这里只填写官网 User credentials 区域的 Consumer Key + Consumer Secret。API Token 无需填写，下载器会自动申请并刷新；编辑时两项都留空表示保留原凭据。';
  } else {
    node.innerHTML = `<div class="secret-grid"><label>用户名<input id="credential-username" autocomplete="off" placeholder="编辑时留空即保留"></label>${password('credential-password','密码')}</div>`;
    note.textContent = '新增必须填写账号凭据；编辑时留空表示保留原凭据。';
  }
  renderCredentialGuide();
}

function renderCredentialGuide() {
  const kind = $('#credential-kind').value;
  const guide = credentialGuides[kind];
  const node = $('#credential-guide');
  if (!guide) {
    node.replaceChildren();
    node.classList.add('hidden');
    return;
  }
  node.classList.remove('hidden');
  node.innerHTML = `
    <div class="credential-guide-head">
      <div><strong>${esc(guide.title)}</strong><p>${esc(guide.summary)}</p></div>
      <a href="${esc(guide.url)}" target="_blank" rel="noopener noreferrer">${esc(guide.link)} ↗</a>
    </div>
    <details>
      <summary>查看获取教程</summary>
      <ol>${guide.steps.map(step => `<li>${esc(step)}</li>`).join('')}</ol>
      <p class="credential-guide-note">${esc(guide.note)}</p>
    </details>`;
}

function rebuildAccessSelects() {
  const credentialSelect = $('#account-credential');
  const previousCredential = credentialSelect.value;
  credentialSelect.replaceChildren(new Option('匿名 / 无凭据', ''));
  const required = providerKinds[$('#account-source').value] || '';
  for (const item of state.access.credentials.filter(item => required && item.kind === required)) {
    credentialSelect.add(new Option(`${item.alias} · ${accessKindLabels[item.kind] || item.kind}`, String(item.id)));
  }
  if ([...credentialSelect.options].some(option => option.value === previousCredential)) credentialSelect.value = previousCredential;

  const proxySelect = $('#account-proxy');
  const previousProxy = proxySelect.value;
  proxySelect.replaceChildren(new Option('选择代理', ''));
  for (const item of state.access.proxies) proxySelect.add(new Option(`${item.alias} · ${item.endpoint_hint}`, String(item.id)));
  if ([...proxySelect.options].some(option => option.value === previousProxy)) proxySelect.value = previousProxy;
  $('#account-proxy-row').classList.toggle('hidden', $('#account-proxy-mode').value !== 'fixed');
}

function openPoolForm(kind, item = null) {
  $(`#${kind}-form`).classList.remove('hidden');
  $(`#${kind}-error`).textContent = '';
  if (kind === 'credential') {
    $('#credential-id').value = item?.id || '';
    $('#credential-alias').value = item?.alias || '';
    $('#credential-kind').value = item?.kind || 'laads';
    $('#credential-kind').disabled = Boolean(item);
    $('#credential-priority').value = item?.priority ?? 0;
    $('#credential-enabled').checked = item?.enabled ?? true;
    credentialFields();
  } else if (kind === 'proxy') {
    $('#proxy-id').value = item?.id || '';
    $('#proxy-alias').value = item?.alias || '';
    $('#proxy-url').value = '';
    $('#proxy-providers').value = item?.providers?.length ? item.providers.join(',') : '*';
    $('#proxy-priority').value = item?.priority ?? 0;
    $('#proxy-enabled').checked = item?.enabled ?? true;
  } else {
    $('#account-id').value = item?.id || '';
    $('#account-alias').value = item?.alias || '';
    $('#account-source').value = item?.source_id || 'goes';
    $('#account-proxy-mode').value = item?.proxy_mode || 'direct';
    $('#account-priority').value = item?.priority ?? 0;
    $('#account-enabled').checked = item?.enabled ?? true;
    rebuildAccessSelects();
    $('#account-credential').value = item?.credential_id ? String(item.credential_id) : '';
    $('#account-proxy').value = item?.proxy_id ? String(item.proxy_id) : '';
  }
}

function closePoolForm(kind) {
  $(`#${kind}-form`).classList.add('hidden');
  if (kind === 'credential') $('#credential-kind').disabled = false;
}

async function saveCredential(event) {
  event.preventDefault();
  const kind = $('#credential-kind').value;
  const currentId = Number($('#credential-id').value || 0);
  const alias = $('#credential-alias').value.trim();
  if (duplicateAlias(state.access.credentials, alias, currentId)) {
    $('#credential-error').textContent = `凭据别名“${alias}”已存在，请编辑已有凭据或换一个别名。`;
    return;
  }
  const secret = {};
  if (['laads','earthdata','earthcare'].includes(kind)) secret.token = $('#credential-token')?.value || '';
  else if (kind === 'eumetsat') {
    secret.consumer_key = $('#credential-consumer-key')?.value || '';
    secret.consumer_secret = $('#credential-consumer-secret')?.value || '';
  } else {
    secret.username = $('#credential-username')?.value || '';
    secret.password = $('#credential-password')?.value || '';
  }
  Object.keys(secret).forEach(key => { if (!secret[key]) delete secret[key]; });
  const payload = {
    alias, kind, secret,
    priority: Number($('#credential-priority').value || 0), enabled: $('#credential-enabled').checked,
  };
  if (currentId) payload.id = currentId;
  try {
    await json('/api/v1/access/credentials', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify(payload)});
    closePoolForm('credential');
    await loadAccessPools();
  } catch (error) { $('#credential-error').textContent = error.message; }
}

async function saveProxy(event) {
  event.preventDefault();
  const currentId = Number($('#proxy-id').value || 0);
  const alias = $('#proxy-alias').value.trim();
  if (duplicateAlias(state.access.proxies, alias, currentId)) {
    $('#proxy-error').textContent = `代理别名“${alias}”已存在，请编辑已有代理或换一个别名。`;
    return;
  }
  const providers = $('#proxy-providers').value.split(',').map(value => value.trim()).filter(Boolean);
  const payload = {
    alias, url: $('#proxy-url').value.trim(), providers,
    priority: Number($('#proxy-priority').value || 0), enabled: $('#proxy-enabled').checked,
  };
  if (currentId) payload.id = currentId;
  try {
    await json('/api/v1/access/proxies', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify(payload)});
    closePoolForm('proxy');
    await loadAccessPools();
  } catch (error) { $('#proxy-error').textContent = error.message; }
}

async function saveAccount(event) {
  event.preventDefault();
  const currentId = Number($('#account-id').value || 0);
  const alias = $('#account-alias').value.trim();
  if (duplicateAlias(state.access.accounts, alias, currentId)) {
    $('#account-error').textContent = `账号别名“${alias}”已存在，请编辑已有账号或换一个别名。`;
    return;
  }
  const credential = $('#account-credential').value;
  const proxy = $('#account-proxy').value;
  const payload = {
    alias, source_id: $('#account-source').value,
    credential_id: credential ? Number(credential) : null, proxy_mode: $('#account-proxy-mode').value,
    proxy_id: proxy ? Number(proxy) : null, priority: Number($('#account-priority').value || 0), enabled: $('#account-enabled').checked,
  };
  if (currentId) payload.id = currentId;
  try {
    await json('/api/v1/access/accounts', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify(payload)});
    closePoolForm('account');
    await loadAccessPools();
  } catch (error) { $('#account-error').textContent = error.message; }
}

function duplicateAlias(items, alias, currentId = 0) {
  const normalized = String(alias || '').trim().toLocaleLowerCase();
  if (!normalized) return false;
  return items.some(item => item.id !== currentId && String(item.alias || '').trim().toLocaleLowerCase() === normalized);
}

async function deletePoolItem(kind, id) {
  if (!confirm('确认删除这条配置？关联账号可能会立即失去可用路由。')) return;
  try {
    const resource = kind === 'proxy' ? 'proxies' : `${kind}s`;
    await json(`/api/v1/access/${resource}/${id}`, {method:'DELETE'});
    await loadAccessPools();
  } catch (error) { alert(error.message); }
}

async function loginAccess(event) {
  event.preventDefault();
  $('#access-login-error').textContent = '';
  try {
    await json('/api/v1/access/login', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({username:$('#access-username').value, password:$('#access-password').value})});
    $('#access-password').value = '';
    await loadAccess();
  } catch (error) { $('#access-login-error').textContent = error.message; }
}

async function logoutAccess() {
  try { await json('/api/v1/access/logout', {method:'POST'}); } catch {}
  state.access.user = null;
  await loadAccess();
}

async function health() {
  const dot = $('#service-dot');
  const label = $('#service-label');
  try {
    await json('/healthz');
    dot.classList.add('ok');
    label.textContent = '服务正常';
  } catch {
    dot.classList.remove('ok');
    label.textContent = '服务异常';
  }
}

function bind() {
  $('.sidebar-toggle').addEventListener('click', () => setSidebar(document.body.classList.contains('sidebar-collapsed'), true));
  document.querySelectorAll('.nav-item').forEach(node => node.addEventListener('click', () => {
    setView(node.dataset.view);
    if (node.dataset.view === 'access-view') loadAccess();
    if (node.dataset.view === 'monitor-view') loadMonitor(true);
  }));
  sourceEl.addEventListener('change', () => { persistFilters(); loadCatalog(); });
  satelliteEl.addEventListener('change', () => { rebuildFilters('satellite'); persistFilters(); });
  instrumentEl.addEventListener('change', () => { rebuildFilters('instrument'); persistFilters(); });
  levelEl.addEventListener('change', () => { rebuildFilters('level'); persistFilters(); });
  productEl.addEventListener('change', updateSelectedProduct);
  yearEl.addEventListener('change', () => { state.dayState.clear(); closeDetail(); renderCalendar(); persistFilters(); loadYear(true); });
  timezoneEl.addEventListener('change', () => {
    state.displayZone = timezoneEl.value;
    localStorage.setItem('satellite-data-zone', state.displayZone);
    closeDetail();
    renderCalendar();
    if (state.monitor.payload) renderMonitor();
  });
  $('#reset-filters').addEventListener('click', resetFilters);
  $('#query-month').addEventListener('click', queryMonth);
  $('#clear-calendar').addEventListener('click', () => loadYear(true));
  $('#close-detail').addEventListener('click', closeDetail);
  $('#refresh-sources').addEventListener('click', loadSources);
  $('#refresh-monitor').addEventListener('click', () => loadMonitor(true));
  $('#monitor-window-tabs').addEventListener('click', event => {
    const button = event.target.closest('[data-monitor-window]');
    if (!button) return;
    state.monitor.window = button.dataset.monitorWindow;
    localStorage.setItem('satellite-data-monitor-window', state.monitor.window);
    loadMonitor(true);
  });
  $('#monitor-links').addEventListener('click', event => {
    const card = event.target.closest('[data-monitor-source]');
    if (!card) return;
    state.monitor.selected = card.dataset.monitorSource;
    localStorage.setItem('satellite-data-monitor-source', state.monitor.selected);
    renderMonitor();
  });
  $('#access-login-form').addEventListener('submit', loginAccess);
  $('#refresh-access').addEventListener('click', loadAccess);
  $('#logout-access').addEventListener('click', logoutAccess);
  document.querySelectorAll('[data-pool-new]').forEach(node => node.addEventListener('click', () => openPoolForm(node.dataset.poolNew)));
  document.querySelectorAll('[data-form-cancel]').forEach(node => node.addEventListener('click', () => closePoolForm(node.dataset.formCancel)));
  $('#credential-kind').addEventListener('change', credentialFields);
  $('#account-source').addEventListener('change', rebuildAccessSelects);
  $('#account-proxy-mode').addEventListener('change', rebuildAccessSelects);
  $('#credential-form').addEventListener('submit', saveCredential);
  $('#proxy-form').addEventListener('submit', saveProxy);
  $('#account-form').addEventListener('submit', saveAccount);
  $('#credential-list').addEventListener('click', event => {
    const edit = event.target.closest('[data-edit-credential]');
    const remove = event.target.closest('[data-delete-credential]');
    if (edit) openPoolForm('credential', state.access.credentials.find(item => item.id === Number(edit.dataset.editCredential)));
    if (remove) deletePoolItem('credential', Number(remove.dataset.deleteCredential));
  });
  $('#proxy-list').addEventListener('click', event => {
    const edit = event.target.closest('[data-edit-proxy]');
    const remove = event.target.closest('[data-delete-proxy]');
    if (edit) openPoolForm('proxy', state.access.proxies.find(item => item.id === Number(edit.dataset.editProxy)));
    if (remove) deletePoolItem('proxy', Number(remove.dataset.deleteProxy));
  });
  $('#account-list').addEventListener('click', event => {
    const edit = event.target.closest('[data-edit-account]');
    const remove = event.target.closest('[data-delete-account]');
    if (edit) openPoolForm('account', state.access.accounts.find(item => item.id === Number(edit.dataset.editAccount)));
    if (remove) deletePoolItem('account', Number(remove.dataset.deleteAccount));
  });
}

async function boot() {
  setSidebar(localStorage.getItem(SIDEBAR_KEY) === '1');
  timezoneEl.value = state.displayZone;
  fillYears();
  fillMonths();
  bind();
  await health();
  await loadSources();
}

boot();

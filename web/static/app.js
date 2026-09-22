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
  displayZone: localStorage.getItem('satellite-data-zone') === 'BJT' ? 'BJT' : 'UTC',
};

const SIDEBAR_KEY = 'satellite-data-sidebar-expanded';
const FILTER_KEY = 'satellite-data-filters-v1';
const tierLabels = {anonymous: '匿名直连', token: '登录 / Token', special: '特殊访问'};

async function json(url) {
  const response = await fetch(url, {headers: {'Accept': 'application/json'}});
  let payload = {};
  try { payload = await response.json(); } catch {}
  if (!response.ok) throw new Error(payload.error || `HTTP ${response.status}`);
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

function rebuildFilters(changed = 'source') {
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
  updateSelectedProduct();
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
  $('#product-meta').textContent = metadata.join(' · ');
  persistFilters();
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
  if (!item) return `${day}\n待查询 · 点击读取官方目录`;
  if (item.status === 'processing') return `${day}\n查询中…`;
  if (item.status === 'error') return `${day}\n查询错误\n${item.error || ''}`;
  if (item.status === 'official-missing') return `${day}\n上游无数据`;
  if (item.status === 'available') {
    const files = item.granules.reduce((sum, granule) => sum + (granule.assets?.length || 0), 0);
    return `${day}\n上游有数据\n${item.granules.length} 个 Granule · ${files} 个资产`;
  }
  return day;
}

function dayBounds(day) {
  const base = Date.parse(`${day}T00:00:00Z`);
  const offset = state.displayZone === 'BJT' ? 8 * 3600 * 1000 : 0;
  const start = new Date(base - offset);
  const end = new Date(base + 24 * 3600 * 1000 - offset);
  return [start.toISOString(), end.toISOString()];
}

async function queryRange(startDay, endDayExclusive, showDetailDay = '') {
  const collection = state.selected;
  if (!collection) return [];
  const [start] = dayBounds(startDay);
  const [, end] = dayBounds(new Date(Date.parse(`${endDayExclusive}T00:00:00Z`) - 24 * 3600 * 1000).toISOString().slice(0, 10));
  const params = new URLSearchParams({
    source: sourceEl.value,
    collection: collection.id,
    start,
    end,
  });
  const payload = await json('/api/v1/search?' + params);
  const buckets = new Map();
  for (const granule of payload.granules || []) {
    const day = displayDay(granule.start);
    if (!buckets.has(day)) buckets.set(day, []);
    buckets.get(day).push(granule);
  }
  for (let cursor = new Date(`${startDay}T00:00:00Z`); cursor < new Date(`${endDayExclusive}T00:00:00Z`); cursor.setUTCDate(cursor.getUTCDate() + 1)) {
    const day = cursor.toISOString().slice(0, 10);
    const granules = buckets.get(day) || [];
    state.dayState.set(day, {status: granules.length ? 'available' : 'official-missing', granules});
    updateCell(day);
  }
  if (showDetailDay) openDetail(showDetailDay);
  return payload.granules || [];
}

async function queryDay(day) {
  if (!state.selected) return;
  state.dayState.set(day, {status: 'processing', granules: []});
  updateCell(day);
  const endDate = new Date(`${day}T00:00:00Z`);
  endDate.setUTCDate(endDate.getUTCDate() + 1);
  const endDay = endDate.toISOString().slice(0, 10);
  try {
    await queryRange(day, endDay, day);
  } catch (error) {
    state.dayState.set(day, {status: 'error', error: error.message, granules: []});
    updateCell(day);
    openDetail(day);
  }
}

async function queryMonth() {
  if (!state.selected) return;
  const year = Number(yearEl.value);
  const month = Number(monthEl.value || 0);
  const monthStart = new Date(Date.UTC(year, month, 1));
  const monthEnd = new Date(Date.UTC(year, month + 1, 1));
  for (let cursor = new Date(monthStart); cursor < monthEnd; cursor.setUTCDate(cursor.getUTCDate() + 7)) {
    const chunkEnd = new Date(Math.min(monthEnd.getTime(), cursor.getTime() + 7 * 86400000));
    const startDay = cursor.toISOString().slice(0, 10);
    const endDay = chunkEnd.toISOString().slice(0, 10);
    for (let day = new Date(cursor); day < chunkEnd; day.setUTCDate(day.getUTCDate() + 1)) {
      const key = day.toISOString().slice(0, 10);
      state.dayState.set(key, {status: 'processing', granules: []});
      updateCell(key);
    }
    try {
      await queryRange(startDay, endDay);
    } catch (error) {
      for (let day = new Date(cursor); day < chunkEnd; day.setUTCDate(day.getUTCDate() + 1)) {
        const key = day.toISOString().slice(0, 10);
        state.dayState.set(key, {status: 'error', error: error.message, granules: []});
        updateCell(key);
      }
      break;
    }
  }
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

function openDetail(day) {
  const item = state.dayState.get(day);
  detailEl.classList.remove('hidden');
  $('#detail-day').textContent = `${day} · ${state.displayZone}`;
  if (!item) {
    $('#detail-summary').textContent = '待查询';
    $('#timeline').replaceChildren();
    $('#granule-list').replaceChildren();
    return;
  }
  if (item.status === 'error') {
    $('#detail-summary').textContent = `查询错误 · ${item.error || ''}`;
    $('#timeline').replaceChildren();
    $('#granule-list').replaceChildren();
    return;
  }
  const granules = item.granules || [];
  const files = granules.reduce((sum, granule) => sum + (granule.assets?.length || 0), 0);
  $('#detail-summary').textContent = granules.length ? `${granules.length} 个 Granule · ${files} 个资产` : '上游无数据';
  renderTimeline(granules);
  renderGranules(granules);
}

function renderTimeline(granules) {
  const timeline = $('#timeline');
  timeline.replaceChildren();
  for (const granule of granules) {
    const [hour, minute, second] = displayTime(granule.start).split(':').map(Number);
    const pos = (hour * 3600 + minute * 60 + second) / 86400 * 100;
    const marker = document.createElement('i');
    marker.className = 'event-slot';
    marker.style.left = `${Math.max(0, Math.min(100, pos)).toFixed(4)}%`;
    marker.title = `${displayTime(granule.start)} · ${granule.assets?.length || 0} assets`;
    timeline.append(marker);
  }
}

function renderGranules(granules) {
  const list = $('#granule-list');
  list.replaceChildren();
  for (const granule of granules.slice(0, 80)) {
    const first = granule.assets?.[0];
    const row = document.createElement('div');
    row.className = 'granule-row';
    row.innerHTML = `<time>${esc(displayTime(granule.start))}</time><span>${granule.assets?.length || 0} 文件</span><code title="${esc(first?.name || granule.id)}">${esc(first?.name || granule.id)}</code>`;
    list.append(row);
  }
  if (granules.length > 80) {
    const row = document.createElement('div');
    row.className = 'granule-row';
    row.textContent = `另有 ${granules.length - 80} 个 Granule 未展开显示`;
    list.append(row);
  }
}

function closeDetail() {
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
    const payload = await json('/api/v1/catalog?source=' + encodeURIComponent(source));
    state.collections = payload.collections || [];
    const saved = readFilters();
    rebuildFilters('source');
    if (saved.source === source) {
      restoreSelect(satelliteEl, saved.satellite);
      rebuildFilters('satellite');
      restoreSelect(instrumentEl, saved.instrument);
      rebuildFilters('instrument');
      restoreSelect(levelEl, saved.level);
      rebuildFilters('level');
      restoreSelect(productEl, saved.product);
      updateSelectedProduct();
    }
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
  document.querySelectorAll('.nav-item').forEach(node => node.addEventListener('click', () => setView(node.dataset.view)));
  sourceEl.addEventListener('change', () => { persistFilters(); loadCatalog(); });
  satelliteEl.addEventListener('change', () => { rebuildFilters('satellite'); persistFilters(); });
  instrumentEl.addEventListener('change', () => { rebuildFilters('instrument'); persistFilters(); });
  levelEl.addEventListener('change', () => { rebuildFilters('level'); persistFilters(); });
  productEl.addEventListener('change', updateSelectedProduct);
  yearEl.addEventListener('change', () => { state.dayState.clear(); closeDetail(); renderCalendar(); persistFilters(); });
  timezoneEl.addEventListener('change', () => {
    state.displayZone = timezoneEl.value;
    localStorage.setItem('satellite-data-zone', state.displayZone);
    state.dayState.clear();
    closeDetail();
    renderCalendar();
  });
  $('#reset-filters').addEventListener('click', resetFilters);
  $('#query-month').addEventListener('click', queryMonth);
  $('#clear-calendar').addEventListener('click', () => { state.dayState.clear(); closeDetail(); renderCalendar(); });
  $('#close-detail').addEventListener('click', closeDetail);
  $('#refresh-sources').addEventListener('click', loadSources);
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

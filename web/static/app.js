const $ = selector => document.querySelector(selector);
const sourceEl = $('#source');
const satelliteEl = $('#satellite');
const instrumentEl = $('#instrument');
const levelEl = $('#level');
const areaEl = $('#area');
const resolutionEl = $('#resolution');
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
  catalogFamily: '',
  catalogSeq: 0,
  selected: null,
  dayState: new Map(),
  coverage: {
    supported: false, bands: [], yearDays: new Map(), month: null, dayData: null,
    view: 'year', time: 'all', band: 0, day: '', hour: -1,
    requestSeq: 0, fileSeq: 0, poll: 0,
  },
  indexStream: null,
  indexStreamKey: '',
  satelliteYears: new Map(),
  calendarSeq: 0,
  archiveIndex: null,
  indexConnection: 'connecting',
  yearPoll: 0,
  dayPoll: 0,
  era5: {catalog: [], selected: null, schema: null, seq: 0, catalogSeq: 0, submitting: false, checking: false, inventory: null, inventorySeq: 0},
  encyclopedia: {catalog: [], loading: 0, module: null},
  monitor: {
    window: localStorage.getItem('satellite-data-monitor-window') || '1h',
    selected: localStorage.getItem('satellite-data-monitor-source') || 'earthcare',
    payload: null,
    poll: 0,
  },
  downloads: {
    displayZone: storedDisplayZone('satellite-data-download-zone', 'UTC'),
    poll: 0, pollSeq: 0, catalog: [], source: '', loading: 0,
    cursor: '', requests: [], page: 1, total: 0, pages: 1, providers: [], stream: null,
    history: {
      displayZone: storedDisplayZone('satellite-data-download-monitor-zone', 'BJT'),
      window: localStorage.getItem('satellite-data-download-window') || '1h',
      selected: localStorage.getItem('satellite-data-download-provider') || 'all',
      payload: null, loadedAt: 0, chartKey: '',
    },
  },
  access: {user: null, credentials: [], proxies: [], accounts: []},
  displayZone: localStorage.getItem('satellite-data-zone') === 'BJT' ? 'BJT' : 'UTC',
};

const SIDEBAR_KEY = 'satellite-data-sidebar-expanded';
const FILTER_KEY = 'satellite-data-filters-v1';
const FAMILY_FILTER_KEY = 'satellite-data-family-filters-v1';
const CALENDAR_YEARS_KEY = 'satellite-data-calendar-years-v1';
function storedDisplayZone(key, fallback) {
  const zone = localStorage.getItem(key);
  return zone === 'UTC' || zone === 'BJT' ? zone : fallback;
}
const tierLabels = {anonymous: '匿名直连', token: '登录 / Token', special: '特殊访问'};
const statusLabels = {
  complete: '全部时次完整',
  complete_official_gap: '全部时次完整 · 官方有缺时次',
  hourly_complete: '整点完整',
  hourly_complete_official_gap: '整点完整 · 官方有缺时次',
  missing: '本地缺失',
  partial: '部分完成',
  error: '错误',
  processing: '处理中',
  indexing: '索引中',
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
    if (response.status === 401 && typeof window !== 'undefined') {
      clearCLIKey();
      window.location.replace('/login');
    }
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
  $('main').classList.toggle('calendar-wide', id === 'calendar-view');
  if (id !== 'access-view') clearCLIKey();
  if (id !== 'monitor-view') clearTimeout(state.monitor.poll);
  if (id !== 'calendar-view') stopIndexStream();
  if (id !== 'downloads-view') {
    clearTimeout(state.downloads.poll);
    if (state.downloads.stream) { state.downloads.stream.close(); state.downloads.stream = null; }
  }
  document.querySelectorAll('.view').forEach(node => node.classList.toggle('active', node.id === id));
  document.querySelectorAll('.nav-item').forEach(node => node.classList.toggle('active', node.dataset.view === id));
}

const viewPaths = Object.freeze({
  'calendar-view': '/calendar',
  'downloads-view': '/downloads',
  'era5-view': '/era5',
  'catalog-view': '/catalog',
  'monitor-view': '/monitor',
  'access-view': '/access',
});
const pathViews = new Map(Object.entries(viewPaths).map(([view, path]) => [path, view]));

function viewFromPath(pathname = location.pathname) {
  let normalized = pathname || '/';
  while (normalized.length > 1 && normalized.endsWith('/')) normalized = normalized.slice(0, -1);
  if (normalized === '/' || normalized === '/index.html') return 'calendar-view';
  if (normalized === '/sources') return 'catalog-view';
  if (normalized === '/sync') return 'downloads-view';
  if (normalized === '/cli') return 'access-view';
  return pathViews.get(normalized) || 'calendar-view';
}

function loadViewContent(id) {
  if (id === 'calendar-view' && state.selected) loadYear(true);
  if (id === 'access-view') { if (state.access.user?.role === 'admin') loadAccess(); loadCLIKeys().catch(error => { $('#cli-key-error').textContent = error.message; }); }
  if (id === 'monitor-view') loadMonitor(true);
  if (id === 'downloads-view') { initializeDownloadSubmit(); loadDownloads(true); }
  if (id === 'era5-view') { loadERA5Inventory(); if ($('[data-era5-tab="download"]').classList.contains('active')) loadERA5Catalog(); }
  if (id === 'catalog-view') { renderSources(); loadEncyclopedia(); }
}

function navigateView(id, historyMode = 'none') {
  if (!viewPaths[id]) id = 'calendar-view';
  setView(id);
  const path = viewPaths[id];
  if (historyMode === 'push' && location.pathname !== path) history.pushState({view: id}, '', path);
  else if (historyMode === 'replace' && location.pathname !== path) history.replaceState({view: id}, '', path);
  loadViewContent(id);
}

function selectedSource() {
  const product = selectedCollection();
  if (product?.source_id) return state.sources.find(item => item.id === product.source_id);
  return familySources(sourceEl.value)[0] || null;
}

function selectedCollection() {
  return state.collections.find(item => item.id === productEl.value) || null;
}

function uniqueSorted(values) {
  return [...new Set(values.filter(Boolean))].sort((a, b) => String(a).localeCompare(String(b), undefined, {numeric: true}));
}

function collectionSatellites(product) {
  return String(product?.satellite || '').split(/\s*(?:\/|\+)\s*/).filter(Boolean);
}

function catalogLevel(source, value) {
  const level = String(value || '').trim();
  const parts = level.split('/').map(part => part.trim().match(/^(?:L|LEVEL\s*)?([0-4](?:\.[0-9]+)?[A-Z]*)$/i));
  if (parts.every(Boolean)) return parts.map(part => 'L' + part[1].toUpperCase()).join('/');
  return /^(aux|orbit)$/i.test(level) ? level.toUpperCase() : level;
}

function normalizeCatalogProduct(product) {
  const metadata = {...(product.metadata || {})};
  let instrument = String(product.instrument || '').trim();
  let level = catalogLevel(product.source_id, product.level);
  if (/^goes\d+$/.test(product.source_id) && instrument === 'SEIS') instrument = 'SEISS';
  if (product.source_id === 'calipso' && /^CAL_LID_L15[-_]/.test(product.data_type)) level = 'L1.5';
  if (product.source_id === 'earthcare') {
    if (product.data_type?.startsWith('AUX_')) { instrument = 'Auxiliary'; level ||= 'AUX'; }
    if (product.data_type === 'MPL_ORBSCT') { instrument = 'Orbit'; level ||= 'ORBIT'; }
  }
  return {...product, satellite: String(product.satellite || '').trim(), instrument, level, metadata};
}

function productOptionLabel(product) {
  const type = product.metadata?.product_type;
  const parts = [product.level, type, product.product];
  const version = product.metadata?.version;
  if (version && !product.product.endsWith(version)) parts.push(version);
  if (product.metadata?.spatial_resolution) parts.push(product.metadata.spatial_resolution);
  const satellites = collectionSatellites(product);
  if (satellites.length > 1) parts.push(satellites.join(' + ') + ' 联合产品');
  else if (!satelliteEl.value) parts.push(product.satellite);
  return [...new Set(parts.filter(Boolean))].join(' · ');
}

const areaLabels = {DISK:'全圆盘', CONUS:'CONUS', MESO:'中尺度', SWATH:'条带', GLOBAL:'全球', SUBSET:'子集', JAPAN:'日本', TARGET:'目标区', SOLAR_DISK:'太阳圆盘'};
function resolutionValues(value) {
  const parts = String(value || '').split(/\s*[/,;]\s*/).filter(Boolean);
  const unit = parts.at(-1)?.match(/(?:km|m|degrees?|deg\.?|arc[- ]seconds?)$/i)?.[0];
  return uniqueSorted(parts.map(part => {
    if (/^\d+(?:\.\d+)?$/.test(part) && unit) part += unit;
    const match = part.match(/^(\d+(?:\.\d+)?(?:\s*[x×]\s*\d+(?:\.\d+)?)?)\s*(km|m|degrees?|deg\.?|arc[- ]seconds?)$/i);
    if (!match) return part;
    return match[1].replace(/\s+/g, '') + ' ' + match[2].toLowerCase().replace(/^degrees?$|^deg\.?$/, 'deg').replace(/^arc[- ]seconds?$/, 'arcsec');
  }));
}

function metadataFilterValues(product, field) {
  const value = field === 'level' ? product.level : field === 'area_type' ? product.metadata?.area_type : product.metadata?.spatial_resolution;
  if (value) return field === 'resolution' ? resolutionValues(value) : [value];
  return ['__' + (product.metadata?.[field + '_status'] || 'catalog_not_provided')];
}

function fillMetadataSelect(node, products, field) {
  const values = uniqueSorted(products.flatMap(product => metadataFilterValues(product, field)));
  const previous = node.value;
  node.replaceChildren();
  if (values.length > 1) node.add(new Option('全部', ''));
  for (const value of values) {
    const label = value.startsWith('__') ? metadataStatusLabels[value.slice(2)] || '目录未提供' : areaLabels[value] || value;
    const option = new Option(label, value);
    option.title = value.startsWith('__') ? label : value;
    node.add(option);
  }
  if ([...node.options].some(option => option.value === previous)) node.value = previous;
  node.disabled = values.length === 1 && values[0] === '__not_applicable';
}

function calendarProductLabel(product) {
  const satellites = collectionSatellites(product);
  const satellite = satellites.length > 1 ? `${satellites.join(' + ')}（联合产品）` : product.satellite;
  return [satellite, product.instrument, product.level, product.metadata?.product_type || product.product].filter(Boolean).join(' · ');
}

function calendarProductType(product) {
  let type = product.product;
  if (product.source_id === 'modis') type = type.replace(/^(MOD|MYD)/, 'MODIS');
  if (product.source_id === 'viirs') type = type.replace(/^(VNP|VJ1|VJ2)/, 'VIIRS');
  if (product.source_id === 'modis') type = type.replace(/_MODIS_(Terra|Aqua)$/, '_MODIS');
  if (product.source_id === 'viirs') type = type.replace(/_VIIRS_(NOAA20|NOAA21|SNPP)$/, '_VIIRS');
  return JSON.stringify([type, product.instrument, catalogLevel(product.source_id, product.level), product.metadata?.version || '', product.metadata?.area_type || '', resolutionValues(product.metadata?.spatial_resolution).join('|')]);
}

function selectedCalendarProducts() {
  const selected = state.selected;
  if (!selected || satelliteEl.value || collectionSatellites(selected).length > 1) return selected ? [selected] : [];
  const type = calendarProductType(selected);
  const bySatellite = new Map();
  for (const product of filteredCollections(5)) {
    if (collectionSatellites(product).length !== 1 || calendarProductType(product) !== type) continue;
    if (!bySatellite.has(product.satellite)) bySatellite.set(product.satellite, product);
  }
  bySatellite.delete(selected.satellite);
  return [selected, ...bySatellite.values()];
}

const sourceFamilyLabels = Object.freeze({
  goes: 'GOES',
  himawari: 'Himawari',
  modis: 'MODIS',
  viirs: 'VIIRS',
  earthcare: 'EarthCARE',
  cloudsat: 'CloudSat',
  calipso: 'CALIPSO',
  mtg: 'MTG',
  era5: 'ERA5',
});
const sourceFamilyOrder = Object.keys(sourceFamilyLabels);
const sourceFamilyHints = Object.freeze({
  goes: ['goes16', 'goes17', 'goes18', 'goes19'],
  himawari: ['himawari8', 'himawari9'],
});

function sourceFamilyID(sourceID) {
  const id = String(sourceID || '').toLowerCase();
  if (/^goes\d+$/.test(id)) return 'goes';
  if (/^himawari\d+$/.test(id)) return 'himawari';
  return id;
}

function familySources(familyID) {
  return state.sources.filter(source => sourceFamilyID(source.id) === familyID);
}

function sourceFamilies() {
  const grouped = new Map();
  for (const source of state.sources) {
    if (source.id === 'era5') continue;
    const id = sourceFamilyID(source.id);
    if (!grouped.has(id)) grouped.set(id, []);
    grouped.get(id).push(source);
  }
  return [...grouped.entries()].map(([id, sources]) => ({
    id,
    sources,
    label: sourceFamilyLabels[id] || sources[0]?.name || id,
  })).sort((a, b) => {
    const ai = sourceFamilyOrder.indexOf(a.id);
    const bi = sourceFamilyOrder.indexOf(b.id);
    if (ai >= 0 || bi >= 0) return (ai < 0 ? 999 : ai) - (bi < 0 ? 999 : bi);
    return a.label.localeCompare(b.label, undefined, {numeric: true});
  });
}

function savedFamily(saved = readFilters()) {
  return saved.family || sourceFamilyID(saved.source);
}

function hintedFamilySourceIDs(familyID) {
  return sourceFamilyHints[familyID] || [familyID];
}

function sameSourceSet(a, b) {
  return [...a].sort().join('|') === [...b].sort().join('|');
}

function familyProductsURL(sourceIDs) {
  return '/api/products?sources=' + encodeURIComponent(sourceIDs.join(','));
}

// Mission periods are not file-coverage intervals. An end is shown only for
// missions with a confirmed end of science observations.
const missionPeriods = {
  'CloudSat': {start: 2006, end: '2023-12-20'},
  'CALIPSO': {start: 2006, end: '2023-08-01'},
  'Terra': {start: 2000}, 'Aqua': {start: 2002},
  'Suomi-NPP': {start: 2011}, 'NOAA-20': {start: 2017}, 'NOAA-21': {start: 2022},
  'GOES-16': {start: 2017, status: '备用'}, 'GOES-17': {start: 2018, status: '在轨存储'},
  'GOES-18': {start: 2022}, 'GOES-19': {start: 2024},
  'Himawari-8': {start: 2015, status: '备用'}, 'Himawari-9': {start: 2022},
  'EarthCARE': {start: 2024}, 'MTG': {start: 2022},
};

function satelliteTimelineData(collections) {
  const bySatellite = new Map();
  for (const product of collections) {
    if (!product.satellite || product.satellite === 'ECMWF') continue;
    for (const name of collectionSatellites(product)) {
      const year = Number(String(product.metadata?.time_start || '').slice(0, 4));
      const found = bySatellite.get(name) || {name, filter: name, dataStart: null};
      if (year >= 1990 && year <= new Date().getUTCFullYear() && (!found.dataStart || year < found.dataStart)) found.dataStart = year;
      bySatellite.set(name, found);
    }
  }
  return [...bySatellite.values()].map(row => ({
    ...row, start: missionPeriods[row.name]?.start || row.dataStart,
    end: missionPeriods[row.name]?.end || null,
    status: missionPeriods[row.name]?.status || '',
  })).sort((a, b) => (a.start || 9999) - (b.start || 9999) || a.name.localeCompare(b.name));
}

function renderSatelliteTimelines() {
  const node = $('#satellite-timeline-rows');
  const rows = satelliteTimelineData(state.collections);
  $('#satellite-timelines').classList.toggle('hidden', rows.length === 0);
  node.replaceChildren();
  const origin = Date.UTC(1999, 0, 1);
  const now = Date.now();
  const horizon = Date.UTC(new Date().getUTCFullYear() + 1, 0, 1);
  const percent = timestamp => Math.max(0, Math.min(100, (timestamp - origin) / (horizon - origin) * 100));
  for (const row of rows) {
    const start = row.start ? Date.UTC(row.start, 0, 1) : null;
    const end = row.end ? Date.parse(row.end + 'T23:59:59Z') : now;
    const from = start === null ? 0 : percent(start);
    const to = percent(end);
    const status = row.end ? '历史归档 · END' : (row.status || '终点未确认');
    const button = document.createElement('button');
    button.type = 'button';
    button.className = `satellite-timeline${row.end ? ' ended' : ''}${row.filter === satelliteEl.value ? ' selected' : ''}`;
    button.dataset.satellite = row.filter;
    button.setAttribute('aria-label', `${row.name}，${row.start || '起点待核实'} 至 ${row.end || status}，${status}`);
    button.innerHTML = `<span class="timeline-name">${esc(row.name)}</span><span class="timeline-dates"><span>${esc(row.start || '起点待核实')}</span><span>${esc(row.end || '至今')}</span></span>
      <span class="timeline-track"><i class="timeline-fill" style="--timeline-start:${from}%;--timeline-end:${to}%"></i>${row.end ? `<i class="timeline-end" style="left:${to}%"></i>` : ''}</span>
      <span class="timeline-status">${esc(status)}</span>`;
    node.append(button);
  }
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

function filteredCollections(stage = 6) {
  return state.collections.filter(item => {
    if (stage > 0 && satelliteEl.value && !collectionSatellites(item).includes(satelliteEl.value)) return false;
    if (stage > 1 && instrumentEl.value && item.instrument !== instrumentEl.value) return false;
    if (stage > 2 && levelEl.value && !metadataFilterValues(item, 'level').includes(levelEl.value)) return false;
    if (stage > 3 && areaEl.value && !metadataFilterValues(item, 'area_type').includes(areaEl.value)) return false;
    if (stage > 4 && resolutionEl.value && !metadataFilterValues(item, 'resolution').includes(resolutionEl.value)) return false;
    return true;
  });
}

function rebuildFilters(changed = 'source', update = true) {
  const counterpart = state.selected && calendarProductType(state.selected);
  if (changed === 'source') {
    const satellites = uniqueSorted(state.collections.flatMap(collectionSatellites));
    fillSelect(satelliteEl, satellites, satellites.length > 1 ? '全部卫星' : '');
    if (satellites.length === 1) satelliteEl.value = satellites[0];
  }
  if (changed === 'source' || changed === 'satellite') {
    fillSelect(instrumentEl, uniqueSorted(filteredCollections(1).map(item => item.instrument)), '全部');
  }
  if (changed === 'source' || changed === 'satellite' || changed === 'instrument') {
    fillMetadataSelect(levelEl, filteredCollections(2), 'level');
  }
  if (['source','satellite','instrument','level'].includes(changed)) fillMetadataSelect(areaEl, filteredCollections(3), 'area_type');
  if (['source','satellite','instrument','level','area'].includes(changed)) fillMetadataSelect(resolutionEl, filteredCollections(4), 'resolution');
  const products = filteredCollections(5).sort((a, b) => {
    if (sourceEl.value !== 'mtg') return 0;
    return Number(!/^L1/i.test(a.level)) - Number(!/^L1/i.test(b.level));
  });
  const previous = productEl.value;
  productEl.replaceChildren();
  for (const item of products) {
    const option = new Option(productOptionLabel(item), item.id);
    option.title = item.metadata?.title || item.product;
    productEl.add(option);
  }
  if ([...productEl.options].some(option => option.value === previous)) productEl.value = previous;
  else if (counterpart && products.some(item => calendarProductType(item) === counterpart)) productEl.value = products.find(item => calendarProductType(item) === counterpart).id;
  else if (productEl.options.length) productEl.selectedIndex = 0;
  const reanalysis = sourceEl.value === 'era5';
  $('#satellite-filter-label').textContent = reanalysis ? '机构' : '卫星';
  $('#instrument-filter-label').textContent = reanalysis ? '数据集' : '仪器';
  $('#level-filter-label').textContent = reanalysis ? '类型' : '级别';
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
  // Month is internal month-view navigation state, no longer a header filter.
  const saved = readFilters();
  const current = String(new Date().getUTCMonth());
  monthEl.value = /^(?:[0-9]|1[01])$/.test(String(saved.month)) ? String(saved.month) : current;
}

function updateSelectedProduct() {
  stopIndexStream();
  state.calendarSeq++;
  state.satelliteYears.clear();
  state.archiveIndex = null;
  state.selected = selectedCollection();
  renderSatelliteTimelines();
  state.dayState.clear();
  state.coverage.requestSeq++;
  state.coverage.fileSeq++;
  state.coverage.supported = false;
  state.coverage.yearDays.clear();
  state.coverage.month = null;
  state.coverage.dayData = null;
  state.coverage.day = '';
  state.coverage.view = 'year';
  clearTimeout(state.coverage.poll);
  closeDetail();
  renderCoverageShell();
  renderCalendar();
  const product = state.selected;
  selectCalendarYear(product);
  renderArchiveIndex();
  const onDemand = product?.metadata?.availability_mode === 'on-demand';
  $('#query-month').disabled = onDemand;
  $('#clear-calendar').disabled = onDemand;
  for (const node of [yearEl, monthEl, timezoneEl]) node.disabled = onDemand;
  $('#calendar-help').classList.add('hidden');
  $('#year-layout').classList.toggle('hidden', onDemand);
  if (!product) {
    $('#product-title').textContent = state.collections.length ? '当前筛选无产品' : '读取产品目录…';
    $('#product-meta').textContent = '';
    $('#product-details').classList.add('hidden');
    return;
  }
  const products = selectedCalendarProducts();
  $('#product-title').textContent = products.length > 1
    ? `全部卫星 · ${product.instrument} · ${product.level} · ${product.product}`
    : calendarProductLabel(product);
  const source = selectedSource();
  const metadata = [];
  if (product.cadence) metadata.push(`时次 ${product.cadence}`);
  if (product.metadata?.spatial_resolution) metadata.push(product.metadata.spatial_resolution);
  if (product.metadata?.version) metadata.push(`版本 ${product.metadata.version}`);
  if (source) metadata.push(tierLabels[source.tier] || source.tier);
  metadata.push(product.metadata?.local_covered ? '本地已覆盖' : '本地未覆盖');
  $('#product-meta').textContent = metadata.join(' · ');
  renderProductDetails(product);
  persistFilters();
  if (onDemand) {
    clearTimeout(state.yearPoll);
    clearTimeout(state.dayPoll);
    $('#product-title').textContent = product.metadata.title || product.product;
    $('#product-meta').textContent = [product.metadata.temporal_start?.slice(0, 10), product.metadata.temporal_end?.slice(0, 10)].filter(Boolean).join(' 至 ') + ' · CDS 按需生成 · 本地未归档';
    return;
  }
  loadYear();
}

const metadataStatusLabels = {catalog_not_provided: '目录未提供', not_applicable: '不适用', per_file: '逐文件', variable: '依变量 / 波段', subsetting: '依子集'};
function metadataField(product, field, value) {
  return value || metadataStatusLabels[product.metadata?.[field + '_status']] || '目录未提供';
}

function renderProductDetails(product) {
  const metadata = product.metadata || {};
  const rows = [
    ['名称', metadata.title || product.product], ['标识', metadata.official_id || product.id],
    ['级别', metadataField(product, 'level', product.level)],
    ['名义分辨率', metadataField(product, 'resolution', metadata.spatial_resolution)],
    ['目录版本', metadataField(product, 'version', metadata.version)],
    ['范围', metadataField(product, 'area_type', metadata.area_type)],
    ['目录', metadata.catalog_source || sourceFamilyLabels[sourceFamilyID(product.source_id)] || product.source_id],
  ];
  $('#product-details').classList.remove('hidden');
  $('#product-official-title').innerHTML = rows.map(([label, value]) => `<dt>${esc(label)}</dt><dd>${esc(value)}</dd>`).join('');
  productEl.title = rows.map(([label, value]) => `${label}: ${value}`).join('\n');
}

async function prepareERA5(product, seq) {
  $('#era5-status').textContent = '读取 CDS 参数…';
  try {
    const schema = await json('/api/v1/era5/schema?collection=' + encodeURIComponent(product.id));
    if (seq !== state.era5.seq) return;
    state.era5.schema = schema;
    for (const [id, key] of [['variables', 'variable'], ['pressure', 'pressure_level'], ['type', 'product_type'], ['format', 'data_format'], ['time', 'time']]) {
      const node = $('#era5-' + id);
      const definition = schema.inputs[key]?.schema || {};
      const values = definition.enum || definition.items?.enum || [];
      node.replaceChildren();
      for (const value of values) node.add(new Option(String(value).replaceAll('_', ' '), value));
      node.disabled = !values.length;
      if (definition.default && !node.multiple) node.value = definition.default;
      if (key === 'time') for (const option of node.options) option.selected = product.cadence !== 'monthly' || option.value === '00:00';
    }
    const monthly = product.cadence === 'monthly';
    const date = $('#era5-date');
    date.type = monthly ? 'month' : 'date';
    date.min = (product.metadata.temporal_start || '').slice(0, monthly ? 7 : 10);
    date.max = (product.metadata.temporal_end || '').slice(0, monthly ? 7 : 10);
    date.value = date.max;
    $('#era5-pressure-field').classList.toggle('hidden', !schema.inputs.pressure_level);
    $('#era5-pressure').required = Boolean(schema.inputs.pressure_level);
    $('#era5-type-field').classList.toggle('hidden', !schema.inputs.product_type);
    $('#era5-time-field').classList.toggle('hidden', !schema.inputs.time);
    $('#era5-time').required = Boolean(schema.inputs.time);
    $('#era5-area').disabled = !schema.inputs.area;
    $('#era5-status').textContent = '';
    $('#era5-submit').disabled = state.era5.submitting;
    $('#era5-job-id').value ||= localStorage.getItem('satellite-data-era5-job') || '';
  } catch (error) {
    if (seq === state.era5.seq) $('#era5-status').textContent = error.message;
  }
}

function selectedValues(node) { return [...node.selectedOptions].map(option => option.value); }

async function submitERA5(event) {
  event.preventDefault();
  if (!state.era5.schema || state.era5.submitting) return;
  const schema = state.era5.schema;
  const collection = state.era5.selected?.id;
  if (!collection) return;
  const seq = state.era5.seq;
  const date = $('#era5-date').value.split('-');
  const inputs = {variable: selectedValues($('#era5-variables')), year: [date[0]], month: [date[1]]};
  if (schema.inputs.day) inputs.day = [date[2]];
  if (schema.inputs.time) inputs.time = selectedValues($('#era5-time'));
  if (schema.inputs.pressure_level) inputs.pressure_level = selectedValues($('#era5-pressure'));
  if (schema.inputs.product_type) inputs.product_type = [$('#era5-type').value];
  if (schema.inputs.data_format) inputs.data_format = $('#era5-format').value;
  if (schema.inputs.download_format) inputs.download_format = 'unarchived';
  if (schema.inputs.area) {
    inputs.area = ['north', 'west', 'south', 'east'].map(key => Number($('#era5-' + key).value));
    if (inputs.area[0] < inputs.area[2] || inputs.area[1] > inputs.area[3]) {
      $('#era5-status').textContent = '区域边界无效';
      return;
    }
  }
  state.era5.submitting = true;
  $('#era5-submit').disabled = true;
  $('#era5-status').textContent = '提交中…';
  $('#era5-result').classList.add('hidden');
  try {
    const job = await json('/api/v1/era5/requests', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({collection, inputs})});
    localStorage.setItem('satellite-data-era5-job', job.jobID);
    if (seq !== state.era5.seq) return;
    $('#era5-job-id').value = job.jobID;
    $('#era5-status').textContent = `CDS ${job.status} · ${job.jobID}`;
  } catch (error) {
    if (seq === state.era5.seq) $('#era5-status').textContent = error.status === 401 ? '需要管理员登录' : `${error.message} · 提交状态未确认，请在 CDS 请求记录中核对后再重试`;
  } finally {
    state.era5.submitting = false;
    $('#era5-submit').disabled = !state.era5.schema;
  }
}

async function refreshERA5() {
  const id = $('#era5-job-id').value.trim();
  if (!id || state.era5.checking) return;
  const seq = state.era5.seq;
  state.era5.checking = true;
  $('#era5-refresh').disabled = true;
  $('#era5-result').classList.add('hidden');
  try {
    const job = await json('/api/v1/era5/requests/' + encodeURIComponent(id));
    if (seq !== state.era5.seq) return;
    $('#era5-status').textContent = `CDS ${job.status} · ${job.jobID}`;
    localStorage.setItem('satellite-data-era5-job', job.jobID);
    if (job.status === 'successful') {
      const asset = await json('/api/v1/era5/requests/' + encodeURIComponent(id) + '/result');
      if (seq !== state.era5.seq) return;
      const node = $('#era5-result');
      node.href = asset.url;
      node.textContent = `下载结果 · ${(asset.size / 1048576).toFixed(1)} MiB`;
      node.classList.remove('hidden');
    }
  } catch (error) {
    if (seq === state.era5.seq) $('#era5-status').textContent = error.status === 401 ? '需要管理员登录' : error.message;
  } finally {
    state.era5.checking = false;
    $('#era5-refresh').disabled = false;
  }
}

async function loadERA5Catalog() {
  const ticket = ++state.era5.catalogSeq;
  $('#era5-status').textContent = '读取目录…';
  try {
    const catalog = await json('/api/products?source=era5');
    if (ticket !== state.era5.catalogSeq) return;
    state.era5.catalog = (catalog || []).map(normalizeCatalogProduct);
    const node = $('#era5-dataset');
    const previous = node.value || localStorage.getItem('satellite-data-era5-dataset');
    node.replaceChildren();
    for (const product of state.era5.catalog) {
      const option = new Option(product.display_name || product.data_type, product.product_key);
      option.title = product.data_type;
      node.add(option);
    }
    restoreSelect(node, previous);
    await selectERA5Dataset();
  } catch (error) { if (ticket === state.era5.catalogSeq) $('#era5-status').textContent = error.message; }
}

async function selectERA5Dataset() {
  const product = state.era5.catalog.find(item => item.product_key === $('#era5-dataset').value);
  const seq = ++state.era5.seq;
  state.era5.schema = null;
  state.era5.selected = product ? {id: product.product_key, cadence: product.cadence, metadata: product.metadata} : null;
  $('#era5-submit').disabled = true;
  if (!product) { $('#era5-status').textContent = '目录无数据集'; return; }
  localStorage.setItem('satellite-data-era5-dataset', product.product_key);
  $('#era5-dataset-meta').textContent = [product.resolution, product.cadence, product.metadata?.temporal_start?.slice(0,10), product.metadata?.temporal_end?.slice(0,10)].filter(Boolean).join(' · ');
  await prepareERA5(state.era5.selected, seq);
}

async function loadERA5Inventory() {
  const ticket = ++state.era5.inventorySeq;
  $('#era5-inventory-status').textContent = '读取库存…';
  try {
    const [payload, module] = await Promise.all([json('/api/v1/era5/inventory'), import('/era5-inventory.js')]);
    if (ticket !== state.era5.inventorySeq) return;
    state.era5.inventory = payload;
    module.initialize(payload);
  } catch (error) { if (ticket === state.era5.inventorySeq) $('#era5-inventory-status').textContent = error.message; }
}

function selectERA5Tab(tab) {
  document.querySelectorAll('[data-era5-tab]').forEach(node => { node.classList.toggle('active', node.dataset.era5Tab === tab); node.setAttribute('aria-selected', String(node.dataset.era5Tab === tab)); });
  $('#era5-panel').classList.toggle('hidden', tab !== 'download');
  $('#era5-inventory-panel').classList.toggle('hidden', tab !== 'inventory');
  if (tab === 'inventory') loadERA5Inventory();
  if (tab === 'download') loadERA5Catalog();
}

async function loadEncyclopedia() {
  const ticket = ++state.encyclopedia.loading;
  $('#catalog-status').textContent = '读取目录…';
  try {
    const [products, module] = await Promise.all([json('/api/products'), state.encyclopedia.module || (state.encyclopedia.module = import('/catalog.js'))]);
    if (ticket !== state.encyclopedia.loading) return;
    state.encyclopedia.catalog = (products || []).filter(product => product.source_id !== 'era5').map(normalizeCatalogProduct);
    module.initialize(state.encyclopedia.catalog);
    $('#catalog-status').textContent = '';
  } catch (error) { if (ticket === state.encyclopedia.loading) $('#catalog-status').textContent = error.message; }
}

function yearGridColumns(year) {
  const start = new Date(Date.UTC(year, 0, 1));
  const days = (Date.UTC(year + 1, 0, 1) - start.getTime()) / 86400000;
  return Math.ceil((start.getUTCDay() + days) / 7);
}

function fitYearCalendar() {
  const width = $('.calendar-wrap').clientWidth;
  if (!(width > 0)) return;
  const columns = yearGridColumns(Number(yearEl.value || defaultYear()));
  // Keep square cells and the original 4:1 cell/gap ratio. Narrow screens scroll.
  const cell = Math.max(12, width / (columns + (columns - 1) / 4));
  const layout = $('#year-layout');
  layout.style.setProperty('--cell', cell.toFixed(4) + 'px');
  layout.style.setProperty('--gap', (cell / 4).toFixed(4) + 'px');
}

function renderCalendar() {
  const products = selectedCalendarProducts();
  const multiple = products.length > 1;
  const heading = $('#year-product-heading');
  heading.classList.toggle('hidden', !multiple);
  heading.textContent = multiple ? calendarProductLabel(state.selected) : '';
  const calendars = $('#satellite-year-calendars');
  calendars.replaceChildren();
  if (state.coverage.supported) {
    renderCoverageYear();
    return;
  }
  const year = Number(yearEl.value || defaultYear());
  gridEl.classList.remove('coverage-year');
  $('.calendar-axis').classList.remove('coverage-axis');
  renderYearGrid(gridEl, state.dayState, year, day => queryCalendarDay(state.selected, day));
  for (const product of products.slice(1)) {
    const entry = state.satelliteYears.get(product.id);
    const section = document.createElement('section');
    section.className = 'satellite-year-calendar';
    section.dataset.product = product.id;
    const title = document.createElement('div');
    title.className = 'year-product-heading';
    title.textContent = calendarProductLabel(product) + (entry?.error ? ` · ${entry.error}` : entry?.loading ? ' · 读取中' : '');
    const axis = document.createElement('div');
    axis.className = 'calendar-axis';
    axis.setAttribute('aria-hidden', 'true');
    for (const text of ['日', '一', '二', '三', '四', '五', '六']) {
      const label = document.createElement('span');
      label.textContent = text;
      axis.append(label);
    }
    const wrap = document.createElement('div');
    wrap.className = 'calendar-wrap';
    const grid = document.createElement('div');
    grid.className = 'year-grid';
    grid.setAttribute('aria-label', `${product.satellite} 年度数据日历`);
    renderYearGrid(grid, entry?.days || new Map(), year, day => queryCalendarDay(product, day));
    if (entry) entry.grid = grid;
    if (entry) entry.heading = title;
    wrap.append(grid);
    const frame = document.createElement('div');
    frame.className = 'year-calendar';
    frame.append(axis, wrap);
    section.append(title, frame);
    calendars.append(section);
  }
  fitYearCalendar();
}

function calendarBaseStatus(item) {
  const status = item?.status || 'unknown';
  return item?.indexing && status === 'missing' && !item.official_count ? 'unknown' : status;
}

function renderYearGrid(grid, days, year, open) {
  grid.classList.remove('placeholder-grid');
  grid.replaceChildren();
  const start = new Date(Date.UTC(year, 0, 1));
  const end = new Date(Date.UTC(year, 11, 31));
  for (let i = 0; i < start.getUTCDay(); i++) {
    const spacer = document.createElement('i');
    spacer.className = 'c spacer';
    grid.append(spacer);
  }
  for (let date = new Date(start); date <= end; date.setUTCDate(date.getUTCDate() + 1)) {
    const day = date.toISOString().slice(0, 10);
    const item = days.get(day);
    const status = calendarBaseStatus(item);
    const cell = document.createElement('i');
    cell.className = `c ${status}${item?.indexing ? ' indexing' : ''}`;
    cell.dataset.day = day;
    cell.dataset.status = status;
    cell.tabIndex = 0;
    cell.setAttribute('aria-label', dayTip(day, days));
    cell.addEventListener('mouseenter', event => {
      tipEl.textContent = dayTip(day, days);
      tipEl.classList.remove('hidden');
      moveTip(event);
    });
    cell.addEventListener('mousemove', moveTip);
    cell.addEventListener('mouseleave', hideTip);
    cell.addEventListener('click', () => open(day));
    cell.addEventListener('keydown', event => {
      if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); open(day); }
    });
    grid.append(cell);
  }
}

async function queryCalendarDay(product, day) {
  if (!product) return;
  if (selectedCalendarProducts().length > 1) {
    const year = yearEl.value;
    satelliteEl.value = collectionSatellites(product)[0];
    rebuildFilters('satellite', false);
    restoreSelect(productEl, product.id);
    updateSelectedProduct();
    yearEl.value = year;
    await loadYear(true);
  }
  await queryDay(day);
}

function bandCoverageSupported(product) {
  return Boolean(product && (
    (/^goes\d+$/.test(product.source_id) && /^ABI-L1b-Rad[FMC]$/.test(product.product)) ||
    (/^himawari[89]$/.test(product.source_id) && product.product === 'AHI-L1b-FLDK')
  ));
}

function coverageBandName(band) {
  return `C${String(band).padStart(2, '0')}`;
}

// A missing band/slot is orange whenever any band has valid local data
// that day. White is reserved for a day with no valid local data at all.
// BandTotals[2] counts complete slots, while [5] counts partially covered
// slots (including days with no fully complete slot).
function coverageTotalsHaveLocal(totals) {
  return (totals || []).some(row => row[2] > 0 || row[5] > 0);
}

function coverageDayHasLocal(day) {
  return (day?.cells || []).some(cell => cell[3] > 0);
}

function coverageStatus(row, officialState, cadence, hourly = false, mode = 'grid', dayHasLocal = false) {
  if (officialState === 0 || officialState === 3) return 'unknown';
  if (officialState === 1) return 'official_missing';
  if (hourly && mode !== 'grid') return 'unknown';
  const count = row?.[hourly ? 3 : 1] || 0;
  const complete = row?.[hourly ? 4 : 2] || 0;
  if (!count) return 'official_missing';
  if (row[hourly ? 8 : 7]) return 'error';
  if (row[hourly ? 10 : 9]) return 'processing';
  if (complete === count) {
    const expected = hourly ? 24 : (cadence > 0 ? 1440 / cadence : 0);
    return expected && count < expected ? 'complete_official_gap' : 'complete';
  }
  return complete || row[hourly ? 6 : 5] || dayHasLocal ? 'partial' : 'missing';
}

function renderCoverageShell() {
  const coverage = state.coverage;
  const visible = coverage.supported;
  const onDemand = state.selected?.metadata?.availability_mode === 'on-demand';
  const tools = $('#coverage-tools');
  tools.classList.toggle('hidden', onDemand);
  $('#query-month').classList.toggle('hidden', onDemand || !visible || coverage.view !== 'month');
  tools.classList.toggle('coverage-tools-limited', !visible && !onDemand);
  tools.title = !visible && !onDemand ? '当前产品仅支持年视图' : '';
  const band = $('#coverage-band');
  band.disabled = !visible;
  if (!visible) {
    band.replaceChildren(new Option('无波段维度', '0'));
    coverage.band = 0;
    coverage.view = 'year';
    coverage.time = 'all';
  }
  document.querySelectorAll('[data-coverage-view]').forEach(button => {
    button.disabled = !visible && button.dataset.coverageView !== 'year';
  });
  document.querySelectorAll('[data-coverage-time]').forEach(button => {
    button.disabled = !visible;
  });
  $('.legend').classList.toggle('hidden', onDemand);
  $('#year-layout').classList.toggle('hidden', onDemand || (visible && coverage.view !== 'year'));
  $('#month-coverage').classList.toggle('hidden', !visible || coverage.view !== 'month');
  $('#day-coverage').classList.toggle('hidden', !visible || coverage.view !== 'day');
  $('#calendar-help').textContent = visible
    ? '绿色 完整 · 蓝色 整点完整 · 橙色 部分覆盖（包括当天其他波段有数据时的缺失波段）· 白色 整日无本地数据 · 黄色斜纹 处理中 · 红色 错误 · 灰色斜纹 官方无数据 · 灰色 待索引；呼吸表示索引中'
    : '年历依据本地持久化索引：绿色 全部完整 · 蓝色 整点完整 · 橙色 部分覆盖 · 白色 整日无本地数据；斜杠表示官方缺次，呼吸表示索引中。';
  $('#calendar-help').classList.toggle('hidden', onDemand);
  document.querySelectorAll('[data-coverage-view]').forEach(button => {
    const active = button.dataset.coverageView === coverage.view;
    button.classList.toggle('active', active);
    button.setAttribute('aria-pressed', String(active));
  });
  document.querySelectorAll('[data-coverage-time]').forEach(button => {
    const active = button.dataset.coverageTime === coverage.time;
    button.classList.toggle('active', active);
    button.setAttribute('aria-pressed', String(active));
  });
}

function applyCoverageYearRows(year, rows) {
  const changed = [];
  for (const row of rows || []) {
    const day = new Date(Date.UTC(year, 0, row[0] + 1)).toISOString().slice(0, 10);
    if (JSON.stringify(state.coverage.yearDays.get(day)) === JSON.stringify(row)) continue;
    state.coverage.yearDays.set(day, row);
    state.dayState.set(day, {
      day, status: row[1], official_count: row[2], local_ok_count: row[3],
      hourly_official_count: row[4], hourly_local_ok_count: row[5],
      official_gap_count: row[6], missing_count: row[7], error_count: row[8],
      processing_count: row[9], indexing: row[10], updated_at: row[11],
    });
    changed.push(day);
  }
  return changed;
}

function stopIndexStream() {
  if (state.indexStream) state.indexStream.close();
  for (const entry of state.satelliteYears.values()) { entry.stream?.close(); entry.stream = null; }
  state.indexStream = null;
  state.indexStreamKey = '';
}

function ensureIndexStream(product, year) {
  if (!$('#calendar-view').classList.contains('active')) return;
  const key = product + '|' + year;
  if (state.indexStream && state.indexStreamKey === key) return;
  stopIndexStream();
  const stream = new EventSource('/api/v1/index/stream?product=' + encodeURIComponent(product) + '&year=' + year + '&watch=1');
  state.indexStream = stream;
  state.indexStreamKey = key;
  state.indexConnection = 'connecting';
  renderArchiveIndex();
  stream.addEventListener('open', () => {
    if (state.indexStream !== stream) return;
    state.indexConnection = 'connected';
    renderArchiveIndex();
  });
  stream.addEventListener('error', () => {
    if (state.indexStream !== stream) return;
    state.indexConnection = 'reconnecting';
    renderArchiveIndex();
  });
  stream.addEventListener('archive', event => {
    if (state.indexStream !== stream || state.selected?.id !== product || Number(yearEl.value) !== year) return;
    let status;
    try { status = JSON.parse(event.data); } catch { return; }
    if (status.product !== product || status.year !== year) return;
    const first = !state.archiveIndex;
    state.archiveIndex = status;
    const counts = new Map((status.years || []).map(item => [String(item.year), item.files]));
    for (const option of yearEl.options) option.text = counts.has(option.value) ? `${option.value} · 本地 ${counts.get(option.value)}` : option.value;
    if (first && selectedCalendarProducts().length === 1 && !rememberedCalendarYear(product) && status.last_day && !state.selected.metadata?.last_day) {
      const preferred = status.last_day.slice(0, 4);
      if (preferred !== yearEl.value && [...yearEl.options].some(option => option.value === preferred)) {
        yearEl.value = preferred;
        changeCalendarYear(false);
        return;
      }
    }
    renderArchiveIndex();
  });
  stream.addEventListener('index', event => {
    if (state.indexStream !== stream || state.selected?.id !== product || Number(yearEl.value) !== year) return;
    let payload;
    try { payload = JSON.parse(event.data); } catch { return; }
    const changed = [];
    if (payload.coverage) {
	  $('#coverage-status').textContent = payload.pending ? `正在补全 ${payload.pending} 天的波段明细…` : '';
      if (payload.bands?.length) state.coverage.bands = payload.bands;
      changed.push(...applyCoverageYearRows(year, payload.days || []));
      for (const day of payload.removed || []) {
        state.coverage.yearDays.delete(day);
        state.dayState.delete(day);
        changed.push(day);
      }
    } else {
      for (const day of payload.days || []) {
        if (JSON.stringify(state.dayState.get(day.day)) === JSON.stringify(day)) continue;
        state.dayState.set(day.day, day);
        changed.push(day.day);
      }
      for (const day of payload.removed || []) {
        state.dayState.delete(day);
        changed.push(day);
      }
    }
    if (changed.length) {
      for (const day of changed) updateCell(day);
      if (state.coverage.supported && state.coverage.view === 'month') {
        const monthPrefix = year + '-' + String(Number(monthEl.value) + 1).padStart(2, '0') + '-';
        if (changed.some(day => day.startsWith(monthPrefix))) loadCoverageMonth();
      }
      if (state.coverage.supported && state.coverage.view === 'day' && changed.includes(state.coverage.day)) {
        openCoverageDay(state.coverage.day);
      } else if (!state.coverage.supported && detailEl.dataset.day && changed.includes(detailEl.dataset.day)) {
        openDay(detailEl.dataset.day, false);
      }
    }
  });
  stream.addEventListener('complete', () => {
    if (state.indexStream === stream) stopIndexStream();
  });
}

// Reuse an in-flight year response. Startup previously loaded the identical
// product/year once when selecting the catalog and again on navigation.
const coverageYearFlights = new Map();
async function loadCoverageYear(schedulePoll = true) {
  const product = state.selected.id;
  const year = Number(yearEl.value);
  const seq = ++state.coverage.requestSeq;
  $('#coverage-status').textContent = '读取年历…';
  try {
    const requestKey = product + '|' + year;
    let flight = coverageYearFlights.get(requestKey);
    if (!flight) {
      flight = json(`/api/coverage/year?product=${encodeURIComponent(product)}&year=${year}`)
        .finally(() => coverageYearFlights.delete(requestKey));
      coverageYearFlights.set(requestKey, flight);
    }
    const payload = await flight;
    if (seq !== state.coverage.requestSeq || state.selected?.id !== product || Number(yearEl.value) !== year) return;
    if (payload.v !== 1) throw new Error('不支持的年历数据版本');
    const coverage = state.coverage;
    coverage.supported = true;
    coverage.bands = payload.bands;
    coverage.yearDays = new Map();
    state.dayState.clear();
    applyCoverageYearRows(year, payload.days || []);
    const bandEl = $('#coverage-band');
    if (bandEl.options.length !== coverage.bands.length + 1 ||
        coverage.bands.some((band, i) => Number(bandEl.options[i + 1]?.value) !== band)) {
      bandEl.replaceChildren(new Option('全部波段', '0'));
      for (const band of coverage.bands) bandEl.add(new Option(coverageBandName(band), String(band)));
    }
    if (!coverage.bands.includes(coverage.band)) coverage.band = 0;
    bandEl.value = String(coverage.band);
    $('#coverage-status').textContent = payload.pending ? `正在补全 ${payload.pending} 天的波段明细…` : '';
    renderCoverageShell();
    renderCalendar();
    if (coverage.view === 'month') loadCoverageMonth();
    if (schedulePoll) ensureIndexStream(product, year);
  } catch (error) {
    if (seq === state.coverage.requestSeq) {
      $('#coverage-status').textContent = `年历读取失败 · ${error.message}`;
      $('#product-meta').textContent = `年历读取失败 · ${error.message}`;
    }
  }
}

function renderCoverageYear() {
  const coverage = state.coverage;
  const year = Number(yearEl.value || defaultYear());
  gridEl.classList.remove('placeholder-grid');
  gridEl.classList.add('coverage-year');
  $('.calendar-axis').classList.add('coverage-axis');
  const fragment = document.createDocumentFragment();
  const first = new Date(Date.UTC(year, 0, 1));
  for (let i = 0; i < first.getUTCDay(); i++) {
    const spacer = document.createElement('i');
    spacer.className = 'c spacer';
    fragment.append(spacer);
  }
  for (let date = new Date(first); date.getUTCFullYear() === year; date.setUTCDate(date.getUTCDate() + 1)) {
    const day = date.toISOString().slice(0, 10);
    const cell = document.createElement('i');
    cell.dataset.day = day;
    cell.tabIndex = 0;
    paintCoverageCell(cell, day);
    fragment.append(cell);
  }
  gridEl.replaceChildren(fragment);
  fitYearCalendar();
}

function paintCoverageCell(cell, day) {
  const coverage = state.coverage;
  const row = coverage.yearDays.get(day);
  const byBand = new Map((row?.[12] || []).map(total => [total[0], total]));
  const hasLocal = coverageTotalsHaveLocal(row?.[12]);
  const cadence = Number.parseInt(state.selected?.cadence, 10) || 0;
  cell.className = `c coverage-day${row?.[10] ? ' indexing' : ''}${day === coverage.day ? ' selected-day' : ''}`;
  cell.setAttribute('aria-label', dayTip(day));
  if (cell.children.length !== coverage.bands.length) {
    cell.replaceChildren(...coverage.bands.map(() => document.createElement('i')));
  }
  coverage.bands.forEach((band, index) => {
    // Cold legacy rows are shown immediately from day_summary, then replaced
    // with accurate per-band colors as the durable cache is warmed via SSE.
    const status = row?.[15] === false && row?.[13] === 2
      ? row[1] : coverageStatus(byBand.get(band), row?.[13] || 0, cadence, coverage.time === 'hourly', row?.[14], hasLocal);
    cell.children[index].className = `${status}${coverage.band && band !== coverage.band ? ' muted' : ''}`;
  });
}

function updateCell(day) {
  const cell = gridEl.querySelector(`[data-day="${day}"]`);
  if (!cell) return;
  if (state.coverage.supported) { paintCoverageCell(cell, day); return; }
  const status = calendarBaseStatus(state.dayState.get(day));
  cell.className = `c ${status}${state.dayState.get(day)?.indexing ? ' indexing' : ''}`;
  cell.dataset.status = status;
  cell.setAttribute('aria-label', dayTip(day));
}

function dayTip(day, days = state.dayState) {
  const item = days.get(day);
  if (!item) return `${day}\n待索引`;
  const band = days === state.dayState && state.coverage.supported ? state.coverage.band : 0;
  const row = band ? state.coverage.yearDays.get(day)?.[12]?.find(entry => entry[0] === band) : null;
  const officialState = state.coverage.supported ? state.coverage.yearDays.get(day)?.[13] || 0 :
    item.status === 'official_missing' ? 1 : item.official_count > 0 ? 2 : 0;
  const cadence = Number.parseInt(state.selected?.cadence, 10) || 0;
  const status = band ? coverageStatus(row, officialState, cadence, state.coverage.time === 'hourly', state.coverage.yearDays.get(day)?.[14],
    coverageTotalsHaveLocal(state.coverage.yearDays.get(day)?.[12])) : item.status;
  const lines = [`${day} · ${band ? coverageBandName(band) + ' · ' : ''}${statusLabels[status] || status} · ${state.displayZone}`];
  if (item.indexing) lines.push('官方索引中');
  if (item.download_count) lines.push(`已选待下载时次 ${item.download_count}`);
  lines.push(`全产品官方时次 ${item.local_ok_count || 0} / ${item.official_count || 0}`);
  if (item.hourly_official_count) lines.push(`整点官方时次 ${item.hourly_local_ok_count || 0} / ${item.hourly_official_count}`);
  if (item.missing_count) lines.push(`本地缺失 ${item.missing_count}`);
  if (item.processing_count) lines.push(`处理中 ${item.processing_count}`);
  if (item.error_count) lines.push(`错误 ${item.error_count}`);
  if (item.official_gap_count) lines.push(`固定时序官方缺时次 ${item.official_gap_count}`);
  if (item.updated_at) lines.push(`索引更新 ${String(item.updated_at).replace('T',' ').slice(0,19)} UTC`);
  if (band) {
    const hourly = state.coverage.time === 'hourly';
    lines.push(`所选波段${hourly ? '整点' : '全部时次'} ${row ? row[hourly ? 4 : 2] : 0} / ${row ? row[hourly ? 3 : 1] : 0}`);
  }
  return lines.join('\n');
}

// Startup navigation and product selection can request the same year at once.
// Single-flight avoids duplicate work and duplicate 502 errors.
const yearFlights = new Map();
function requestYear(product, year) {
  const key = product + '|' + year;
  let flight = yearFlights.get(key);
  if (!flight) {
    flight = json('/api/year?product=' + encodeURIComponent(product) + '&year=' + year)
      .finally(() => yearFlights.delete(key));
    yearFlights.set(key, flight);
  }
  return flight;
}

async function loadYear(schedulePoll = true) {
  clearTimeout(state.yearPoll);
  if (!state.selected || state.selected.metadata?.availability_mode === 'on-demand') return;
  const products = selectedCalendarProducts();
  if (products.length > 1) {
    await loadSatelliteYears(products, schedulePoll);
    return;
  }
  const streamKey = state.selected.id + '|' + Number(yearEl.value || defaultYear());
  if (state.indexStreamKey && state.indexStreamKey !== streamKey) stopIndexStream();
  if (bandCoverageSupported(state.selected)) {
    await loadCoverageYear(schedulePoll);
    return;
  }
  state.coverage.supported = false;
  renderCoverageShell();
  const product = state.selected.id;
  const year = Number(yearEl.value || defaultYear());
  try {
    const payload = await requestYear(product, year);
    if (state.selected?.id !== product || Number(yearEl.value) !== year) return;
    state.dayState.clear();
    for (const day of payload.days || []) state.dayState.set(day.day, day);
    renderCalendar();
    if (schedulePoll) ensureIndexStream(product, year);
  } catch (error) {
    $('#product-meta').textContent = `年历索引读取失败 · ${error.message}`;
  }
}

async function loadSatelliteYears(products, watch) {
  const seq = ++state.calendarSeq;
  const product = state.selected.id;
  const year = Number(yearEl.value || defaultYear());
  stopIndexStream();
  state.satelliteYears.clear();
  state.coverage.supported = false;
  state.coverage.view = 'year';
  for (const item of products.slice(1)) state.satelliteYears.set(item.id, {product: item, days: new Map(), loading: true});
  state.dayState.clear();
  renderCoverageShell();
  renderCalendar();
  await Promise.all(products.map(async item => {
    try {
      const payload = await requestYear(item.id, year);
      if (seq !== state.calendarSeq || state.selected?.id !== product || Number(yearEl.value) !== year) return;
      const entry = state.satelliteYears.get(item.id);
      const days = entry ? entry.days : state.dayState;
      for (const row of payload.days || []) days.set(row.day, row);
      if (entry) entry.loading = false;
    } catch (error) {
      if (seq !== state.calendarSeq) return;
      const entry = state.satelliteYears.get(item.id);
      if (entry) { entry.loading = false; entry.error = `索引读取失败 · ${error.message}`; }
      else $('#product-meta').textContent = `年历索引读取失败 · ${error.message}`;
    }
  }));
  if (seq !== state.calendarSeq || state.selected?.id !== product || Number(yearEl.value) !== year) return;
  renderCalendar();
  if (!watch || !$('#calendar-view').classList.contains('active')) return;
  ensureIndexStream(product, year);
  for (const [id, entry] of state.satelliteYears) {
    const stream = new EventSource('/api/v1/index/stream?product=' + encodeURIComponent(id) + '&year=' + year + '&watch=1');
    entry.stream = stream;
    stream.addEventListener('index', event => {
      if (seq !== state.calendarSeq || entry.stream !== stream || Number(yearEl.value) !== year) return;
      let payload;
      try { payload = JSON.parse(event.data); } catch { return; }
      if (entry.error) {
        entry.error = '';
        if (entry.heading) entry.heading.textContent = calendarProductLabel(entry.product);
      }
      const changed = [];
      for (const row of payload.days || []) {
        const day = payload.coverage ? {
          day: new Date(Date.UTC(year, 0, row[0] + 1)).toISOString().slice(0, 10),
          status: row[1], official_count: row[2], local_ok_count: row[3],
          hourly_official_count: row[4], hourly_local_ok_count: row[5], official_gap_count: row[6],
          missing_count: row[7], error_count: row[8], processing_count: row[9], indexing: row[10], updated_at: row[11],
        } : row;
        entry.days.set(day.day, day);
        changed.push(day.day);
      }
      for (const day of payload.removed || []) { entry.days.delete(day); changed.push(day); }
      for (const day of changed) {
        const cell = entry.grid?.querySelector(`[data-day="${day}"]`);
        if (!cell) continue;
        const status = calendarBaseStatus(entry.days.get(day));
        cell.className = `c ${status}${entry.days.get(day)?.indexing ? ' indexing' : ''}`;
        cell.dataset.status = status;
        cell.setAttribute('aria-label', dayTip(day, entry.days));
      }
    });
  }
}

async function queryDay(day) {
  if (state.selected?.metadata?.availability_mode === 'on-demand') return;
  if (state.coverage.supported) {
    await openCoverageDay(day);
    return;
  }
  await openDay(day, true);
}

async function queryMonth() {
  if (!state.selected || state.selected.metadata?.availability_mode === 'on-demand') return;
  const year = Number(yearEl.value);
  const month = Number(monthEl.value || 0);
  const start = new Date(Date.UTC(year, month, 1));
  const end = new Date(Date.UTC(year, month + 1, 1));
  const results = await Promise.allSettled(selectedCalendarProducts().map(product => {
    const params = new URLSearchParams({product: product.id,
      start: start.toISOString().slice(0, 10), end: end.toISOString().slice(0, 10)});
    return json('/api/index?' + params, {method: 'POST'});
  }));
  await loadYear(true);
  const failed = results.filter(result => result.status === 'rejected');
  if (failed.length) $('#product-meta').textContent = `本月索引请求失败 ${failed.length} 个产品 · ${failed[0].reason.message}`;
}

async function loadCoverageMonth() {
  const coverage = state.coverage;
  if (!coverage.supported) return;
  const product = state.selected.id;
  const year = Number(yearEl.value);
  const month = Number(monthEl.value) + 1;
  const band = coverage.band;
  const mode = coverage.time;
  const seq = ++coverage.requestSeq;
  coverage.month = null;
  $('#coverage-status').textContent = '读取月度覆盖…';
  try {
    const params = new URLSearchParams({product, year, month, band, time:mode});
    const payload = await json('/api/coverage/month?' + params);
    if (seq !== coverage.requestSeq || state.selected?.id !== product ||
        Number(yearEl.value) !== year || Number(monthEl.value) + 1 !== month || coverage.band !== band || coverage.time !== mode) return;
    if (payload.v !== 1) throw new Error('不支持的月度数据版本');
    coverage.month = payload;
    $('#coverage-status').textContent = '';
    renderCoverageMonth();
  } catch (error) {
    if (seq === coverage.requestSeq) $('#coverage-status').textContent = `月度覆盖读取失败 · ${error.message}`;
  }
}

function renderCoverageMonth() {
  const coverage = state.coverage;
  const payload = coverage.month;
  const year = Number(yearEl.value);
  const month = Number(monthEl.value) + 1;
  $('#coverage-month-title').textContent = `${year} 年 ${month} 月`;
  const grid = $('#month-grid');
  if (!payload) { grid.replaceChildren(); return; }
  const fragment = document.createDocumentFragment();
  const start = new Date(Date.UTC(year, month - 1, 1));
  for (let i = 0; i < start.getUTCDay(); i++) {
    const spacer = document.createElement('div');
    spacer.className = 'month-spacer';
    fragment.append(spacer);
  }
  const cadence = Number.parseInt(state.selected?.cadence, 10) || 0;
  for (const [dayNumber, officialState, indexing, mode, totals, hours] of payload.days) {
    const byBand = new Map(totals.map(row => [row[0], row]));
    const hasLocal = coverageTotalsHaveLocal(totals);
    const date = `${year}-${String(month).padStart(2, '0')}-${String(dayNumber).padStart(2, '0')}`;
    const button = document.createElement('button');
    button.type = 'button';
    button.className = `month-day${indexing ? ' indexing' : ''}`;
    button.dataset.day = date;
    const descriptions = coverage.bands.filter(band => !coverage.band || band === coverage.band).map(band => {
      const row = byBand.get(band);
      const status = coverageStatus(row, officialState, cadence, coverage.time === 'hourly', mode, hasLocal);
      return `${coverageBandName(band)} ${statusLabels[status] || status} ${row?.[coverage.time === 'hourly' ? 4 : 2] || 0}/${row?.[coverage.time === 'hourly' ? 3 : 1] || 0}`;
    });
    button.title = `${date} · UTC${indexing ? ' · 官方索引中' : ''}\n${descriptions.join(' · ')}`;
    button.setAttribute('aria-label', button.title);
    const ring = document.createElement('span');
    ring.className = 'month-ring';
    const segment = 360 / coverage.bands.length;
    const colors = [];
    for (const [index, band] of coverage.bands.entries()) {
      const row = byBand.get(band);
      const status = coverageStatus(row, officialState, cadence, coverage.time === 'hourly', mode, hasLocal);
      const begin = index * segment;
      const end = begin + segment - 0.6;
      if (coverage.band && coverage.band !== band) {
        colors.push(`#e4e8eb ${begin}deg ${end}deg`);
      } else {
        const color = {complete:'var(--green)', complete_official_gap:'var(--green)', partial:'var(--orange)',
          error:'var(--red)', processing:'var(--yellow)', missing:'#fff',
          official_missing:'#b5bec4', unknown:'var(--empty)'}[status] || 'var(--empty)';
        colors.push(`${color} ${begin}deg ${end}deg`);
      }
      colors.push(`#fff ${end}deg ${(index + 1) * segment}deg`);
    }
    ring.style.background = `conic-gradient(${colors.join(',')})`;
    const label = document.createElement('span');
    label.textContent = String(dayNumber);
    ring.append(label);
    button.append(ring);
    const count = document.createElement('small');
    count.className = 'month-band-count';
    if (coverage.band) {
      const selected = byBand.get(coverage.band);
      count.textContent = `${coverageBandName(coverage.band)} ${selected?.[coverage.time === 'hourly' ? 4 : 2] || 0}/${selected?.[coverage.time === 'hourly' ? 3 : 1] || 0}`;
    } else {
      const covered = totals.filter(row => row[2] > 0 || row[5] > 0).length;
      count.textContent = `${covered}/${coverage.bands.length} 波段`;
    }
    button.append(count);
    const bar = document.createElement('span');
    bar.className = 'month-hours';
    for (const [, expected, ok, partial, errors, processing] of hours) {
      const slot = document.createElement('i');
      const status = indexing && (officialState === 0 || officialState === 3) ? 'indexing' : officialState === 0 || officialState === 3 || (coverage.time === 'hourly' && mode !== 'grid') ? 'unknown' : officialState === 1 ? 'official_missing' :
        errors ? 'error' : processing ? 'processing' : !expected ? 'official_missing' :
        ok >= expected ? 'complete' : ok || partial || hasLocal ? 'partial' : 'missing';
      slot.className = `month-hour ${status}`;
      bar.append(slot);
    }
    button.append(bar);
    fragment.append(button);
  }
  grid.replaceChildren(fragment);
}

function moveCoverageMonth(delta) {
  const year = Number(yearEl.value);
  const month = Number(monthEl.value);
  const next = new Date(Date.UTC(year, month + delta, 1));
  if (![...yearEl.options].some(option => Number(option.value) === next.getUTCFullYear())) return;
  yearEl.value = String(next.getUTCFullYear());
  monthEl.value = String(next.getUTCMonth());
  if (next.getUTCFullYear() !== year) {
    state.dayState.clear();
    state.coverage.yearDays.clear();
    persistFilters();
    loadYear(true);
  } else {
    loadCoverageMonth();
  }
}

async function openCoverageDay(day) {
  const coverage = state.coverage;
  const year = Number(yearEl.value);
  const parsed = new Date(`${day}T00:00:00Z`);
  if (!/^\d{4}-\d{2}-\d{2}$/.test(day) || Number(day.slice(0, 4)) !== year ||
      !Number.isFinite(parsed.getTime()) || parsed.toISOString().slice(0, 10) !== day) return;
  const refreshing = coverage.day === day && coverage.view === 'day';
  if (!refreshing) coverage.hour = -1;
  coverage.day = day;
  coverage.dayData = null;
  coverage.view = 'day';
  coverage.fileSeq++;
  clearTimeout(coverage.poll);
  monthEl.value = String(Number(day.slice(5, 7)) - 1);
  closeDetail();
  renderCoverageShell();
  if (!refreshing) renderCalendar();
  $('#coverage-day-picker').value = day;
  $('#coverage-day-picker').min = `${year}-01-01`;
  $('#coverage-day-picker').max = `${year}-12-31`;
  $('#coverage-day-title').textContent = day;
  $('#coverage-files').replaceChildren();
  $('#coverage-day-summary').textContent = '读取覆盖率…';
  const product = state.selected.id;
  const seq = ++coverage.requestSeq;
  try {
    const payload = await json(`/api/coverage/day?product=${encodeURIComponent(product)}&day=${day}`);
    if (seq !== coverage.requestSeq || state.selected?.id !== product || coverage.day !== day) return;
    if (payload.v !== 1) throw new Error('不支持的日度数据版本');
    coverage.dayData = payload;
    coverage.cells = new Map(payload.cells.map(cell => [`${cell[0]}:${cell[1]}`, cell]));
    renderCoverageDay();
  } catch (error) {
    if (seq === coverage.requestSeq) $('#coverage-day-summary').textContent = `日度覆盖读取失败 · ${error.message}`;
  }
}

function coverageDayColumns(day) {
  const hourly = state.coverage.time === 'hourly';
  if (day.mode === 'grid' && day.cadence > 0) {
    const minutes = hourly
      ? Array.from({length: 24}, (_, hour) => hour * 60)
      : Array.from({length: (state.coverage.hour < 0 ? 1440 : 60) / day.cadence}, (_, index) => Math.max(0, state.coverage.hour) * 60 + index * day.cadence);
    const byMinute = new Map();
    day.times.forEach(([ts, minute], index) => {
      if (minute < 0) return;
      if (!byMinute.has(minute)) byMinute.set(minute, []);
      byMinute.get(minute).push(index);
    });
    return minutes.map(minute => ({
      label: `${String(Math.floor(minute / 60)).padStart(2, '0')}:${String(minute % 60).padStart(2, '0')}`,
      indices: byMinute.get(minute) || [],
    }));
  }
  return day.times.map(([ts], index) => ({
    label: ts.slice(11, 16), indices: [index],
  })).filter(column => hourly ? column.label.endsWith(':00') : state.coverage.hour < 0 || Number(column.label.slice(0, 2)) === state.coverage.hour);
}

function coverageCellState(day, indices, band, hasLocal = coverageDayHasLocal(day)) {
  const cells = indices.map(index => state.coverage.cells.get(`${index}:${band}`)).filter(Boolean);
  const total = position => cells.reduce((sum, cell) => sum + cell[position], 0);
  const official = total(2), ok = total(3), errors = total(4), processing = total(5);
  const status = day.official_state !== 2 ? day.official_state === 1 ? 'official_missing' : 'unknown' :
    !official ? 'official_missing' : errors ? 'error' : processing ? 'processing' :
    ok >= official ? 'complete' : ok || hasLocal ? 'partial' : 'missing';
  return {status, official, ok, indices: cells.map(cell => cell[0])};
}

function coverageDayHours(day) {
  if (day.mode !== 'grid' || !(day.cadence > 0) || 60 % day.cadence !== 0) return [];
  const bands = state.coverage.band ? [state.coverage.band] : state.coverage.bands;
  const hasLocal = coverageDayHasLocal(day);
  const byMinute = new Map();
  day.times.forEach(([, minute], index) => {
    if (!byMinute.has(minute)) byMinute.set(minute, []);
    byMinute.get(minute).push(index);
  });
  return Array.from({length:24}, (_, hour) => {
    const slots = Array.from({length:state.coverage.time === 'hourly' ? 1 : 60 / day.cadence}, (_, step) => {
      const minute = hour * 60 + step * day.cadence;
      const states = bands.map(band => coverageCellState(day, byMinute.get(minute) || [], band, hasLocal).status);
      const status = ['error','processing','unknown','official_missing'].find(value => states.includes(value)) ||
        (states.every(value => value === 'complete') ? 'complete' : states.some(value => value === 'complete' || value === 'partial') ? 'partial' : 'missing');
      return {minute, status};
    });
    return {hour, slots, gaps:slots.filter(slot => slot.status === 'official_missing').length};
  });
}

function renderCoverageDayOverview(day) {
  const hours = coverageDayHours(day);
  const node = $('#coverage-day-overview');
  node.classList.toggle('hidden', !hours.length);
  const hourEl = $('#coverage-hour');
  for (let hour = 0; hour < 24; hour++) hourEl.options[hour + 1].text = `${String(hour).padStart(2, '0')}:00`;
  const fragment = document.createDocumentFragment();
  for (const {hour, slots, gaps} of hours) {
    const label = String(hour).padStart(2, '0');
    if (hourEl.options[hour + 1]) hourEl.options[hour + 1].text = `${label}:00${gaps ? ` · 官方缺 ${gaps}` : ''}`;
    const button = document.createElement('button');
    button.type = 'button';
    button.className = `day-hour${state.coverage.time !== 'hourly' && hour === state.coverage.hour ? ' selected' : ''}`;
    button.dataset.hour = String(hour);
    button.disabled = state.coverage.time === 'hourly';
    const missing = slots.filter(slot => slot.status === 'official_missing').map(slot => `${label}:${String(slot.minute % 60).padStart(2, '0')}`);
    button.title = `${label}:00 UTC${missing.length ? ' · 官方缺时次 ' + missing.join(', ') : ''}`;
    button.setAttribute('aria-label', button.title);
    const title = document.createElement('span');
    title.textContent = label;
    const strip = document.createElement('span');
    strip.className = 'day-hour-slots';
    strip.style.gridTemplateColumns = `repeat(${slots.length},1fr)`;
    for (const slot of slots) {
      const cell = document.createElement('i');
      cell.className = `day-hour-slot ${slot.status}`;
      cell.title = `${label}:${String(slot.minute % 60).padStart(2, '0')} UTC · ${statusLabels[slot.status] || slot.status}`;
      strip.append(cell);
    }
    button.append(title, strip);
    button.addEventListener('click', () => {
      state.coverage.hour = hour;
      $('#coverage-files').replaceChildren();
      renderCoverageDay();
    });
    fragment.append(button);
  }
  node.replaceChildren(fragment);
  return hours.reduce((sum, hour) => sum + hour.gaps, 0);
}

function renderCoverageDay() {
  const coverage = state.coverage;
  const day = coverage.dayData;
  if (!day) return;
  const summary = $('#coverage-day-summary');
  summary.textContent = day.official_state === 0 ? (day.indexing ? '官方索引中' : '待官方索引') :
    day.official_state === 1 ? '官方未发布 · UTC 时次' :
    day.official_state === 3 ? '波段文件名无法识别 · 待核查' :
    `${day.times.length} 个实际时次 · ${day.cells.length} 个波段时次 · 时间轴为 UTC`;
  const hourEl = $('#coverage-hour');
  if (!hourEl.options.length) {
    hourEl.add(new Option('全天', '-1'));
    for (let hour = 0; hour < 24; hour++) hourEl.add(new Option(`${String(hour).padStart(2, '0')}:00`, String(hour)));
  }
  hourEl.value = String(coverage.time === 'hourly' ? -1 : coverage.hour);
  hourEl.disabled = coverage.time === 'hourly';
  const gaps = renderCoverageDayOverview(day);
  if (gaps && day.official_state === 2) summary.textContent += ` · 全天官方缺时次 ${gaps}`;
  const columns = coverageDayColumns(day);
  const matrix = $('#coverage-matrix');
  const fullDay = coverage.hour < 0 || coverage.time === 'hourly';
  matrix.classList.toggle('full-day', fullDay);
  if (fullDay) {
    renderCoverageTimelines(day, columns);
    return;
  }
  matrix.style.minWidth = '';
  matrix.style.gridTemplateColumns = `70px repeat(${Math.max(1, columns.length)},20px)`;
  const fragment = document.createDocumentFragment();
  const first = document.createElement('span');
  first.className = 'matrix-label';
  first.textContent = '波段 / UTC';
  fragment.append(first);
  for (const column of columns) {
    const header = document.createElement('span');
    header.className = 'matrix-header';
    header.textContent = column.label.slice(-2);
    header.title = `${column.label} UTC`;
    fragment.append(header);
  }
  const bands = coverage.band ? [coverage.band] : coverage.bands;
  const hasLocal = coverageDayHasLocal(day);
  for (const band of bands) {
    const label = document.createElement('span');
    label.className = 'matrix-label';
    label.textContent = coverageBandName(band);
    fragment.append(label);
    for (const column of columns) {
      const {status, official, ok, indices} = coverageCellState(day, column.indices, band, hasLocal);
      const cell = document.createElement('button');
      cell.type = 'button';
      cell.className = `matrix-cell ${status}`;
      cell.title = `${coverageBandName(band)} · ${column.label} UTC · ${statusLabels[status] || status} · ${ok}/${official} 文件`;
      cell.dataset.band = String(band);
      cell.dataset.indices = indices.join(',');
      fragment.append(cell);
    }
  }
  matrix.replaceChildren(fragment);
}

function renderCoverageTimelines(day, columns) {
  const matrix = $('#coverage-matrix');
  const labelWidth = 96, slotWidth = 3, slotGap = 1;
  matrix.style.gridTemplateColumns = `${labelWidth}px minmax(744px,1fr)`;
  const fragment = document.createDocumentFragment();
  const heading = document.createElement('span');
  heading.className = 'matrix-label';
  heading.textContent = '波段 / UTC';
  const axis = document.createElement('span');
  axis.className = 'coverage-time-axis';
  for (const hour of [0,4,8,12,16,20,24]) {
    const label = document.createElement('span');
    label.textContent = String(hour).padStart(2,'0') + ':00';
    axis.append(label);
  }
  fragment.append(heading, axis);
  const bands = state.coverage.band ? [state.coverage.band] : state.coverage.bands;
  const hasLocal = coverageDayHasLocal(day);
  const fixed = day.mode === 'grid' && day.cadence > 0 && 1440 % day.cadence === 0;
  matrix.style.minWidth = `${labelWidth + Math.max(744, fixed ? columns.length * (slotWidth + slotGap) - slotGap : 0)}px`;
  for (const band of bands) {
    const label = document.createElement('span');
    label.className = 'matrix-label';
    label.textContent = coverageBandName(band);
    const track = document.createElement('div');
    track.className = `band-track ${fixed ? 'fixed' : 'events'}${day.official_state === 1 ? ' official_missing' : day.official_state !== 2 ? ' unknown' : ''}`;
    track.dataset.band = String(band);
    if (fixed) track.style.gridTemplateColumns = `repeat(${Math.max(1, columns.length)},minmax(${slotWidth}px,1fr))`;
    for (const column of columns) {
      const {status, official, ok, indices} = coverageCellState(day, column.indices, band, hasLocal);
      const cell = document.createElement('button');
      cell.type = 'button';
      cell.className = `matrix-cell band-slot ${status}`;
      cell.dataset.band = String(band);
      cell.dataset.indices = indices.join(',');
      cell.title = `${coverageBandName(band)} · ${column.label} UTC · ${statusLabels[status] || status} · ${ok}/${official} 文件`;
      cell.setAttribute('aria-label', cell.title);
      if (fixed) {
        cell.dataset.minute = String(Number(column.label.slice(0,2)) * 60 + Number(column.label.slice(3,5)));
      } else {
        // Sparse products have markers at actual times, never synthetic gaps.
        if (!official) continue;
        const timestamp = day.times[column.indices[0]]?.[0];
        const instant = new Date(timestamp);
        const seconds = instant.getUTCHours() * 3600 + instant.getUTCMinutes() * 60 + instant.getUTCSeconds() + instant.getUTCMilliseconds() / 1000;
        cell.style.left = `${seconds / 864}%`;
        cell.dataset.timestamp = timestamp;
        cell.title = `${coverageBandName(band)} · ${timestamp} · ${statusLabels[status] || status} · ${ok}/${official} 文件`;
        cell.setAttribute('aria-label', cell.title);
      }
      track.append(cell);
    }
    fragment.append(label, track);
  }
  matrix.replaceChildren(fragment);
}

async function showCoverageFiles(indices, band) {
  const coverage = state.coverage;
  const day = coverage.dayData;
  const node = $('#coverage-files');
  const seq = ++coverage.fileSeq;
  node.replaceChildren();
  if (!day || !indices.length) {
    node.textContent = '该波段与时次没有官方文件';
    return;
  }
  const heading = document.createElement('strong');
  heading.textContent = `${coverageBandName(band)} · ${day.day}`;
  node.append(heading);
  let selector;
  if (indices.length > 1) {
    selector = document.createElement('select');
    selector.setAttribute('aria-label', '实际采集时间');
    for (const index of indices) selector.add(new Option(displayTime(day.times[index][0]), String(index)));
    node.append(selector);
  }
  const list = document.createElement('div');
  node.append(list);
  async function load(index) {
    const current = ++coverage.fileSeq;
    list.textContent = '读取文件…';
    const slot = day.times[index]?.[0];
    if (!slot) { list.textContent = '时次无效'; return; }
    try {
      const params = new URLSearchParams({product:state.selected.id, slot, band});
      const payload = await json('/api/coverage/files?' + params);
      if (current !== coverage.fileSeq || coverage.dayData !== day) return;
      const fragment = document.createDocumentFragment();
      for (const file of payload.files || []) {
        const row = document.createElement('div');
        row.className = 'coverage-file';
        const name = document.createElement('code');
        name.textContent = file.filename;
        const size = document.createElement('span');
        size.textContent = `${(file.size / 1048576).toFixed(1)} MiB`;
        const status = document.createElement('span');
        status.textContent = {ok:'齐全', error:'错误', processing:'处理', missing:'缺失'}[file.state] || file.state;
        row.append(name, size, status);
        fragment.append(row);
      }
      list.replaceChildren(fragment);
      if (!payload.files?.length) list.textContent = '该时次没有官方文件';
    } catch (error) {
      if (current === coverage.fileSeq) list.textContent = `文件读取失败 · ${error.message}`;
    }
  }
  if (selector) selector.addEventListener('change', () => load(Number(selector.value)));
  if (seq === coverage.fileSeq) load(indices[0]);
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
  detailEl.dataset.day = day;
  $('#detail-day').textContent = `${day} · ${state.displayZone}`;
  const official = item.official_day;
  const localFiles = (item.slots || []).reduce((sum, slot) => sum + (slot.local?.length || 0), 0);
  $('#detail-summary').textContent = official
    ? `${item.slots.length} 个时次 · 官方资产 ${official.asset_count} · 本地文件 ${localFiles}${item.official_gap_count ? ` · 官方缺时次 ${item.official_gap_count}` : ''}`
    : (item.indexing ? '官方索引中…' : '尚未生成官方索引');
  renderTimeline(item);
  renderGranules(item.slots || []);
  if (schedulePoll && item.indexing && !official) ensureIndexStream(product, Number(yearEl.value || defaultYear()));
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
	  if (!slot.nominal_slot) continue;
      const date = new Date(slot.nominal_slot);
      const minute = date.getUTCHours() * 60 + date.getUTCMinutes();
	  const index = Math.round(minute / interval) % total;
	  if (!by.has(index)) by.set(index, []);
	  by.get(index).push(slot);
    }
    const grid = document.createElement('div');
    grid.className = 'slots';
    grid.style.gridTemplateColumns = `repeat(${total},minmax(4px,1fr))`;
    for (let i = 0; i < total; i++) {
	  const group = by.get(i) || [];
      const cell = document.createElement('i');
      const hourly = i % (60 / interval) === 0;
	  const status = ['error','processing','partial','missing','official_missing','unknown','complete'].find(value => group.some(slot => slot.status === value))
	    || (day.indexing && !day.official_day ? 'indexing' : day.official_day ? 'official_missing' : 'unknown');
      cell.className = `slot c ${status}${hourly ? ' hourly' : ''}`;
      const minute = i * interval;
	  cell.title = `${String(Math.floor(minute / 60)).padStart(2,'0')}:${String(minute % 60).padStart(2,'0')} · ${statusLabels[status] || status}` +
	    (group.length ? ` · 实际 ${group.map(slot => displayTime(slot.slot)).join(', ')}` : '');
      grid.append(cell);
    }
    timeline.append(grid);
    return;
  }
  for (const slot of slots) {
    const [hour, minute, second] = displayTime(slot.slot).split(':').map(Number);
    const pos = (hour * 3600 + minute * 60 + second) / 86400 * 100;
    const marker = document.createElement('i');
	marker.className = `event-slot c ${slot.status}`;
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
	const nominal = slot.nominal_slot && slot.nominal_slot !== slot.slot ? ` → ${displayTime(slot.nominal_slot).slice(0, 5)}` : '';
	row.innerHTML = `<time>${esc(displayTime(slot.slot) + nominal)}</time><span>${local} / ${officialAssets} 文件</span><code title="${esc(first?.filename || '')}">${esc(first?.filename || statusLabels[slot.status] || slot.status)}</code>`;
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
  delete detailEl.dataset.day;
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
  for (const source of state.sources) {
    if (source.id === 'era5') continue;
    const row = document.createElement('article');
    row.className = 'source-row';
    row.innerHTML = `
      <strong>${esc(source.name)}</strong>
      <span class="tier ${esc(source.tier)}">${esc(tierLabels[source.tier] || source.tier)}</span>
      <p>${esc(source.description || '')}</p>
      <small>${esc(source.method || '')}</small>`;
    node.append(row);
  }
}

function renderSourceCounts() {
  const counts = {anonymous: 0, token: 0, special: 0};
  for (const source of state.sources.filter(source => source.id !== 'era5')) counts[source.tier] = (counts[source.tier] || 0) + 1;
  for (const [tier, count] of Object.entries(counts)) {
    const target = document.querySelector(`#tier-${tier}`);
    if (target) target.textContent = count;
  }
}

function applyCatalog(payload, family, saved = readFamilyFilters(family)) {
  state.catalogFamily = family;
  state.collections = (payload || []).map(normalizeCatalogProduct).map(product => ({
    id: product.product_key,
    source_id: product.source_id,
    satellite: product.satellite,
    instrument: product.instrument,
    level: catalogLevel(product.source_id, product.level),
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
      first_day: product.first_day || '',
      last_day: product.last_day || '',
    },
  }));
  setCatalogLoading(false);
  rebuildFilters('source', false);
  if (savedFamily(saved) === family) {
    let rememberedSatellite = saved.satellite;
    if (!Object.hasOwn(saved, 'satellite') && saved.source) {
      rememberedSatellite = state.sources.find(item => item.id === saved.source)?.name || '';
    }
    restoreSelect(satelliteEl, rememberedSatellite);
    rebuildFilters('satellite', false);
    restoreSelect(instrumentEl, saved.instrument);
    rebuildFilters('instrument', false);
    restoreSelect(levelEl, catalogLevel(family, saved.level));
    rebuildFilters('level', false);
    restoreSelect(areaEl, saved.area);
    rebuildFilters('area', false);
    restoreSelect(resolutionEl, saved.resolution);
    rebuildFilters('resolution', false);
    restoreSelect(productEl, saved.product);
  }
  updateSelectedProduct();
}

async function loadSources() {
  const saved = readFilters();
  const preferredFamily = savedFamily(saved) === 'era5' ? 'goes' : savedFamily(saved) || 'goes';
  const hintedSources = hintedFamilySourceIDs(preferredFamily);
  const catalogPromise = preferredFamily
    ? json(familyProductsURL(hintedSources)).catch(() => null)
    : Promise.resolve(null);
  const [payload, prefetchedCatalog] = await Promise.all([json('/api/v1/sources'), catalogPromise]);
  state.sources = payload.sources || [];
  renderSourceCounts();
  if ($('#catalog-view').classList.contains('active')) renderSources();
  const families = sourceFamilies();
  const previous = sourceEl.value;
  sourceEl.replaceChildren();
  for (const family of families) sourceEl.add(new Option(family.label, family.id));
  if (families.some(item => item.id === previous)) sourceEl.value = previous;
  if (families.some(item => item.id === preferredFamily)) sourceEl.value = preferredFamily;
  else if (families.some(item => item.id === 'goes')) sourceEl.value = 'goes';
  else if (families.length) sourceEl.value = families[0].id;
  const actualSources = familySources(sourceEl.value).map(item => item.id);
  if (sourceEl.value === preferredFamily && prefetchedCatalog && sameSourceSet(actualSources, hintedSources)) {
    applyCatalog(prefetchedCatalog, sourceEl.value, saved);
  } else {
    await loadCatalog();
  }
  if ($('#downloads-view').classList.contains('active')) initializeDownloadSubmit();
}

async function loadCatalog() {
  const family = sourceEl.value;
  const saved = readFamilyFilters(family);
  const ticket = ++state.catalogSeq;
  stopIndexStream();
  state.calendarSeq++;
  state.coverage.requestSeq++;
  state.coverage.fileSeq++;
  clearTimeout(state.yearPoll);
  clearTimeout(state.dayPoll);
  clearTimeout(state.coverage.poll);
  state.satelliteYears.clear();
  $('#satellite-year-calendars').replaceChildren();
  closeDetail();
  setCatalogLoading(true);
  $('#product-title').textContent = '读取产品目录…';
  $('#product-meta').textContent = '';
  $('#product-details').classList.add('hidden');
  state.collections = [];
  state.selected = null;
  gridEl.classList.add('placeholder-grid');
  gridEl.replaceChildren();
  const sourceIDs = familySources(family).map(item => item.id);
  if (!sourceIDs.length) { setCatalogLoading(false); return; }
  try {
    const payload = await json(familyProductsURL(sourceIDs));
    if (ticket !== state.catalogSeq || family !== sourceEl.value) return;
    applyCatalog(payload, family, saved);
  } catch (error) {
    if (ticket !== state.catalogSeq || family !== sourceEl.value) return;
    setCatalogLoading(false);
    gridEl.classList.remove('placeholder-grid');
    $('#product-title').textContent = '产品目录读取失败';
    $('#product-meta').textContent = error.message;
  }
}

function setCatalogLoading(loading) {
  $('.controls').setAttribute('aria-busy', String(loading));
  for (const node of [satelliteEl, instrumentEl, levelEl, areaEl, resolutionEl, productEl, yearEl, monthEl, timezoneEl]) node.disabled = loading;
  $('#query-month').disabled = loading;
  $('#clear-calendar').disabled = loading;
}

function restoreSelect(node, value) {
  if (value !== undefined && value !== null && [...node.options].some(option => option.value === value)) node.value = value;
}

function readFilters() {
  try { return JSON.parse(localStorage.getItem(FILTER_KEY) || '{}'); } catch { return {}; }
}

function readFamilyFilters(family) {
  try {
    const saved = JSON.parse(localStorage.getItem(FAMILY_FILTER_KEY) || '{}');
    if (saved?.[family] && typeof saved[family] === 'object') return saved[family];
  } catch {}
  const legacy = readFilters();
  return savedFamily(legacy) === family ? legacy : {};
}

function rememberedCalendarYear(product) {
  try {
    const saved = JSON.parse(localStorage.getItem(CALENDAR_YEARS_KEY) || '{}');
    const value = String(saved?.[product] || '');
    return [...yearEl.options].some(option => option.value === value) ? value : '';
  } catch { return ''; }
}

function selectCalendarYear(product) {
  for (const option of yearEl.options) option.text = option.value;
  const latest = selectedCalendarProducts().map(item => item.metadata?.last_day || '').sort().at(-1);
  const year = rememberedCalendarYear(product?.id) || latest?.slice(0, 4) || String(defaultYear());
  restoreSelect(yearEl, year);
}

function renderArchiveIndex() {
  const status = state.archiveIndex;
  const node = $('#archive-index-status');
  node.classList.toggle('hidden', !state.selected || state.selected.metadata?.availability_mode === 'on-demand');
  const connection = state.indexConnection === 'reconnecting' ? ' · 实时状态重连中' : '';
  if (!status) {
    $('#archive-scan-state').textContent = '读取索引状态…' + connection;
    $('#archive-product-state').textContent = '';
    return;
  }
  const scan = status.scan || {};
  const text = scan.state === 'running' ? `归档扫描中 · 已扫描 ${scan.observed_files || 0} 个文件` :
    scan.state === 'error' ? `归档扫描失败 · ${scan.last_error || '等待重试'}` :
    scan.state === 'ready' ? `归档扫描完成 · ${downloadTimestamp(scan.scanned_at, 'BJT')}` : '归档等待扫描';
  $('#archive-scan-state').textContent = text + connection;
  const details = [`本地已索引 ${status.files || 0} 个文件`, `${status.year} 年 ${status.year_files || 0} 个文件`, `官方已索引 ${status.official_days || 0} 天`];
  if (selectedCalendarProducts().length > 1) details.unshift(calendarProductLabel(state.selected));
  if (status.first_day) details.push(`数据日期 ${status.first_day} 至 ${status.last_day} UTC`);
  if (status.running_days) details.push(`官方索引中 ${status.running_days} 天`);
  if (status.queued_days) details.push(`排队 ${status.queued_days} 天`);
  if (status.retry_days) details.push(`退避重试 ${status.retry_days} 天`);
  if (scan.skipped_paths?.length) details.push(`跳过 ${scan.skipped_paths.length} 个不可读目录`);
  $('#archive-product-state').textContent = details.join(' · ');
}

function changeCalendarYear(manual = true) {
  if (manual && state.selected) {
    let saved = {};
    try { saved = JSON.parse(localStorage.getItem(CALENDAR_YEARS_KEY) || '{}') || {}; } catch {}
    if (typeof saved !== 'object' || Array.isArray(saved)) saved = {};
    saved[state.selected.id] = yearEl.value;
    localStorage.setItem(CALENDAR_YEARS_KEY, JSON.stringify(saved));
  }
  state.coverage.requestSeq++;
  state.coverage.fileSeq++;
  clearTimeout(state.coverage.poll);
  state.coverage.day = '';
  state.coverage.dayData = null;
  state.coverage.month = null;
  state.coverage.yearDays.clear();
  state.archiveIndex = null;
  if (state.coverage.view === 'day') state.coverage.view = 'year';
  state.dayState.clear(); closeDetail(); renderCoverageShell(); renderCalendar(); renderArchiveIndex(); persistFilters(); loadYear(true);
}

function persistFilters() {
  if (!state.selected) return;
  const family = state.catalogFamily || sourceEl.value;
  const record = {
    family,
    source: state.selected?.source_id || '',
    satellite: satelliteEl.value,
    instrument: instrumentEl.value,
    level: levelEl.value,
    area: areaEl.value,
    resolution: resolutionEl.value,
    product: productEl.value,
    year: yearEl.value,
    month: monthEl.value,
    timezone: state.displayZone,
  };
  let families = {};
  try { families = JSON.parse(localStorage.getItem(FAMILY_FILTER_KEY) || '{}') || {}; } catch {}
  if (typeof families !== 'object' || Array.isArray(families)) families = {};
  families[family] = record;
  localStorage.setItem(FAMILY_FILTER_KEY, JSON.stringify(families));
  localStorage.setItem(FILTER_KEY, JSON.stringify(record));
}

function resetFilters() {
  localStorage.removeItem(FILTER_KEY);
  localStorage.removeItem(FAMILY_FILTER_KEY);
  localStorage.removeItem(CALENDAR_YEARS_KEY);
  if ([...sourceEl.options].some(item => item.value === 'goes')) sourceEl.value = 'goes';
  else sourceEl.selectedIndex = 0;
  yearEl.value = String(defaultYear());
  monthEl.value = String(new Date().getUTCMonth());
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
  date.setUTCHours(date.getUTCHours() + 8);
  return date.toISOString().slice(11, 16) + ' BJT';
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
  const bottom = 8;
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
  return '<div class="monitor-chart-plot">' +
    '<svg viewBox="0 0 ' + width + ' ' + height + '" preserveAspectRatio="none">' +
    grids +
    '<path class="chart-line" d="' + path.trim() + '"/>' + dot + '</svg>' +
    '<span class="monitor-chart-max">' + esc(topLabel) + '</span>' +
    '</div>' +
    '<div class="monitor-chart-time"><span>' + esc(startLabel) + '</span><span>' + esc(endLabel) + '</span></div>';
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
  const grouped = new Map();
  for (const link of payload.links || []) {
    const key = link.target + ':' + link.port;
    if (!grouped.has(key)) grouped.set(key, {...link, group_id: key, source_ids: [], source_names: []});
    grouped.get(key).source_ids.push(link.source_id);
    grouped.get(key).source_names.push(link.name);
  }
  const links = [...grouped.values()].map(link => ({
    ...link,
    name: link.source_names.length > 2 && link.source_names.every(name => name.startsWith('GOES-'))
      ? 'GOES' : link.source_names.join(' / '),
  }));
  if (!links.some(link => link.group_id === state.monitor.selected)) {
    state.monitor.selected = links.find(link => link.source_ids.includes(state.monitor.selected))?.group_id ||
      links.find(link => link.source_ids.includes('earthcare'))?.group_id || links[0]?.group_id || '';
  }
  localStorage.setItem('satellite-data-monitor-source', state.monitor.selected);
  const tones = links.map(monitorTone);
  const healthy = tones.filter(tone => tone === 'ok').length;
  const latest = links.map(link => link.latest).filter(Boolean);
  const medianLatency = monitorMedian(latest.map(item => monitorNumber(item, 'latency_ms')));
  const meanJitter = monitorMean(latest.map(item => monitorNumber(item, 'jitter_ms')));
  const meanLoss = monitorMean(latest.map(item => monitorNumber(item, 'loss_pct')));
  $('#monitor-healthy').textContent = healthy + '/' + links.length;
  $('#monitor-link-count').textContent = latest.length ? latest.length + ' 个域名已有样本' : '等待首轮采样';
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
    return '<button type="button" class="monitor-link-card' + (state.monitor.selected === link.group_id ? ' selected' : '') +
      '" data-monitor-source="' + esc(link.group_id) + '" data-tone="' + tone + '">' +
      '<div class="monitor-link-head">' +
      '<div class="monitor-link-title"><strong><i></i>' + esc(link.name) + '</strong><small>' + esc(target) + '</small></div>' +
      '<div class="monitor-link-primary"><strong>' + esc(latency) + '</strong><small>' + esc(monitorToneLabel(tone)) + '</small></div>' +
      '</div><div class="monitor-link-meta">' +
      '<span>Jitter <b>' + esc(jitter) + '</b></span><span>Loss <b>' + esc(loss) + '</b></span>' +
      '</div><div class="monitor-spark">' + monitorSpark(link.points) + '</div></button>';
  }).join('');
  renderMonitorDetail(links.find(link => link.group_id === state.monitor.selected));
}

const downloadStates = {
  pending: '等待发现', discovering: '正在发现', retry_wait: '等待重试', transferring: '传输中',
  verifying: '待校验', verify_wait: '校验重试', promoting: '校验/归档中',
  waiting_auth: '等待授权', done: '已完成', failed: '失败',
};

const dlStages = [
  ['satellite', '卫星', p => p.satellite],
  ['instrument', '传感器', p => p.instrument],
  ['level', '级别', p => p.level],
  ['area', '区域', p => p.area_type || p.metadata?.area_type || ''],
];

function downloadSelect(id, values, placeholder) {
  const node = $('#dl-' + id), old = node.value;
  node.replaceChildren(new Option(placeholder, ''));
  for (const value of values) node.add(new Option(value, value));
  if (values.includes(old)) node.value = old;
}

function refreshDownloadFilters(changed = 'source') {
  let items = state.downloads.catalog;
  for (const [id, label, field] of dlStages) {
    if (changed === 'source' || dlStages.some(([earlier]) => earlier === changed && dlStages.findIndex(([current]) => current === earlier) < dlStages.findIndex(([current]) => current === id))) {
      downloadSelect(id, uniqueSorted(items.map(field)), '全部' + label);
    }
    const chosen = $('#dl-' + id).value;
    if (chosen) items = items.filter(item => field(item) === chosen);
  }
  if (changed !== 'product' && changed !== 'band' && changed !== 'resolution') {
    const node = $('#dl-product'), old = node.value;
    node.replaceChildren(new Option('选择产品', ''));
    for (const item of items) {
      const label = [...new Set([item.level, item.data_type, item.version, item.resolution, item.metadata?.access_class].filter(Boolean))].join(' · ');
      const option = new Option(label || item.product_key, item.product_key);
      option.title = item.display_name || item.data_type;
      node.add(option);
    }
    if (items.some(item => item.product_key === old)) node.value = old;
  }
  const product = state.downloads.catalog.find(item => item.product_key === $('#dl-product').value);
  const bands = product?.bands || [];
  const band = $('#dl-band');
  const resolutionNode = $('#dl-resolution');
  if (changed !== 'resolution') {
    const values = uniqueSorted(bands.map(item => item.resolution).concat(product?.resolution || []));
    downloadSelect('resolution', values, values.length ? '全部' : metadataStatusLabels[product?.metadata?.resolution_status] || '目录未提供');
    if (!bands.length && values.length === 1) resolutionNode.value = values[0];
    if (changed === 'band' && Number(band.value)) {
      resolutionNode.value = bands.find(item => item.number === Number(band.value))?.resolution || '';
    }
  }
  if (changed !== 'band') {
    const old = band.value;
    band.replaceChildren(new Option('全部波段', '0'));
    const eligible = bands.filter(item => !resolutionNode.value || item.resolution === resolutionNode.value);
    for (const item of eligible) band.add(new Option(`C${String(item.number).padStart(2, '0')} · ${item.wavelength}`, String(item.number)));
    band.value = eligible.some(item => String(item.number) === old) ? old : '0';
  }
  band.disabled = bands.length === 0;
}

async function loadDownloadCatalog() {
  const source = $('#dl-source').value;
  syncDownloadTimezones();
  const ticket = ++state.downloads.loading;
  state.downloads.catalog = [];
  $('#dl-submit-status').textContent = '';
  refreshDownloadFilters();
  if (!source) return;
  try {
    const catalog = await json('/api/v1/downloads/catalog?source=' + encodeURIComponent(source));
    if (ticket !== state.downloads.loading) return;
    state.downloads.catalog = (catalog.products || []).map(normalizeCatalogProduct);
    state.downloads.source = source;
    refreshDownloadFilters();
  } catch (error) {
    if (ticket === state.downloads.loading) $('#dl-submit-status').textContent = '产品目录读取失败：' + error.message;
  }
}

function initializeDownloadSubmit() {
  const node = $('#dl-source'), old = node.value;
  node.replaceChildren(new Option('选择数据源', ''));
  for (const source of state.sources.filter(item => item.id !== 'era5' && item.capabilities?.includes('download'))) node.add(new Option(source.name, source.id));
  if ([...node.options].some(option => option.value === old)) node.value = old;
  if (node.value && node.value !== state.downloads.source) loadDownloadCatalog();
}

function downloadInputZone() {
  // CDS orders require whole UTC dates, regardless of the list display zone.
  return $('#dl-source').value === 'era5' ? 'UTC' : state.downloads.displayZone;
}

function downloadTimes() {
  const mode = $('#dl-time-mode').value;
  if (mode === 'all') return [];
  const times = mode === 'hourly' ? Array.from({length: 24}, (_, hour) => `${String(hour).padStart(2, '0')}:00`)
    : mode === 'synoptic' ? ['00:00', '06:00', '12:00', '18:00']
    : $('#dl-custom').value.split(/[\s,，;；]+/).filter(Boolean);
  if (!times.length || times.some(value => !/^(?:[01]\d|2[0-3]):[0-5]\d$/.test(value))) throw new Error('自定义时次须为 HH:MM，至少填一个');
  return [...new Set(times.map(value => {
    if (downloadInputZone() !== 'BJT') return value;
    const hour = (Number(value.slice(0, 2)) + 16) % 24;
    return String(hour).padStart(2, '0') + value.slice(2);
  }))].sort();
}

function downloadRange(start, end) {
  const validDay = day => /^\d{4}-\d{2}-\d{2}$/.test(day) &&
    Number.isFinite(Date.parse(day + 'T00:00:00Z')) && new Date(day + 'T00:00:00Z').toISOString().slice(0, 10) === day;
  if (!validDay(start) || !validDay(end)) throw new Error('请选择有效的开始和结束日期');
  if (end < start) throw new Error('结束日期不能早于开始日期');
  const offset = downloadInputZone() === 'BJT' ? '+08:00' : 'Z';
  const nextDay = new Date(end + 'T00:00:00' + offset);
  nextDay.setUTCDate(nextDay.getUTCDate() + 1);
  return {start: new Date(start + 'T00:00:00' + offset).toISOString(), end: nextDay.toISOString()};
}

function syncDownloadTimezones() {
  const zone = downloadInputZone();
  $('#download-timezone').value = state.downloads.displayZone;
  $('#download-monitor-timezone').value = state.downloads.history.displayZone;
  $('#dl-start-label').textContent = `开始日期（${zone}）`;
  $('#dl-end-label').textContent = `结束日期（${zone}，含当天）`;
  $('#dl-time-label').textContent = `时次（${zone}）`;
  $('#dl-custom-label').textContent = `自定义时次（${zone}，HH:MM）`;
}

function setDownloadTimezone(zone) {
  if (zone !== 'UTC' && zone !== 'BJT') return;
  state.downloads.displayZone = zone;
  localStorage.setItem('satellite-data-download-zone', zone);
  syncDownloadTimezones();
  renderDownloadRequests(state.downloads.requests);
}

function setDownloadMonitorTimezone(zone) {
  if (zone !== 'UTC' && zone !== 'BJT') return;
  state.downloads.history.displayZone = zone;
  localStorage.setItem('satellite-data-download-monitor-zone', zone);
  state.downloads.history.chartKey = '';
  syncDownloadTimezones();
  renderDownloadMonitor(state.downloads.providers, state.downloads.providers.length > 0 && !state.downloads.providerError);
}

async function submitDownload(event) {
  event.preventDefault();
  const status = $('#dl-submit-status'), button = $('#dl-submit');
  const product = state.downloads.catalog.find(item => item.product_key === $('#dl-product').value);
  if (!product || product.source_id !== $('#dl-source').value) { status.textContent = '请选择产品'; return; }
  let range, times;
  try {
    range = downloadRange($('#dl-start').value, $('#dl-end').value);
    times = downloadTimes();
  } catch (error) { status.textContent = error.message; return; }
  button.disabled = true;
  status.textContent = '正在提交…';
  try {
    const extras = ($('#dl-extra-products').value || '').split(/[,，\n]+/).map(s => s.trim()).filter(Boolean);
    const products = [product.product_key, ...extras];
    if (new Set(products).size !== products.length) throw new Error('附加产品不能重复');
    const batch = products.length > 1;
    const payload = await json(batch ? '/api/v1/downloads/batch' : '/api/v1/downloads', {
      method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(batch ? {products, ...range, band: Number($('#dl-band').value) || 0, times} :
        {source: product.source_id, collection: product.product_key, ...range,
          band: Number($('#dl-band').value) || 0, times}),
    });
    status.textContent = batch ?
      (payload.created ? '已创建批量订单 #' : '已去重复用批量订单 #') + payload.group.id :
      '已提交 #' + payload.download.id;
    loadDownloads(true);
  } catch (error) { status.textContent = '提交失败：' + error.message; }
  finally { button.disabled = false; }
}

function downloadRate(value) {
  const rate = Math.max(0, Number(value) || 0);
  if (rate < 1024) return Math.round(rate) + ' B/s';
  if (rate < 1048576) return (rate / 1024).toFixed(1) + ' KiB/s';
  return (rate / 1048576).toFixed(1) + ' MiB/s';
}

function renderDownloadBatches(groups) {
  const holder = $('#download-batches');
  if (!holder) return;
  holder.replaceChildren();
  if (!groups.length) {
    holder.textContent = '暂无多产品订单';
    return;
  }
  for (const group of groups) {
    const section = document.createElement('div');
    section.className = 'download-request';
    const header = document.createElement('strong');
    header.textContent = '批量订单 #' + group.id + ' · ' + group.products + ' 个产品 · ' + group.state;
    const info = document.createElement('div');
    info.textContent = '已发现 ' + group.assets_queued + ' · 已完成 ' + group.files_done +
      ' · 失败 ' + group.files_failed + ' · 待认证 ' + group.files_waiting_auth;
    const products = document.createElement('small');
    products.textContent = (group.downloads || []).map(item => item.collection_id + ' (' + item.state + ')').join(' / ');
    section.append(header, info, products);
    holder.append(section);
  }
}

const downloadWindowSeconds = {realtime: 300, '1h': 3600, '3h': 10800, '6h': 21600, '12h': 43200, '1d': 86400, '2d': 172800, '1w': 604800};

function downloadHistoryPoints(payload, source) {
  const sources = (payload?.providers || []).filter(item => source === 'all' || item.source_id === source);
  if (source !== 'all') return sources[0]?.points || [];
  const buckets = new Map();
  for (const provider of sources) for (const point of provider.points || []) {
    const key = point.sampled_at;
    if (!buckets.has(key)) buckets.set(key, {sampled_at: key, bytes_per_second: 0, active_transfers: 0, adaptive_concurrency: 0});
    const bucket = buckets.get(key);
    bucket.bytes_per_second += Number(point.bytes_per_second) || 0;
    bucket.active_transfers += Number(point.active_transfers) || 0;
    bucket.adaptive_concurrency += Number(point.adaptive_concurrency) || 0;
  }
  return [...buckets.values()].sort((a, b) => a.sampled_at.localeCompare(b.sampled_at));
}

function downloadTimestamp(timestamp, zone, includeYear = true) {
  if (timestamp == null || timestamp === '') return '—';
  const date = new Date(timestamp);
  if (!Number.isFinite(date.getTime())) return '—';
  if (zone === 'BJT') date.setUTCHours(date.getUTCHours() + 8);
  return date.toISOString().slice(includeYear ? 0 : 5, 16).replace('T', ' ') + ' ' + zone;
}

function downloadMonitorTime(timestamp) {
  return downloadTimestamp(timestamp, state.downloads.history.displayZone, false);
}

function drawDownloadChart(id, points, metric, bucketSeconds, windowName) {
  const node = $('#' + id);
  const width = 620, height = 150, left = 12, right = 12, top = 20, bottom = 10;
  const chartWidth = width - left - right, chartHeight = height - top - bottom;
  const now = Date.now(), start = now - (downloadWindowSeconds[windowName] || 3600) * 1000;
  const entries = points.map(point => ({
    at: new Date(point.sampled_at).getTime(),
    rate: Math.max(0, Number(point.bytes_per_second) || 0),
    active: Math.max(0, Number(point.active_transfers) || 0),
    limit: Math.max(0, Number(point.adaptive_concurrency) || 0),
  })).filter(point => Number.isFinite(point.at) && point.at >= start && point.at <= now);
  if (!entries.length) {
    node.innerHTML = '<div class="download-plot-empty">等待历史样本</div>';
    return;
  }
  const maxValue = metric === 'rate'
    ? Math.max(1024, ...entries.map(point => point.rate * 1.15))
    : Math.max(1, ...entries.map(point => Math.max(point.active, point.limit) * 1.15));
  const x = at => left + (at - start) / (now - start) * chartWidth;
  const y = value => top + (1 - Math.min(1, value / maxValue)) * chartHeight;
  const path = key => entries.map((point, index) => {
    const joined = index > 0 && point.at - entries[index - 1].at <= bucketSeconds * 2500;
    return (joined ? 'L' : 'M') + x(point.at).toFixed(1) + ',' + y(point[key]).toFixed(1);
  }).join(' ');
  const grid = [0, .5, 1].map(fraction => {
    const yy = top + fraction * chartHeight;
    return `<line class="plot-grid" x1="${left}" x2="${width - right}" y1="${yy}" y2="${yy}"/>`;
  }).join('');
  const label = metric === 'rate' ? downloadRate(maxValue) : String(Math.ceil(maxValue));
  const line = `<path class="plot-line" d="${path(metric === 'rate' ? 'rate' : 'active')}"/>`;
  const limit = metric === 'active' ? `<path class="plot-limit" d="${path('limit')}"/>` : '';
  node.innerHTML = `<svg viewBox="0 0 ${width} ${height}" preserveAspectRatio="none" role="img" aria-label="${metric === 'rate' ? '下载速度' : '活动传输与动态上限'}历史趋势">
    <title>${metric === 'rate' ? '下载速度' : '活动传输与动态上限'}历史趋势</title>${grid}${limit}${line}
    <line class="plot-cursor" x1="0" x2="0" y1="${top}" y2="${top + chartHeight}" visibility="hidden"/>
    </svg><span class="plot-max">${esc(label)}</span><div class="plot-time"><span>${esc(downloadMonitorTime(start))}</span><span>${esc(downloadMonitorTime(now))}</span></div><div class="plot-tip hidden"></div>`;
  const svg = node.querySelector('svg'), cursor = node.querySelector('.plot-cursor'), tip = node.querySelector('.plot-tip');
  svg.addEventListener('pointermove', event => {
    const bounds = svg.getBoundingClientRect();
    const target = start + Math.max(0, Math.min(1, ((event.clientX - bounds.left) / bounds.width * width - left) / chartWidth)) * (now - start);
    let nearest = entries[0];
    for (const point of entries) if (Math.abs(point.at - target) < Math.abs(nearest.at - target)) nearest = point;
    const xx = x(nearest.at);
    cursor.setAttribute('x1', xx.toFixed(1));
    cursor.setAttribute('x2', xx.toFixed(1));
    cursor.setAttribute('visibility', 'visible');
    tip.textContent = downloadMonitorTime(nearest.at) + ' · ' + (metric === 'rate'
      ? downloadRate(nearest.rate) : `${nearest.active} 活动 / ${nearest.limit} 上限`);
    tip.classList.remove('hidden');
    tip.style.left = Math.max(4, Math.min(bounds.width - tip.offsetWidth - 4, xx / width * bounds.width + 8)) + 'px';
    tip.style.top = '22px';
  });
  svg.addEventListener('pointerleave', () => { cursor.setAttribute('visibility', 'hidden'); tip.classList.add('hidden'); });
}

function renderDownloadMonitor(statuses, liveAvailable) {
  const history = state.downloads.history;
  const selector = $('#download-monitor-provider');
  const available = uniqueSorted([...statuses.map(item => item.source_id), ...(history.payload?.providers || []).map(item => item.source_id)]);
  selector.replaceChildren(new Option('全部 Provider', 'all'));
  for (const source of available) selector.add(new Option(source, source));
  if (history.selected !== 'all' && !available.includes(history.selected)) history.selected = 'all';
  selector.value = history.selected;
  document.querySelectorAll('[data-download-window]').forEach(button => button.classList.toggle('active', button.dataset.downloadWindow === history.window));
  const selected = history.selected === 'all' ? statuses.filter(item => item.enabled) : statuses.filter(item => item.source_id === history.selected);
  const total = key => selected.reduce((sum, item) => sum + (Number(item[key]) || 0), 0);
  const active = total('active_transfers');
  const allowed = selected.reduce((sum, item) => sum + (Number(item.adaptive_concurrency ?? item.transfer_concurrency) || 0), 0);
  const rate = total('bytes_per_second');
  const queued = selected.reduce((sum, item) => sum + (Number(item.states?.queued || 0) + Number(item.states?.retry_wait || 0)), 0);
  const hasLive = liveAvailable && selected.length > 0;
  $('#download-kpi-rate').textContent = hasLive ? downloadRate(rate) : '—';
  $('#download-kpi-active').textContent = hasLive ? `${active} / ${allowed}` : '—';
  $('#download-kpi-queued').textContent = hasLive ? String(queued) : '—';
  $('#download-chart-rate-now').textContent = hasLive ? downloadRate(rate) : '—';
  $('#download-chart-active-now').textContent = hasLive ? `${active} / ${allowed}` : '—';
  const samples = history.selected === 'all' ? (history.payload?.providers || []) : (history.payload?.providers || []).filter(item => item.source_id === history.selected);
  const last = Math.max(0, ...samples.map(item => new Date(item.latest?.sampled_at).getTime() || 0));
  const age = Date.now() - last;
  $('#download-kpi-sampled').textContent = !last ? '等待样本' : age > 45_000 ? '采样中断' : downloadMonitorTime(last);
  const chartKey = `${history.window}|${history.selected}|${history.displayZone}|${history.payload?.generated_at || ''}`;
  if (history.chartKey !== chartKey) {
    const points = downloadHistoryPoints(history.payload, history.selected);
    drawDownloadChart('download-chart-rate', points, 'rate', Number(history.payload?.bucket_seconds) || 15, history.window);
    drawDownloadChart('download-chart-active', points, 'active', Number(history.payload?.bucket_seconds) || 15, history.window);
    history.chartKey = chartKey;
  }
}

function incrementalSeriesSince(payload, collection) {
  const latestPerSeries = [];
  for (const item of payload?.[collection] || []) {
    let latest = 0;
    for (const point of item.points || []) latest = Math.max(latest, new Date(point.sampled_at).getTime() || 0);
    if (latest) latestPerSeries.push(latest);
  }
  if (!latestPerSeries.length) return '';
  return new Date(Math.min(...latestPerSeries)).toISOString();
}

function mergePointSeries(previous = [], incoming = [], cutoff = 0) {
  const points = new Map();
  for (const point of previous) {
    const at = new Date(point.sampled_at).getTime() || 0;
    if (!cutoff || at >= cutoff) points.set(point.sampled_at, point);
  }
  for (const point of incoming) points.set(point.sampled_at, point);
  return [...points.values()].sort((a, b) => new Date(a.sampled_at) - new Date(b.sampled_at));
}

function mergeDownloadHistory(previous, incoming, windowName) {
  if (!previous || previous.window !== incoming.window) return incoming;
  const cutoff = Date.now() - (downloadWindowSeconds[windowName] || 3600) * 1000;
  const bySource = new Map((previous.providers || []).map(item => [item.source_id, {...item, points: [...(item.points || [])]}]));
  for (const item of incoming.providers || []) {
    const old = bySource.get(item.source_id) || {source_id:item.source_id, points:[]};
    bySource.set(item.source_id, {
      ...old, ...item,
      latest: item.latest || old.latest,
      points: mergePointSeries(old.points, item.points || [], cutoff),
    });
  }
  return {...previous, ...incoming, providers:[...bySource.values()]};
}

function mergeLinkHistory(previous, incoming, windowName) {
  if (!previous || previous.window !== incoming.window) return incoming;
  const cutoff = Date.now() - (downloadWindowSeconds[windowName] || 3600) * 1000;
  const bySource = new Map((previous.links || []).map(item => [item.source_id, {...item, points:[...(item.points || [])]}]));
  for (const item of incoming.links || []) {
    const old = bySource.get(item.source_id) || {source_id:item.source_id, points:[]};
    bySource.set(item.source_id, {
      ...old, ...item,
      latest: item.latest || old.latest,
      points: mergePointSeries(old.points, item.points || [], cutoff),
    });
  }
  return {...previous, ...incoming, links:[...bySource.values()]};
}

function renderDownloadProviders(statuses, liveAvailable = true) {
  renderDownloadMonitor(statuses, liveAvailable);
  $('#download-providers').innerHTML = statuses.map(item => {
    const states = item.states || {};
    const waiting = (states.queued || 0) + (states.retry_wait || 0);
    const verifyPending = (states.verifying || 0) + (states.verify_wait || 0);
    const verifyActive = states.promoting || 0;
    const verifyLimit = item.verify_concurrency || 0;
    const transferLimit = item.adaptive_concurrency || item.transfer_concurrency || 0;
    const chip = (label, value, kind = '') =>
      '<span class="download-provider-chip ' + kind + '"><b>' + esc(label) + '</b><span>' + esc(value) + '</span></span>';
    return '<div class="download-provider"><strong class="download-provider-name">' + esc(item.source_id) + '</strong>' +
      '<div class="download-provider-chips">' +
      chip('速度', downloadRate(item.bytes_per_second), item.bytes_per_second > 0 ? 'info' : '') +
      chip('下载', (item.active_transfers || 0) + ' / ' + transferLimit, (item.active_transfers || 0) > 0 ? 'info' : '') +
      chip('校验', verifyActive + ' / ' + verifyLimit, verifyActive > 0 ? 'info' : '') +
      chip('排队', waiting, waiting > 0 ? 'wait' : '') +
      chip('待校验', verifyPending, verifyPending > 0 ? 'wait' : '') +
      chip('完成', states.done || 0, (states.done || 0) > 0 ? 'good' : '') +
      chip('失败', states.failed || 0, (states.failed || 0) > 0 ? 'bad' : '') +
      chip('待授权', states.waiting_auth || 0, (states.waiting_auth || 0) > 0 ? 'bad' : '') +
      '</div></div>';
  }).join('') || '<div class="pool-empty">暂无 Provider 状态</div>';
}

function renderDownloadRequests(items) {
  $('#download-requests').innerHTML = items.slice(0, 50).map(item => '<div class="download-request">' +
    '<strong>#' + esc(item.id) + ' · ' + esc(item.collection_id) + '</strong>' +
    '<span>' + esc(downloadStates[item.state] || item.state) + ' · ' + esc(item.files_done) + '/' + esc(item.assets_queued) + ' 文件</span>' +
    '<span>' + esc(downloadTimestamp(item.start, state.downloads.displayZone)) + ' - ' + esc(downloadTimestamp(item.end, state.downloads.displayZone)) + ' · ' +
    esc(item.band ? `C${String(item.band).padStart(2, '0')}` : '全波段') + ' · ' +
    esc(item.times_count ? item.times_count + ' 时次/天' : '全部时次') + '</span>' +
    (item.last_error ? '<small title="' + esc(item.last_error) + '">' + esc(item.last_error) + '</small>' : '') +
    '</div>').join('') || '<div class="pool-empty">暂无下载任务</div>';
  const {page, pages, total} = state.downloads;
  const pager = $('#download-pages');
  pager.classList.toggle('hidden', total <= 50);
  $('#download-page-summary').textContent = `共 ${total} 条，每页最多 50 条`;
  $('#download-page-current').textContent = `第 ${page} / ${Math.max(1, pages)} 页`;
  $('#download-page-prev').disabled = page <= 1;
  $('#download-page-next').disabled = page >= pages;
}

function applyDownloadPage(payload) {
  // Query only 50 rows on the server; never retain the entire download history.
  state.downloads.requests = (payload.downloads || []).slice(0, 50);
  state.downloads.page = payload.page || state.downloads.page;
  state.downloads.total = payload.total || 0;
  state.downloads.pages = Math.max(1, payload.pages || 1);
  renderDownloadRequests(state.downloads.requests);
}

function changeDownloadPage(direction) {
  const next = Math.max(1, Math.min(state.downloads.pages, state.downloads.page + direction));
  if (next === state.downloads.page) return;
  state.downloads.page = next;
  loadDownloads(true);
}

function mergeDownloadUpdates(payload, reset = false) {
  const byID = new Map((reset ? [] : state.downloads.requests).map(item => [item.id, item]));
  for (const item of payload.downloads || []) byID.set(item.id, item);
  state.downloads.requests = [...byID.values()].sort((a, b) => Number(b.id) - Number(a.id)).slice(0, 50);
  state.downloads.cursor = payload.cursor || state.downloads.cursor;
  renderDownloadRequests(state.downloads.requests);
}

function ensureDownloadProviderStream() {
  if (state.downloads.stream || !$('#downloads-view').classList.contains('active')) return;
  const stream = new EventSource('/api/v1/downloader/providers/stream');
  state.downloads.stream = stream;
  stream.addEventListener('providers', event => {
    if (state.downloads.stream !== stream) return;
    try {
      const payload = JSON.parse(event.data);
      state.downloads.providers = payload.providers || [];
      state.downloads.providerError = payload.error || '';
      renderDownloadProviders(state.downloads.providers, !payload.error);
      $('#downloads-error').textContent = [state.downloads.requestError, state.downloads.providerError].filter(Boolean).join(' · ');
    } catch (error) {
      state.downloads.providerError = error.message;
    }
  });
  stream.addEventListener('provider', event => {
    if (state.downloads.stream !== stream) return;
    try {
      const item = JSON.parse(event.data);
      const bySource = new Map(state.downloads.providers.map(provider => [provider.source_id, provider]));
      bySource.set(item.source_id, item);
      state.downloads.providers = [...bySource.values()].sort((a, b) => a.source_id.localeCompare(b.source_id));
      renderDownloadProviders(state.downloads.providers, !state.downloads.providerError);
    } catch (error) {
      state.downloads.providerError = error.message;
    }
  });
  stream.addEventListener('provider_error', event => {
    if (state.downloads.stream !== stream) return;
    try {
      const payload = JSON.parse(event.data);
      state.downloads.providerError = payload.error || '';
      $('#downloads-error').textContent = [state.downloads.requestError, state.downloads.providerError].filter(Boolean).join(' · ');
    } catch {}
  });
  stream.onerror = () => {
    state.downloads.providerError = 'Provider 实时流重连中';
    $('#downloads-error').textContent = [state.downloads.requestError, state.downloads.providerError].filter(Boolean).join(' · ');
  };
  stream.onopen = () => {
    state.downloads.providerError = '';
    $('#downloads-error').textContent = state.downloads.requestError || '';
  };
}

async function loadDownloads(schedule = true, forceHistory = false) {
  clearTimeout(state.downloads.poll);
  const ticket = ++state.downloads.pollSeq;
  ensureDownloadProviderStream();
  const history = state.downloads.history;
  if (!downloadWindowSeconds[history.window]) history.window = '1h';
  const cacheFresh = !forceHistory && history.payload?.window === history.window && Date.now() - history.loadedAt < 15_000;
  const historySince = !forceHistory && history.payload?.window === history.window ? incrementalSeriesSince(history.payload, 'providers') : '';
  const requestURL = '/api/v1/downloads?limit=50&page=' + encodeURIComponent(state.downloads.page);
  const batchFresh = state.downloads.batchLoadedAt && Date.now() - state.downloads.batchLoadedAt < 15_000;
  const [requests, samples, batches] = await Promise.allSettled([
    json(requestURL),
    cacheFresh ? Promise.resolve(history.payload) : json('/api/v1/monitor/downloads?window=' + encodeURIComponent(history.window) +
      (historySince ? '&since=' + encodeURIComponent(historySince) : '')),
    batchFresh ? Promise.resolve(null) : json('/api/v1/downloads/batch?limit=10'),
  ]);
  if (ticket !== state.downloads.pollSeq) return;
  state.downloads.requestError = requests.status === 'rejected' ? requests.reason.message : '';
  $('#downloads-error').textContent = [state.downloads.requestError, state.downloads.providerError].filter(Boolean).join(' · ');
  if (requests.status === 'fulfilled') {
    applyDownloadPage(requests.value);
    if (!state.downloads.requests.length && state.downloads.page > 1) {
      state.downloads.page = Math.max(1, state.downloads.pages);
      loadDownloads(schedule, forceHistory);
      return;
    }
  }
  if (batches.status === 'fulfilled' && batches.value) {
    renderDownloadBatches(batches.value.groups || []);
    state.downloads.batchLoadedAt = Date.now();
  }
  if (samples.status === 'fulfilled') {
    history.payload = cacheFresh ? samples.value : mergeDownloadHistory(history.payload, samples.value, history.window);
    if (!cacheFresh) history.loadedAt = Date.now();
  }
  $('#download-monitor-error').textContent = samples.status === 'rejected' ? '历史采样读取失败 · ' + samples.reason.message : '';
  renderDownloadProviders(state.downloads.providers, state.downloads.providers.length > 0 && !state.downloads.providerError);
  if (schedule && $('#downloads-view').classList.contains('active')) {
    state.downloads.poll = setTimeout(() => loadDownloads(true), 3000);
  }
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
    const since = incrementalSeriesSince(state.monitor.payload, 'links');
    const payload = await json('/api/v1/monitor/links?window=' + encodeURIComponent(state.monitor.window) +
      (since ? '&since=' + encodeURIComponent(since) : ''));
    state.monitor.payload = mergeLinkHistory(state.monitor.payload, payload, state.monitor.window);
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
  laads: 'LAADS', earthdata: 'Earthdata', earthcare: 'ESA MAAP', eumetsat: 'EUMETSAT', cloudsat: 'CloudSat DPC', cds: 'CDS',
};
const providerKinds = {modis: 'laads', viirs: 'laads', mtg: 'eumetsat', earthcare: 'earthcare', calipso: 'earthdata', cloudsat: 'cloudsat', era5: 'cds'};
const credentialGuides = {
  cds: {
    title: 'Copernicus CDS Personal Access Token',
    summary: '用于 ERA5 与 ERA5-Land，使用 CDS 个人访问 Token。',
    steps: ['登录 Copernicus Climate Data Store。', '在个人资料的 API 区域复制 Personal Access Token。', '先在数据集页面接受使用条款，再将完整 Token 填入下方并绑定 ERA5 账号。'],
    note: '凭据由中心加密保存。已提交的 CDS 任务绑定原账号与凭据，修改 Token 后需恢复原凭据才能继续查询该任务。',
    url: 'https://cds.climate.copernicus.eu/profile',
    link: '打开 CDS 个人资料',
  },
  laads: {
    title: 'LAADS Download Token',
    summary: '用于 MODIS 与 VIIRS。优先使用 LAADS 自己生成的下载 Token，而不是通用 Earthdata Token。',
    steps: ['登录 NASA LAADS DAAC。', '在 Token / Download Token 区域生成或复制当前 Token。', '把完整 Token 粘贴到下方 Token 输入框；保存后网页只保留掩码。'],
    note: '一条 LAADS 凭据可以分别绑定 MODIS、VIIRS 两个账号路由。中心会优先读取 LAADS Token 自带的 exp 到期字段；只有 Token 本身不提供 exp 时才显示“官网未提供”。',
    url: 'https://ladsweb.modaps.eosdis.nasa.gov/',
    link: '打开 LAADS DAAC',
  },
  earthdata: {
    title: 'NASA Earthdata User Token',
    summary: '当前用于 CALIPSO。需要 Earthdata Login 账号生成 User Token。',
    steps: ['登录 Earthdata Login。', '进入 Generate Token / User Token 页面并生成 Token。', '点击 Show Token，复制完整值后粘贴到下方。'],
    note: 'Earthdata User Token 当前有效 60 天；保存后中心会自动读取 Token 的 exp 字段显示到期时间，无需手填。',
    url: 'https://urs.earthdata.nasa.gov/',
    link: '打开 Earthdata Login',
  },
  earthcare: {
    title: 'ESA MAAP EarthCARE Token',
    summary: '用于 EarthCARE 数据访问。需要 ESA EO / MAAP 账号，并生成 EarthCARE MAAP 长期访问 Token。',
    steps: ['登录 ESA MAAP，并确保账号有 EarthCARE 数据访问权限。', '打开 MAAP Token 页面生成长期 Token。', '复制生成的 Token 粘贴到下方；不要填写示例里的 CLIENT_SECRET。'],
    note: 'offline token 当前有效 90 天；中心会自动读取 Token 的 exp 字段。部分 Cal/Val 或 Commissioning 集合仍取决于你的 ESA 账号权限。',
    url: 'https://portal.maap.eo.esa.int/ini/services/auth/token/90dToken.php',
    link: '生成 ESA MAAP Token',
  },
  eumetsat: {
    title: 'EUMETSAT API 凭据 · Consumer Key / Consumer Secret',
    summary: '用于 MTG。这里只保存 API Key Management 提供的 Consumer Key + Consumer Secret；中心会用 OAuth2 client_credentials 自动换取短期 Access Token。',
    steps: ['登录 EUMETSAT Data Store / User Portal。', '进入 API Key Management，在 User credentials 区域找到 Consumer key 与 Consumer secret。', '只把这两个值分别复制到下方两个输入框；页面下方的一小时 API token 不需要复制。'],
    note: '本凭据表单只接收 Consumer Key + Consumer Secret。中心会自动向 EUMETSAT /token 端点申请并续期 Bearer Access Token。',
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
  $('#access-error').textContent = '';
  try {
    await loadAccessPools();
  } catch (error) {
    $('#access-error').textContent = error.message;
  }
}

let cliKeyGeneration = 0;

function clearCLIKey() {
  cliKeyGeneration++;
  $('#cli-key-value').value = '';
  $('#cli-key-new').classList.add('hidden');
}

async function loadCLIKeys() {
  const keys = await json('/api/v1/me/api-keys');
  const node = $('#cli-key-list');
  node.innerHTML = keys.length ? keys.map(key => `<div class="cli-key-row"><div><strong>${esc(key.name)}</strong><code>${esc(key.prefix)}</code><small>创建 ${esc(key.created_at)}${key.last_used_at ? ' · 最近使用 ' + esc(key.last_used_at) : ''}${key.revoked_at ? ' · 已撤销' : ''}</small></div>${key.revoked_at ? '' : `<button type="button" data-cli-key-revoke="${esc(key.id)}">撤销</button>`}</div>`).join('') : '<div class="pool-empty">暂无 API Key</div>';
}

async function createCLIKey(event) {
  event.preventDefault();
  const button = $('#cli-key-create');
  if (button.disabled) return;
  clearCLIKey();
  const generation = cliKeyGeneration;
  $('#cli-key-error').textContent = '';
  button.disabled = true;
  try {
    const payload = await json('/api/v1/me/api-keys', {method:'POST', headers:{'Content-Type':'application/json', 'X-Satellite-Action':'1'}, body:JSON.stringify({name:$('#cli-key-name').value.trim()})});
    if (generation !== cliKeyGeneration) return;
    $('#cli-key-value').value = payload.api_key;
    $('#cli-key-new').classList.remove('hidden');
    $('#cli-key-name').value = '';
    await loadCLIKeys();
  } catch (error) { if (generation === cliKeyGeneration) $('#cli-key-error').textContent = error.message; }
  finally { button.disabled = false; }
}

async function revokeCLIKey(event) {
  const button = event.target.closest('[data-cli-key-revoke]');
  if (!button || !confirm('撤销这个 CLI API Key？')) return;
  button.disabled = true;
  $('#cli-key-error').textContent = '';
  try {
    await json('/api/v1/me/api-keys/' + encodeURIComponent(button.dataset.cliKeyRevoke), {method:'DELETE', headers:{'X-Satellite-Action':'1'}});
    clearCLIKey();
    await loadCLIKeys();
  } catch (error) { $('#cli-key-error').textContent = error.message; }
  finally { button.disabled = false; }
}

async function copyCLIKey() {
  try { await navigator.clipboard.writeText($('#cli-key-value').value); }
  catch { $('#cli-key-error').textContent = '剪贴板不可用'; }
}

async function loadAccessPools() {
  const payload = await json('/api/v1/access/pools');
  state.access.credentials = payload.credentials || [];
  state.access.proxies = payload.proxies || [];
  state.access.accounts = payload.accounts || [];
  const bound = new Set(state.access.accounts.map(item => item.credential_id).filter(Boolean));
  $('#access-account-count').textContent = state.access.accounts.length;
  $('#access-credential-count').textContent = state.access.credentials.filter(item => !bound.has(item.id)).length;
  $('#access-proxy-count').textContent = state.access.proxies.length;
  renderAccountList();
  renderProxyList();
  rebuildAccessSelects();
}

function poolStatePill(item) {
  const stateName = item.enabled === false ? 'disabled' : (item.state || 'ready');
  const label = {ready: '可用', auth_required: '需认证', degraded: '降级', disabled: '已停用'}[stateName] || stateName;
  return `<span class="pool-pill ${esc(stateName)}">${esc(label)}</span>`;
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
  const credentialById = new Map(state.access.credentials.map(item => [item.id, item]));
  const bound = new Set(state.access.accounts.map(item => item.credential_id).filter(Boolean));
  if (!state.access.accounts.length && !state.access.credentials.length) node.innerHTML = '<div class="pool-empty">暂无访问配置</div>';
  for (const item of state.access.accounts) {
    const route = item.proxy_mode === 'direct' ? '直连' : item.proxy_mode === 'auto' ? '代理池' : `固定 · ${item.proxy_alias || '—'}`;
    const stored = credentialById.get(item.credential_id);
    const row = document.createElement('div');
    row.className = 'pool-row';
    row.innerHTML = `
      <div class="pool-row-main"><div class="pool-row-title"><strong>${esc(item.alias)}</strong><small>${esc(item.source_id)} · ${esc(stored ? `${accessKindLabels[stored.kind] || stored.kind} · Secret 已保存` : '匿名')} · ${esc(route)}</small></div>
      <div class="pool-actions"><button type="button" data-edit-account="${item.id}">编辑</button><button type="button" data-delete-account="${item.id}">删除</button></div></div>
      <div class="pool-meta">${poolStatePill(item)}${stored ? credentialExpiryPill(stored) : ''}<span class="pool-pill">优先级 ${item.priority || 0}</span>${item.last_success_at ? `<span>成功 ${esc(formatAccessTime(item.last_success_at))}</span>` : ''}</div>
      ${item.last_error ? `<small class="form-error">${esc(item.last_error)}</small>` : ''}`;
    node.append(row);
  }
  for (const item of state.access.credentials) {
    if (bound.has(item.id)) continue;
    const row = document.createElement('div');
    row.className = 'pool-row';
    row.innerHTML = `<div class="pool-row-main"><div class="pool-row-title"><strong>${esc(item.alias)}</strong><small>${esc(accessKindLabels[item.kind] || item.kind)} · 已加密，尚未绑定 Provider</small></div>
      <div class="pool-actions"><button type="button" data-bind-credential="${item.id}">绑定</button><button type="button" data-delete-credential="${item.id}">删除</button></div></div>
      <div class="pool-meta"><span class="pool-pill">待绑定</span>${credentialExpiryPill(item)}</div>`;
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
  const kind = providerKinds[$('#account-source').value];
  const node = $('#credential-secret-fields');
  const note = $('#credential-secret-note');
  const password = (id, label, placeholder = '') => `<label>${label}<input id="${id}" type="password" autocomplete="new-password" placeholder="${placeholder}"></label>`;
  if (!kind) {
    node.replaceChildren();
    note.textContent = '该 Provider 无需凭据。';
    renderCredentialGuide();
    return;
  }
  if (['laads','earthdata','earthcare','cds'].includes(kind)) {
    node.innerHTML = `<div class="secret-grid">${password('credential-token','Token','编辑时留空即保留')}</div>`;
    note.textContent = '可沿用上方已加密 Token；填写新 Token 则为此配置创建独立凭据。';
  } else if (kind === 'eumetsat') {
    node.innerHTML = `<div class="secret-grid">${password('credential-consumer-key','Consumer Key','从 EUMETSAT API Key Management 复制')}${password('credential-consumer-secret','Consumer Secret','从 EUMETSAT API Key Management 复制')}</div>`;
    note.textContent = '这里只填写官网 User credentials 区域的 Consumer Key + Consumer Secret。API Token 无需填写，中心会自动申请并刷新；编辑时两项都留空表示保留原凭据。';
  } else {
    node.innerHTML = `<div class="secret-grid"><label>用户名<input id="credential-username" autocomplete="off" placeholder="编辑时留空即保留"></label>${password('credential-password','密码')}</div>`;
    note.textContent = '新增必须填写账号凭据；编辑时留空表示保留原凭据。';
  }
  renderCredentialGuide();
}

function renderCredentialGuide() {
  const kind = providerKinds[$('#account-source').value];
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
  credentialSelect.replaceChildren(new Option('新凭据 / 无凭据', ''));
  const required = providerKinds[$('#account-source').value] || '';
  const profileByCredential = new Map(state.access.accounts.filter(item => item.credential_id).map(item => [item.credential_id, item.alias]));
  for (const item of state.access.credentials) {
    if (item.kind !== required) continue;
    credentialSelect.add(new Option(`${profileByCredential.get(item.id) || item.alias} · ${accessKindLabels[item.kind] || item.kind}`, String(item.id)));
  }
  if ([...credentialSelect.options].some(option => option.value === previousCredential)) credentialSelect.value = previousCredential;
  $('#account-credential-row').classList.toggle('hidden', !required);

  const proxySelect = $('#account-proxy');
  const previousProxy = proxySelect.value;
  proxySelect.replaceChildren(new Option('选择代理', ''));
  for (const item of state.access.proxies) proxySelect.add(new Option(`${item.alias} · ${item.endpoint_hint}`, String(item.id)));
  if ([...proxySelect.options].some(option => option.value === previousProxy)) proxySelect.value = previousProxy;
  $('#account-proxy-row').classList.toggle('hidden', $('#account-proxy-mode').value !== 'fixed');
}

function openPoolForm(kind, item = null, savedCredential = null) {
  $(`#${kind}-form`).classList.remove('hidden');
  $(`#${kind}-error`).textContent = '';
  if (kind === 'proxy') {
    $('#proxy-id').value = item?.id || '';
    $('#proxy-alias').value = item?.alias || '';
    $('#proxy-url').value = '';
    $('#proxy-providers').value = item?.providers?.length ? item.providers.join(',') : '*';
    $('#proxy-priority').value = item?.priority ?? 0;
    $('#proxy-enabled').checked = item?.enabled ?? true;
  } else {
    $('#account-id').value = item?.id || '';
    $('#account-alias').value = item?.alias || (savedCredential ? `${savedCredential.alias} access`.slice(0, 64) : '');
    $('#account-source').value = item?.source_id || ({cds:'era5',laads:'modis',earthdata:'calipso',earthcare:'earthcare',eumetsat:'mtg',cloudsat:'cloudsat'}[savedCredential?.kind] || 'goes');
    $('#account-proxy-mode').value = item?.proxy_mode || 'direct';
    $('#account-priority').value = item?.priority ?? 0;
    $('#account-enabled').checked = item?.enabled ?? true;
    rebuildAccessSelects();
    credentialFields();
    $('#account-credential').value = item?.credential_id ? String(item.credential_id) : savedCredential ? String(savedCredential.id) : '';
    $('#account-proxy').value = item?.proxy_id ? String(item.proxy_id) : '';
  }
}

function closePoolForm(kind) {
  $(`#${kind}-form`).classList.add('hidden');
  if (kind === 'account') $('#credential-secret-fields').replaceChildren();
  if (kind === 'proxy') $('#proxy-url').value = '';
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
  const kind = providerKinds[$('#account-source').value];
  const secret = {};
  if (['laads','earthdata','earthcare','cds'].includes(kind)) secret.token = $('#credential-token')?.value || '';
  else if (kind === 'eumetsat') {
    secret.consumer_key = $('#credential-consumer-key')?.value || '';
    secret.consumer_secret = $('#credential-consumer-secret')?.value || '';
  } else if (kind === 'cloudsat') {
    secret.username = $('#credential-username')?.value || '';
    secret.password = $('#credential-password')?.value || '';
  }
  Object.keys(secret).forEach(key => { if (!secret[key]) delete secret[key]; });
  const newSecret = Object.keys(secret).length > 0;
  if (newSecret && credential && !confirm('新凭据将仅用于当前配置，原加密凭据保留。继续吗？')) return;
  const payload = {
    alias, source_id: $('#account-source').value,
    credential_id: !newSecret && credential ? Number(credential) : null, secret,
    proxy_mode: $('#account-proxy-mode').value,
    proxy_id: proxy ? Number(proxy) : null, priority: Number($('#account-priority').value || 0), enabled: $('#account-enabled').checked,
  };
  if (currentId) payload.id = currentId;
  try {
    await json('/api/v1/access/profiles', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify(payload)});
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
  if (!confirm(kind === 'account' ? '确认删除此访问配置？已加密凭据会保留为待绑定项。' : '确认删除这条配置？关联账号可能会立即失去可用路由。')) return;
  try {
    const resource = kind === 'proxy' ? 'proxies' : `${kind}s`;
    await json(`/api/v1/access/${resource}/${id}`, {method:'DELETE'});
    await loadAccessPools();
  } catch (error) { alert(error.message); }
}

async function logoutPortal() {
  clearCLIKey();
  try {
    await json('/api/v1/auth/logout', {method:'POST'});
    window.location.replace('/login');
  } catch (error) { $('#service-label').textContent = error.message; }
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
  if (typeof ResizeObserver !== 'undefined') new ResizeObserver(fitYearCalendar).observe($('.calendar-wrap'));
  else window.addEventListener('resize', fitYearCalendar);
  $('.sidebar-toggle').addEventListener('click', () => setSidebar(document.body.classList.contains('sidebar-collapsed'), true));
  document.querySelectorAll('.nav-item').forEach(node => node.addEventListener('click', event => {
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    navigateView(node.dataset.view, 'push');
  }));
  window.addEventListener('popstate', () => navigateView(viewFromPath(), 'none'));
  sourceEl.addEventListener('change', () => { persistFilters(); loadCatalog(); });
  satelliteEl.addEventListener('change', () => { rebuildFilters('satellite'); persistFilters(); });
  instrumentEl.addEventListener('change', () => { rebuildFilters('instrument'); persistFilters(); });
  levelEl.addEventListener('change', () => { rebuildFilters('level'); persistFilters(); });
  areaEl.addEventListener('change', () => { rebuildFilters('area'); persistFilters(); });
  resolutionEl.addEventListener('change', () => { rebuildFilters('resolution'); persistFilters(); });
  productEl.addEventListener('change', updateSelectedProduct);
  yearEl.addEventListener('change', () => changeCalendarYear());
  timezoneEl.addEventListener('change', () => {
    state.displayZone = timezoneEl.value;
    localStorage.setItem('satellite-data-zone', state.displayZone);
    persistFilters();
    closeDetail();
    renderCalendar();
    if (state.coverage.supported && state.coverage.view === 'day') renderCoverageDay();
    if (state.monitor.payload) renderMonitor();
  });
  $('#reset-filters').addEventListener('click', resetFilters);
  $('#query-month').addEventListener('click', queryMonth);
  $('#clear-calendar').addEventListener('click', () => loadYear(true));
  $('#coverage-views').addEventListener('click', event => {
    const button = event.target.closest('[data-coverage-view]');
    if (!button || !state.coverage.supported) return;
    const view = button.dataset.coverageView;
    if (view !== 'day') {
      clearTimeout(state.coverage.poll);
      state.coverage.requestSeq++;
      state.coverage.fileSeq++;
    }
    state.coverage.view = view;
    renderCoverageShell();
    if (view === 'month') loadCoverageMonth();
    if (view === 'day') {
      const year = Number(yearEl.value);
      const today = new Date().toISOString().slice(0, 10);
      const day = state.coverage.day && state.coverage.day.startsWith(String(year))
        ? state.coverage.day : today.startsWith(String(year)) && Number(today.slice(5, 7)) === Number(monthEl.value) + 1
          ? today : `${year}-${String(Number(monthEl.value) + 1).padStart(2, '0')}-01`;
      openCoverageDay(day);
    }
  });
  $('#coverage-band').addEventListener('change', event => {
    state.coverage.band = Number(event.target.value);
    renderCalendar();
    if (state.coverage.view === 'month') loadCoverageMonth();
    if (state.coverage.view === 'day') { $('#coverage-files').replaceChildren(); renderCoverageDay(); }
  });
  $('#coverage-times').addEventListener('click', event => {
    const button = event.target.closest('[data-coverage-time]');
    if (!button || !state.coverage.supported) return;
    state.coverage.time = button.dataset.coverageTime;
    renderCoverageShell();
    renderCalendar();
    if (state.coverage.view === 'month') loadCoverageMonth();
    if (state.coverage.view === 'day') { $('#coverage-files').replaceChildren(); renderCoverageDay(); }
  });
  gridEl.addEventListener('mouseover', event => {
    const cell = state.coverage.supported && event.target.closest('[data-day]');
    if (cell) showTip(event, cell.dataset.day);
  });
  gridEl.addEventListener('mousemove', event => { if (state.coverage.supported && !tipEl.classList.contains('hidden')) moveTip(event); });
  gridEl.addEventListener('mouseout', event => { if (state.coverage.supported && !event.relatedTarget?.closest?.('[data-day]')) hideTip(); });
  gridEl.addEventListener('click', event => {
    const cell = state.coverage.supported && event.target.closest('[data-day]');
    if (cell) queryDay(cell.dataset.day);
  });
  gridEl.addEventListener('keydown', event => {
    const cell = state.coverage.supported && event.target.closest('[data-day]');
    if (cell && (event.key === 'Enter' || event.key === ' ')) { event.preventDefault(); queryDay(cell.dataset.day); }
  });
  $('#month-grid').addEventListener('click', event => {
    const day = event.target.closest('[data-day]')?.dataset.day;
    if (day) openCoverageDay(day);
  });
  $('#coverage-prev-month').addEventListener('click', () => moveCoverageMonth(-1));
  $('#coverage-next-month').addEventListener('click', () => moveCoverageMonth(1));
  $('#coverage-day-picker').addEventListener('change', event => openCoverageDay(event.target.value));
  $('#coverage-hour').addEventListener('change', event => {
    state.coverage.hour = Number(event.target.value);
    $('#coverage-files').replaceChildren();
    renderCoverageDay();
  });
  $('#coverage-matrix').addEventListener('click', event => {
    const cell = event.target.closest('[data-indices]');
    if (!cell) return;
    document.querySelector('#coverage-matrix .selected')?.classList.remove('selected');
    cell.classList.add('selected');
    showCoverageFiles(cell.dataset.indices ? cell.dataset.indices.split(',').map(Number) : [], Number(cell.dataset.band));
  });
  $('#close-detail').addEventListener('click', closeDetail);
  $('#refresh-sources').addEventListener('click', loadSources);
  $('#era5-form').addEventListener('submit', submitERA5);
  $('#era5-refresh').addEventListener('click', refreshERA5);
  $('#era5-dataset').addEventListener('change', selectERA5Dataset);
  $('#era5-inventory-refresh').addEventListener('click', loadERA5Inventory);
  document.querySelectorAll('[data-era5-tab]').forEach(node => node.addEventListener('click', () => selectERA5Tab(node.dataset.era5Tab)));
  document.querySelectorAll('[data-catalog-tab]').forEach(node => node.addEventListener('click', () => {
    const sources = node.dataset.catalogTab === 'sources';
    $('#catalog-products-panel').classList.toggle('hidden', sources);
    $('#catalog-sources-panel').classList.toggle('hidden', !sources);
    $('.catalog-search').classList.toggle('hidden', sources);
    document.querySelectorAll('[data-catalog-tab]').forEach(tab => { tab.classList.toggle('active', tab === node); tab.setAttribute('aria-selected', String(tab === node)); });
  }));
  $('#refresh-monitor').addEventListener('click', () => loadMonitor(true));
  $('#refresh-downloads').addEventListener('click', () => loadDownloads(true, true));
  $('#download-page-prev').addEventListener('click', () => changeDownloadPage(-1));
  $('#download-page-next').addEventListener('click', () => changeDownloadPage(1));
  $('#download-timezone').addEventListener('change', event => setDownloadTimezone(event.target.value));
  $('#download-monitor-timezone').addEventListener('change', event => setDownloadMonitorTimezone(event.target.value));
  $('#download-monitor-provider').addEventListener('change', event => {
    state.downloads.history.selected = event.target.value;
    localStorage.setItem('satellite-data-download-provider', event.target.value);
    state.downloads.history.chartKey = '';
    loadDownloads(true);
  });
  $('#download-monitor-windows').addEventListener('click', event => {
    const button = event.target.closest('[data-download-window]');
    if (!button) return;
    state.downloads.history.window = button.dataset.downloadWindow;
    state.downloads.history.payload = null;
    state.downloads.history.loadedAt = 0;
    localStorage.setItem('satellite-data-download-window', button.dataset.downloadWindow);
    state.downloads.history.chartKey = '';
    loadDownloads(true, true);
  });
  $('#dl-source').addEventListener('change', loadDownloadCatalog);
  for (const [id] of dlStages) $('#dl-' + id).addEventListener('change', () => refreshDownloadFilters(id));
  $('#dl-product').addEventListener('change', () => refreshDownloadFilters('product'));
  $('#dl-band').addEventListener('change', () => refreshDownloadFilters('band'));
  $('#dl-resolution').addEventListener('change', () => refreshDownloadFilters('resolution'));
  $('#dl-time-mode').addEventListener('change', () => $('#dl-custom-field').classList.toggle('hidden', $('#dl-time-mode').value !== 'custom'));
  $('#download-submit-form').addEventListener('submit', submitDownload);
  $('#monitor-window-tabs').addEventListener('click', event => {
    const button = event.target.closest('[data-monitor-window]');
    if (!button) return;
    state.monitor.window = button.dataset.monitorWindow;
    state.monitor.payload = null;
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
  $('#portal-logout').addEventListener('click', logoutPortal);
  $('#refresh-cli-keys').addEventListener('click', () => { clearCLIKey(); loadCLIKeys().catch(error => { $('#cli-key-error').textContent = error.message; }); });
  $('#cli-key-form').addEventListener('submit', createCLIKey);
  $('#cli-key-list').addEventListener('click', revokeCLIKey);
  $('#cli-key-copy').addEventListener('click', copyCLIKey);
  $('#cli-key-dismiss').addEventListener('click', clearCLIKey);
  $('#refresh-access').addEventListener('click', loadAccess);
  document.querySelectorAll('[data-pool-new]').forEach(node => node.addEventListener('click', () => openPoolForm(node.dataset.poolNew)));
  document.querySelectorAll('[data-form-cancel]').forEach(node => node.addEventListener('click', () => closePoolForm(node.dataset.formCancel)));
  $('#account-source').addEventListener('change', () => { rebuildAccessSelects(); credentialFields(); });
  $('#account-proxy-mode').addEventListener('change', () => $('#account-proxy-row').classList.toggle('hidden', $('#account-proxy-mode').value !== 'fixed'));
  $('#proxy-form').addEventListener('submit', saveProxy);
  $('#account-form').addEventListener('submit', saveAccount);
  $('#proxy-list').addEventListener('click', event => {
    const edit = event.target.closest('[data-edit-proxy]');
    const remove = event.target.closest('[data-delete-proxy]');
    if (edit) openPoolForm('proxy', state.access.proxies.find(item => item.id === Number(edit.dataset.editProxy)));
    if (remove) deletePoolItem('proxy', Number(remove.dataset.deleteProxy));
  });
  $('#account-list').addEventListener('click', event => {
    const edit = event.target.closest('[data-edit-account]');
    const remove = event.target.closest('[data-delete-account]');
    const bind = event.target.closest('[data-bind-credential]');
    const discard = event.target.closest('[data-delete-credential]');
    if (edit) openPoolForm('account', state.access.accounts.find(item => item.id === Number(edit.dataset.editAccount)));
    if (remove) deletePoolItem('account', Number(remove.dataset.deleteAccount));
    if (bind) openPoolForm('account', null, state.access.credentials.find(item => item.id === Number(bind.dataset.bindCredential)));
    if (discard) deletePoolItem('credential', Number(discard.dataset.deleteCredential));
  });
  $('#satellite-timeline-rows').addEventListener('click', event => {
    const row = event.target.closest('[data-satellite]');
    if (!row || ![...satelliteEl.options].some(option => option.value === row.dataset.satellite)) return;
    satelliteEl.value = row.dataset.satellite;
    rebuildFilters('satellite');
    persistFilters();
  });
}

async function boot() {
  const sessionPromise = json('/api/v1/auth/session');
  setSidebar(localStorage.getItem(SIDEBAR_KEY) === '1');
  const saved = readFilters();
  if (saved.timezone === 'UTC' || saved.timezone === 'BJT') state.displayZone = saved.timezone;
  timezoneEl.value = state.displayZone;
  syncDownloadTimezones();
  fillYears();
  fillMonths();
  bind();
  // The health indicator is not a prerequisite for drawing the calendar.
  // Waiting for /healthz used to delay navigation even when year data was ready.
  void health();
  const sourcesPromise = loadSources().catch(error => {
    $('#product-title').textContent = '产品目录读取失败';
    $('#product-meta').textContent = error.message;
  });
  const session = await sessionPromise;
  state.access.user = session.user;
  $('#portal-user').textContent = session.user.username;
  document.querySelectorAll('[data-admin-only]').forEach(node => node.classList.toggle('hidden', session.user.role !== 'admin'));
  await sourcesPromise;
  navigateView(viewFromPath(), 'none');
}

boot();

import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';

const app = (await readFile(new URL('./static/app.js', import.meta.url), 'utf8')).replace(/\nboot\(\);\s*$/, '\n');
const html = await readFile(new URL('./static/index.html', import.meta.url), 'utf8');

function harness(saved = {}) {
  const storage = new Map(Object.entries(saved));
  const elements = new Map();
  const requests = [];
  const element = selector => {
    const properties = new Map();
    if (!elements.has(selector)) elements.set(selector, {
      value: '', textContent: '', innerHTML: '', options: [], listeners: {},
      style: {setProperty: (name, value) => properties.set(name, value), getPropertyValue: name => properties.get(name)},
      add(option) { this.options.push(option); },
      replaceChildren(...options) { this.options = options; },
      addEventListener(name, callback) { this.listeners[name] = callback; },
      showModal() { this.open = true; },
      close() { this.open = false; },
      classList: {add() {}, remove() {}, toggle() {}, contains() { return false; }},
    });
    return elements.get(selector);
  };
  const context = vm.createContext({
    document: {querySelector: element, querySelectorAll: () => []},
    window: {addEventListener() {}},
    localStorage: {getItem: key => storage.get(key) ?? null, setItem: (key, value) => storage.set(key, value)},
    Option: function(text, value) { this.text = text; this.value = value; },
    fetch: async (url, options = {}) => {
      requests.push({url, ...options});
      const body = options.body ? JSON.parse(options.body) : {};
      return {ok: true, json: async () => ({created:true, task: {id:7, products:body.products?.length || 0,
        created_at:'2026-10-30T00:00:00Z', downloads:(body.products || []).map(collection_id =>
          ({collection_id, start:body.options?.[collection_id]?.start || body.start, end:body.options?.[collection_id]?.end || body.end,
            selection:body.options?.[collection_id] || {times:body.times, band:body.band}, state:'pending'}))}})};
    },
    setTimeout, clearTimeout, console,
  });
  vm.runInContext(app, context);
  vm.runInContext('loadDownloads = () => {};', context);
  return {context, element, storage, requests, run: code => vm.runInContext(code, context)};
}

test('downloads default UTC and transfer monitoring defaults BJT, independent of calendar preferences', () => {
  const h = harness({'satellite-data-zone': 'BJT', 'satellite-data-filters-v1': '{"timezone":"BJT"}'});
  assert.equal(h.run('state.downloads.displayZone'), 'UTC');
  assert.equal(h.run('state.downloads.history.displayZone'), 'BJT');
  h.context.syncDownloadTimezones();
  assert.equal(h.element('#download-timezone').value, 'UTC');
  assert.equal(h.element('#download-monitor-timezone').value, 'BJT');
  assert.equal(h.element('#dl-start-label').textContent, '开始日期（UTC）');
  assert.match(html, /id="download-timezone"[^>]*>[\s\S]*?<option value="UTC">UTC<\/option>/);
  assert.match(html, /id="download-monitor-timezone"[^>]*>[\s\S]*?<option value="BJT" selected>BJT<\/option>/);
});

test('preferences persist separately and invalid saved values fall back to the correct defaults', () => {
  const h = harness({'satellite-data-download-zone': 'BJT', 'satellite-data-download-monitor-zone': 'UTC'});
  assert.equal(h.run('state.downloads.displayZone'), 'BJT');
  assert.equal(h.run('state.downloads.history.displayZone'), 'UTC');
  const invalid = harness({'satellite-data-download-zone': 'local', 'satellite-data-download-monitor-zone': ''});
  assert.equal(invalid.run('state.downloads.displayZone'), 'UTC');
  assert.equal(invalid.run('state.downloads.history.displayZone'), 'BJT');
});

test('task control updates the form and cached request list immediately without network requests', () => {
  const h = harness();
  h.context.bind();
  h.run(`state.downloads.tasks = [{id: 1, state:'done', downloads:[{collection_id: 'earthcare:ACM_CAP_2B',
    state: 'done', start: '2026-12-31T20:00:00Z', end: '2027-01-01T02:00:00Z', files_done: 1, assets_queued: 1}]}];`);
  h.element('#download-timezone').listeners.change({target: {value: 'BJT'}});
  assert.match(h.element('#download-tasks').innerHTML, /2027-01-01 04:00 BJT/);
  assert.match(h.element('#download-tasks').innerHTML, /2027-01-01 10:00 BJT/);
  assert.equal(h.element('#dl-start-label').textContent, '开始日期（BJT）');
  assert.equal(h.storage.get('satellite-data-download-zone'), 'BJT');
  assert.equal(h.run('state.downloads.history.displayZone'), 'BJT');
  h.element('#download-timezone').listeners.change({target: {value: 'UTC'}});
  assert.match(h.element('#download-tasks').innerHTML, /2026-12-31 20:00 UTC/);
  assert.equal(h.requests.length, 0);
});

test('monitor control redraws immediately and neither it nor calendar changes affect task timezone', () => {
  const h = harness();
  h.context.bind();
  h.context.renderDownloadMonitor([], false);
  assert.match(h.run('state.downloads.history.chartKey'), /\|BJT\|/);
  h.element('#download-monitor-timezone').listeners.change({target: {value: 'UTC'}});
  assert.match(h.run('state.downloads.history.chartKey'), /\|UTC\|/);
  assert.equal(h.storage.get('satellite-data-download-monitor-zone'), 'UTC');
  h.context.setDownloadTimezone('BJT');
  assert.equal(h.run('state.downloads.history.displayZone'), 'UTC');
  h.run("state.displayZone = 'BJT'");
  assert.equal(h.context.downloadMonitorTime('2026-10-05T16:30:00Z'), '10-05 16:30 UTC');
  h.context.setDownloadMonitorTimezone('BJT');
  assert.equal(h.context.downloadMonitorTime('2026-10-05T16:30:00Z'), '10-06 00:30 BJT');
  assert.equal(h.run('state.downloads.displayZone'), 'BJT');
  assert.equal(h.requests.length, 0);
});

test('timestamps handle offsets and date rollover without using the browser timezone', () => {
  const h = harness();
  assert.equal(h.context.downloadTimestamp('2026-10-06T00:30:00+08:00', 'UTC'), '2026-10-05 16:30 UTC');
  assert.equal(h.context.downloadTimestamp('2026-12-31T20:30:00Z', 'BJT'), '2027-01-01 04:30 BJT');
  for (const value of ['', null, undefined, 'invalid']) assert.equal(h.context.downloadTimestamp(value, 'UTC'), '—');
});

test('download tasks show at most 30 items per page', () => {
  const h = harness();
  h.context.bind();
  const items = Array.from({length: 33}, (_, i) => ({id: 33-i, state:'pending', downloads:[{collection_id:'modis:MYD06_L2',
    files_done:0, assets_queued:1, start:'2023-01-01T00:00:00Z',end:'2023-01-02T00:00:00Z'}]}));
  h.context.applyDownloadPage({tasks:items, page:1, pages:2, total:33});
  assert.equal(h.run('state.downloads.tasks.length'), 30);
  assert.match(h.element('#download-page-summary').textContent, /33 个任务，每页最多 30 项/);
  assert.equal(h.element('#download-page-prev').disabled, true);
  assert.equal(h.element('#download-page-next').disabled, false);
  assert.doesNotMatch(h.element('#download-tasks').innerHTML, /任务 #1 ·/);
  h.context.changeDownloadPage(1);
  assert.equal(h.run('state.downloads.page'), 2);
  h.context.applyDownloadPage({tasks:items.slice(30), page:2, pages:2, total:33});
  assert.equal(h.run('state.downloads.tasks.length'), 3);
  assert.equal(h.element('#download-page-next').disabled, true);
  h.context.changeDownloadPage(-1);
  assert.equal(h.run('state.downloads.page'), 1);
  assert.match(html, /id="download-pages"/);
});

test('task cards expose satellite, product, time selection and progress before expansion', () => {
  const h = harness();
  const child = {
    id: 509, collection_id: 'modis:C1443535037-LAADS', state: 'retry_wait',
    start: '2015-01-02T00:00:00Z', end: '2015-11-07T00:00:00Z',
    assets_queued: 6, files_done: 2, files_failed: 0, files_waiting_auth: 0,
    last_error: 'CMR MODIS search returned HTTP 502',
    satellite:'Aqua', product_code:'MYD06_L2', product_name:'Cloud properties',
    selection:{times:['00:00','12:00']},
  };
  const group = {
    id: 43, state: 'running', products: 1, assets_queued: 6, files_done: 2,
    files_failed: 0, files_waiting_auth: 0, created_at: '2026-10-20T16:00:00Z',
    downloads: [child],
  };
  h.context.renderDownloadTasks([group]);
  const closed = h.element('#download-tasks').innerHTML;
  assert.match(closed, /download-queue-toggle/);
  assert.match(closed, /aria-expanded="false"/);
  assert.match(closed, /任务 #43 · 1 个产品/);
  assert.match(closed, /Aqua · MYD06_L2/);
  assert.match(closed, /00:00 \/ 12:00 · UTC/);
  assert.doesNotMatch(closed, /批量|下载请求/);
  assert.match(closed, /执行中/);
  assert.match(closed, /已发现 6 · 已完成 2/);
  assert.match(closed, /CMR MODIS search returned HTTP 502/);
  assert.doesNotMatch(closed, /<progress\b|download-progress|progress-bar/);
  h.run('expandedDownloadTasks.add("task-43")');
  h.context.renderDownloadTasks([group]);
  assert.match(h.element('#download-tasks').innerHTML, /download-queue-card expanded/);
  assert.match(h.element('#download-tasks').innerHTML, /aria-expanded="true"/);
});

test('download request rows show precise sparse scene selections instead of all times', () => {
  const h = harness({'satellite-data-download-zone': 'BJT'});
  h.context.applyDownloadPage({tasks:[{id:408,state:'pending',downloads:[{
    id: 408, collection_id: 'modis:C1379758607-LAADS', state: 'pending',
    start: '2006-06-15T00:00:00Z', end: '2007-01-01T00:00:00Z',
    selection: {band:0, scenes:{'2006-06-15':['18:45','18:50'], '2006-06-19':['13:25']}},
    assets_queued: 0, files_done: 0, files_failed: 0, files_waiting_auth: 0,
  }]}], page:1, pages:1, total:1});
  const content = h.element('#download-tasks').innerHTML;
  assert.match(content, /2006-06-15 08:00 BJT/);
  assert.match(content, /2 天 · 3 个指定时次/);
  assert.match(content, /等待查询/);
  assert.doesNotMatch(content, /全部时次|<progress\b/);
  h.run('expandedDownloadTasks.add("task-408")');
  h.context.renderDownloadTasks(h.run('state.downloads.tasks'));
  assert.match(h.element('#download-tasks').innerHTML, /aria-expanded="true"/);
});

test('UTC inclusive end date remains a half-open UTC range', () => {
  const h = harness();
  assert.deepEqual(JSON.parse(JSON.stringify(h.context.downloadRange('2026-12-31', '2026-12-31'))), {
    start: '2026-12-31T00:00:00.000Z', end: '2027-01-01T00:00:00.000Z',
  });
});

test('BJT dates and custom times submit corresponding UTC values including previous-day hours', async () => {
  const h = harness();
  h.context.setDownloadTimezone('BJT');
  h.run("state.downloads.catalog = [{product_key:'goes18:rad',source_id:'goes18',time_profile:{kind:'scan',label:'静止卫星时次',cadence_minutes:10}}];state.downloads.selectedProducts.add('goes18:rad');state.downloads.productOptions['goes18:rad']={mode:'custom',custom:'00:30, 08:00, 23:00, 00:30'}");
  for (const [key, value] of Object.entries({start: '2027-01-01', end: '2027-01-01'})) {
    h.element('#dl-' + key).value = value;
  }
  await h.context.submitDownload({preventDefault() {}});
  assert.equal(h.requests.length, 0);
  assert.equal(h.element('#download-confirm').open, true);
  assert.match(h.element('#dl-confirm-summary').innerHTML, /2027-01-01 至 2027-01-01 · BJT/);
  await h.context.confirmDownloadSubmission();
  assert.equal(h.requests.length, 1);
  assert.deepEqual(JSON.parse(h.requests[0].body), {
    products: ['goes18:rad'],
    start: '2026-12-31T16:00:00.000Z', end: '2027-01-01T16:00:00.000Z',
    options: {'goes18:rad': {start:'2026-12-31T16:00:00.000Z',end:'2027-01-01T16:00:00.000Z',band:0,times:['00:00','15:00','16:30']}},
  });
  assert.equal(h.requests[0].url, '/api/v1/download-tasks');
  assert.equal(h.element('#dl-submit-status').textContent, '下载任务 #7 已提交');
  assert.equal(h.element('#download-confirm').open, false);
});

test('synoptic and hourly time modes also respect BJT while all-times remains unfiltered', () => {
  const h = harness();
  h.context.setDownloadTimezone('BJT');
  assert.deepEqual([...h.context.downloadTimes({mode:'synoptic'})], ['04:00', '10:00', '16:00', '22:00']);
  assert.deepEqual([...h.context.downloadTimes({mode:'hourly'})], Array.from({length: 24}, (_, hour) => String(hour).padStart(2, '0') + ':00'));
  assert.deepEqual([...h.context.downloadTimes({mode:'all'})], []);
});

test('CDS input dates remain explicitly UTC without restricting BJT list display', () => {
  const h = harness();
  h.run("state.downloads.catalog=[{product_key:'era5:sample',source_id:'era5'}];state.downloads.selectedProducts.add('era5:sample')");
  h.context.setDownloadTimezone('BJT');
  assert.equal(h.element('#download-timezone').value, 'BJT');
  assert.equal(h.element('#dl-start-label').textContent, '开始日期（UTC）');
  assert.equal(h.context.downloadRange('2026-10-06', '2026-10-06').start, '2026-10-06T00:00:00.000Z');
  assert.deepEqual([...h.context.downloadTimes({mode:'synoptic'})], ['00:00', '06:00', '12:00', '18:00']);
});

test('invalid dates, inverted ranges and invalid times cannot enqueue a request', async () => {
  const h = harness();
  for (const [start, end] of [['', '2026-10-06'], ['2026-02-30', '2026-03-01'], ['2026-10-07', '2026-10-06']]) {
    assert.throws(() => h.context.downloadRange(start, end));
  }
  for (const times of ['', '24:00', '12:99']) {
    assert.throws(() => h.context.downloadTimes({mode:'custom',custom:times}));
  }
  h.run("state.downloads.catalog = [{product_key: 'cloudsat:2B-GEOPROF', source_id: 'cloudsat'}]");
  h.run("state.downloads.selectedProducts.add('cloudsat:2B-GEOPROF')");
  await h.context.submitDownload({preventDefault() {}});
  assert.equal(h.requests.length, 0);
  assert.match(h.element('#dl-submit-status').textContent, /观测起止时间/);
});

test('cascading multi-satellite filters retain the selected basket across searches', () => {
  const h = harness();
  h.run(`state.downloads.catalog = [
    {product_key:'terra:cloud',source_id:'modis',satellite:'Terra',instrument:'MODIS',level:'L2',data_type:'MOD06_L2',display_name:'Cloud',bands:[]},
    {product_key:'aqua:cloud',source_id:'modis',satellite:'Aqua',instrument:'MODIS',level:'L2',data_type:'MYD06_L2',display_name:'Cloud',bands:[]},
    {product_key:'goes:rad',source_id:'goes18',satellite:'GOES-18',instrument:'ABI',level:'L1B',data_type:'ABI-L1b-RadF',bands:[{number:6}]}];`);
  h.context.selectDownloadProduct('goes:rad', true);
  h.run("state.downloads.filters.satellite = new Set(['Terra','Aqua'])");
  h.context.refreshDownloadFilters();
  assert.deepEqual([...h.context.filteredDownloadProducts()].map(product => product.product_key), ['terra:cloud','aqua:cloud']);
  assert.match(h.element('#dl-selected-products').innerHTML, /GOES-18/);
  assert.doesNotMatch(h.element('#dl-product-results').innerHTML, /GOES-18/);
  h.context.selectDownloadResults(true);
  assert.equal(h.run('state.downloads.selectedProducts.size'), 3);
  assert.match(h.element('#dl-selected-products').innerHTML, /data-option-field="band"/);
  assert.doesNotMatch(html, /id="dl-band"/);
  h.element('#dl-search').value = 'MYD06';
  h.context.renderDownloadSelection();
  assert.equal(h.element('#dl-filtered-count').textContent, '1');
  assert.equal(h.element('#dl-selected-count').textContent, '已选 3 个');
  h.context.selectDownloadResults(false);
  assert.equal(h.run('state.downloads.selectedProducts.size'), 2);
  assert.equal(h.run("state.downloads.selectedProducts.has('goes:rad')"), true);
});

test('selecting an oversized result does not silently submit a partial product selection', () => {
  const h = harness();
  h.run("state.downloads.catalog = Array.from({length:33},(_,i)=>({product_key:'modis:'+i,source_id:'modis',satellite:'Terra',data_type:'P'+i}))");
  h.context.selectDownloadResults(true);
  assert.equal(h.run('state.downloads.selectedProducts.size'), 0);
  assert.match(h.element('#dl-submit-status').textContent, /32/);
  for (let i=0;i<33;i++) h.context.selectDownloadProduct('modis:'+i,true);
  assert.equal(h.run('state.downloads.selectedProducts.size'), 32);
});

test('joint products remain selectable from either real satellite', () => {
  const h=harness();
  h.run("state.downloads.catalog=[{product_key:'modis:joint',source_id:'modis',satellite:'Terra / Aqua',instrument:'MODIS',data_type:'MCD06'}];state.downloads.filters.satellite=new Set(['Terra'])");
  h.context.refreshDownloadFilters();
  assert.equal(h.context.filteredDownloadProducts().length,1);
  assert.doesNotMatch(h.element('#dl-filters').innerHTML,/value="Terra \/ Aqua"/);
  h.run("state.downloads.filters.satellite=new Set(['Aqua'])");
  h.context.refreshDownloadFilters();
  assert.equal(h.context.filteredDownloadProducts().length,1);
});

test('confirmation can be cancelled and submits the reviewed snapshot exactly once', async () => {
  const h = harness();
  h.run("state.downloads.catalog=[{product_key:'modis:a',source_id:'modis',satellite:'Aqua',data_type:'MYD06_L2'},{product_key:'goes18:b',source_id:'goes18',satellite:'GOES-18',data_type:'ABI-L1b-RadF'}];state.downloads.selectedProducts=new Set(['modis:a','goes18:b'])");
  h.run("state.downloads.catalog[1].time_profile={kind:'scan',label:'静止卫星时次',cadence_minutes:10};state.downloads.productOptions['goes18:b']={mode:'synoptic'}");
  for (const [id,value] of Object.entries({start:'2026-01-01',end:'2026-01-02'})) h.element('#dl-'+id).value=value;
  await h.context.submitDownload({preventDefault(){}});
  assert.equal(h.requests.length,0);
  assert.match(h.element('#dl-confirm-summary').innerHTML,/Aqua、GOES-18/);
  assert.match(h.element('#dl-confirm-summary').innerHTML,/00:00 \/ 06:00 \/ 12:00 \/ 18:00/);
  h.context.closeDownloadConfirmation();
  await h.context.confirmDownloadSubmission();
  assert.equal(h.requests.length,0);
  assert.equal(h.run('state.downloads.selectedProducts.size'),2);
  await h.context.submitDownload({preventDefault(){}});
  h.element('#dl-start').value='2025-01-01';
  h.run("state.downloads.selectedProducts.clear()");
  let respond;
  h.context.fetch=(url,options)=>{h.requests.push({url,...options});return new Promise(resolve=>{respond=resolve;});};
  const first=h.context.confirmDownloadSubmission();
  const second=h.context.confirmDownloadSubmission();
  assert.equal(h.requests.length,1);
  const body=JSON.parse(h.requests[0].body);
  assert.deepEqual(body.products,['modis:a','goes18:b']);
  assert.equal(body.start,'2026-01-01T00:00:00.000Z');
  respond({ok:true,json:async()=>({created:true,task:{id:12,downloads:[]}})});
  await Promise.all([first,second]);
  assert.equal(h.element('#download-confirm').open,false);
  assert.match(h.element('#dl-submit-result').innerHTML,/下载任务 #12 已提交/);
});

test('failed submission retains the reviewed selection for an explicit retry', async () => {
  const h=harness();
  h.run("state.downloads.catalog=[{product_key:'modis:a',source_id:'modis',satellite:'Aqua',data_type:'MYD06_L2'}];state.downloads.selectedProducts.add('modis:a')");
  for(const [id,value] of Object.entries({start:'2026-01-01',end:'2026-01-02','time-mode':'all'})) h.element('#dl-'+id).value=value;
  await h.context.submitDownload({preventDefault(){}});
  h.context.fetch=async(url,options)=>{h.requests.push({url,...options});return {ok:false,status:503,json:async()=>({error:'暂时不可用'})};};
  await h.context.confirmDownloadSubmission();
  assert.equal(h.element('#download-confirm').open,true);
  assert.match(h.element('#dl-confirm-error').textContent,/暂时不可用/);
  assert.equal(h.run('state.downloads.selectedProducts.size'),1);
  assert.equal(h.run('state.downloads.confirmation.payload.products[0]'),'modis:a');
  assert.equal(h.element('#dl-confirm-submit').disabled,false);
});

test('mixed temporal products submit independent observation windows and UTC calendar periods', async () => {
  const h=harness();
  h.run(`state.downloads.catalog=[
    {product_key:'goes18:scan',source_id:'goes18',satellite:'GOES-18',version:'1',time_profile:{kind:'scan',label:'静止卫星时次',cadence_minutes:10},bands:[{number:2,wavelength:'0.64 µm',resolution:'0.5 km'}]},
    {product_key:'modis:swath',source_id:'modis',time_profile:{kind:'observation',label:'观测区间'}},
    ...['daily','weekly','composite','dekadal','monthly','annual'].map(kind=>({product_key:'modis:'+kind,source_id:'modis',time_profile:{kind,label:kind}}))
  ];state.downloads.selectedProducts=new Set(state.downloads.catalog.map(p=>p.product_key));
  state.downloads.productOptions['goes18:scan']={mode:'custom',custom:'00:10',band:2};
  state.downloads.productOptions['modis:swath']={customTime:true,start:'2024-02-29T08:30',end:'2024-02-29T09:15'};`);
  h.element('#dl-start').value='2024-02-29';h.element('#dl-end').value='2024-02-29';
  h.context.setDownloadTimezone('BJT');
  await h.context.submitDownload({preventDefault(){}});
  assert.equal(h.element('#download-confirm').open,true);
  const opts=h.run('JSON.parse(JSON.stringify(state.downloads.confirmation.payload.options))');
  assert.equal(opts['goes18:scan'].start,'2024-02-28T16:00:00.000Z');
  assert.deepEqual([...opts['goes18:scan'].times],['16:10']);
  assert.equal(opts['goes18:scan'].band,2);
  assert.equal(opts['modis:swath'].start,'2024-02-29T00:30:00.000Z');
  assert.equal(opts['modis:swath'].end,'2024-02-29T01:15:00.000Z');
  for(const kind of ['daily','weekly','composite']) {
    assert.equal(opts['modis:'+kind].start,'2024-02-29T00:00:00.000Z');
    assert.equal(opts['modis:'+kind].end,'2024-03-01T00:00:00.000Z');
    assert.equal(opts['modis:'+kind].times.length,0);
  }
  assert.equal(opts['modis:dekadal'].start,'2024-02-21T00:00:00.000Z');
  assert.equal(opts['modis:dekadal'].end,'2024-03-01T00:00:00.000Z');
  assert.equal(opts['modis:monthly'].start,'2024-02-01T00:00:00.000Z');
  assert.equal(opts['modis:monthly'].end,'2024-03-01T00:00:00.000Z');
  assert.equal(opts['modis:annual'].start,'2024-01-01T00:00:00.000Z');
  assert.equal(opts['modis:annual'].end,'2025-01-01T00:00:00.000Z');
  assert.match(h.element('#dl-confirm-summary').innerHTML,/2024-02 下旬 至 2024-02 下旬 · UTC/);
  assert.match(h.element('#dl-confirm-summary').innerHTML,/C02 · 0.64 µm · 0.5 km/);
  assert.match(h.element('#dl-confirm-summary').innerHTML,/2024-02-29 08:30 至 2024-02-29 09:15 · BJT/);
  assert.equal(h.requests.length,0);
});

test('each product exposes only applicable controls and independent band definitions', () => {
  const h=harness();
  h.run(`state.downloads.catalog=[
    {product_key:'goes18:a',source_id:'goes18',time_profile:{kind:'scan',label:'静止卫星时次',cadence_minutes:10},bands:[{number:2,wavelength:'0.64 µm',resolution:'0.5 km'}]},
    {product_key:'himawari9:b',source_id:'himawari9',time_profile:{kind:'scan',label:'静止卫星时次',cadence_minutes:10},bands:[{number:2,wavelength:'0.51 µm',resolution:'1 km'}]},
    ...['observation','orbit','daily','weekly','dekadal','monthly'].map(kind=>({product_key:'modis:'+kind,source_id:'modis',time_profile:{kind,label:kind}}))
  ];state.downloads.selectedProducts=new Set(state.downloads.catalog.map(p=>p.product_key));`);
  h.element('#dl-start').value='2026-01-01';h.element('#dl-end').value='2026-01-02';
  h.context.changeDownloadProductOption('goes18:a','band','2');
  h.context.changeDownloadProductOption('goes18:a','mode','custom');
  h.context.changeDownloadProductOption('goes18:a','custom','00:10');
  assert.equal(h.run("downloadSettings(state.downloads.catalog[1]).band"),0);
  assert.equal(h.run("downloadSettings(state.downloads.catalog[1]).mode"),'all');
  h.context.changeDownloadProductOption('goes18:a','inherit','',false);
  h.context.changeDownloadProductOption('goes18:a','start','2025-12-31');
  h.context.changeDownloadProductOption('goes18:a','inherit','',true);
  assert.equal(h.run("downloadSettings(state.downloads.catalog[0]).start"),'2026-01-01');
  assert.equal(h.run("downloadSettings(state.downloads.catalog[0]).band"),2);
  assert.equal(h.run("downloadSettings(state.downloads.catalog[0]).mode"),'custom');
  assert.equal(h.run("downloadBandLabel(state.downloads.catalog[0],2)"),'C02 · 0.64 µm · 0.5 km');
  assert.equal(h.run("downloadBandLabel(state.downloads.catalog[1],2)"),'C02 · 0.51 µm · 1 km');
  const render=kind=>h.run(`downloadProductOptions(state.downloads.catalog.find(p=>p.time_profile.kind==='${kind}'))`);
  assert.match(render('scan'),/data-option-field="mode"/);
  for(const kind of ['observation','orbit','daily','weekly','dekadal','monthly']) assert.doesNotMatch(render(kind),/data-option-field="mode"|data-option-field="band"/);
  assert.match(render('observation'),/type="datetime-local"/);
  assert.match(render('monthly'),/data-option-field="start-year"/);
  assert.match(render('monthly'),/data-option-field="start-month"/);
  assert.match(render('dekadal'),/上旬[\s\S]*中旬[\s\S]*下旬/);
  assert.match(render('weekly'),/覆盖开始日期/);
  assert.throws(()=>h.run("downloadProductInterval(state.downloads.catalog[0],{start:'2026-01-01',end:'2026-01-02',mode:'custom',custom:'00:05'},'UTC')"),/扫描间隔/);
});

test('source families, satellite versions, resolution units and inapplicable metadata stay distinct', () => {
  const h=harness();
  h.run(`state.downloads.catalog=[
    {product_key:'goes16:a',source_id:'goes16',satellite:'GOES-16',data_type:'ABI',version:'1',resolution:'1000m'},
    {product_key:'goes18:a',source_id:'goes18',satellite:'GOES-18',data_type:'ABI',version:'2',resolution:'1km'},
    {product_key:'himawari8:a',source_id:'himawari8',satellite:'Himawari-8',version:'1'},
    {product_key:'himawari9:a',source_id:'himawari9',satellite:'Himawari-9',version:'2'}];
    state.downloads.filters.source=new Set(['goes']);`);
  h.context.refreshDownloadFilters();
  assert.equal(h.context.filteredDownloadProducts().length,2);
  assert.doesNotMatch(h.element('#dl-filters').innerHTML,/value="goes18"|value="goes16"/);
  h.run("state.downloads.filters.version=new Set(['2'])");
  h.context.refreshDownloadFilters();
  assert.deepEqual([...h.context.filteredDownloadProducts()].map(p=>p.product_key),['goes18:a']);
  const resolutions=value=>[...h.context.downloadResolutions({resolution:value})];
  assert.deepEqual(resolutions('1000m'),resolutions('1 km'));
  assert.deepEqual(resolutions('0.5 / 1 / 2 km'),['0.5 km','1 km','2 km']);
  assert.deepEqual(resolutions('1/5km'),['1 km','5 km']);
  assert.deepEqual(resolutions('2 arcsec/pixel'),['2 arcsec/pixel']);
  assert.deepEqual([...h.context.downloadFilterValues('level',{level:'ORBIT'},p=>p.level)],['__not_applicable']);
});

test('year grid uses all available width with proportional square-cell spacing including 54-week leap years', () => {
  const h = harness();
  assert.equal(h.context.yearGridColumns(2026), 53);
  assert.equal(h.context.yearGridColumns(2000), 54);
  for (const year of [2026, 2000]) {
    h.element('#year').value = String(year);
    h.element('.calendar-wrap').clientWidth = 1100;
    h.context.fitYearCalendar();
    const cell = Number.parseFloat(h.element('#year-layout').style.getPropertyValue('--cell'));
    const gap = Number.parseFloat(h.element('#year-layout').style.getPropertyValue('--gap'));
    const columns = h.context.yearGridColumns(year);
    assert.ok(cell > 12);
    assert.ok(Math.abs(cell - gap * 4) < .001);
    assert.ok(Math.abs(columns * cell + (columns - 1) * gap - 1100) < .01);
  }
  h.element('.calendar-wrap').clientWidth = 300;
  h.context.fitYearCalendar();
  assert.equal(h.element('#year-layout').style.getPropertyValue('--cell'), '12.0000px');
  h.element('.calendar-wrap').clientWidth = 0;
  h.context.fitYearCalendar();
  assert.equal(h.element('#year-layout').style.getPropertyValue('--cell'), '12.0000px');
});

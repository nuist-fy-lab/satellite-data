import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';

const app = (await readFile(new URL('./static/app.js', import.meta.url), 'utf8')).replace(/\nboot\(\);\s*$/, '\n');

function harness(saved = {}) {
  const elements = new Map(), storage = new Map(Object.entries(saved)), streams = [], requests = [];
  function node(fragment = false) {
    let value = '';
    const result = {fragment, children: [], dataset: {}, style: {setProperty() {}}, className: '', textContent: '',
      listeners: {}, replacements: 0, clientWidth: 1000,
      get value() { return value || this.children[0]?.value || ''; },
      set value(next) { value = next; },
      set selectedIndex(index) { value = this.children[index]?.value || ''; },
      get options() { return this.children; },
      append(...items) { for (const item of items) this.children.push(...(item.fragment ? item.children : [item])); },
      replaceChildren(...items) { this.replacements++; this.children = []; value = ''; this.append(...items); },
      add(option) { this.append(option); }, setAttribute() {},
      addEventListener(name, callback) { this.listeners[name] = callback; },
      querySelector(selector) { const day = selector.match(/data-day="([^"]+)"/)?.[1]; return this.children.find(child => child.dataset.day === day); },
    };
    result.classList = {
      contains(value) { return result.className.split(' ').includes(value); },
      toggle(value, enabled) {
        const classes = new Set(result.className.split(' ').filter(Boolean));
        if (enabled ?? !classes.has(value)) classes.add(value); else classes.delete(value);
        result.className = [...classes].join(' ');
      },
      add(...values) { values.forEach(value => this.toggle(value, true)); },
      remove(...values) { values.forEach(value => this.toggle(value, false)); },
    };
    return result;
  }
  const element = selector => { if (!elements.has(selector)) elements.set(selector, node()); return elements.get(selector); };
  class Stream {
    constructor(url) { this.url = url; this.listeners = {}; this.closed = false; streams.push(this); }
    addEventListener(name, callback) { this.listeners[name] = callback; }
    close() { this.closed = true; }
    emit(name, payload) { this.listeners[name]?.({data: JSON.stringify(payload)}); }
  }
  const context = vm.createContext({
    document: {querySelector: element, querySelectorAll: () => [], createElement: () => node(), createDocumentFragment: () => node(true)},
    window: {addEventListener() {}}, EventSource: Stream,
    Option: function(text, value) { this.text = text; this.value = value; },
    localStorage: {getItem: key => storage.get(key) ?? null, setItem: (key, value) => storage.set(key, value)},
    setTimeout, clearTimeout, console, URLSearchParams,
    fetch: async (url, options = {}) => { requests.push({url, ...options}); return {ok: true, json: async () => context.payload}; },
  });
  vm.runInContext(app, context);
  const run = code => vm.runInContext(code, context);
  element('#calendar-view').className = 'active';
  element('#year').replaceChildren(...[2023,2025,2026].map(year => ({text: String(year), value: String(year)})));
  element('#year').value = '2025';
  run(`state.selected = {id:'goes18:ABI-L1b-RadC',source_id:'goes18',product:'ABI-L1b-RadC',cadence:'5m',metadata:{}};
    state.coverage.supported = true; state.coverage.bands = [2,7];`);
  return {context, run, element, streams, requests};
}

function fixedDay(h) {
  h.run(`const times = Array.from({length:288},(_,index)=>index*5).filter(minute=>minute!==905)
    .map(minute=>['2025-06-09T'+String(Math.floor(minute/60)).padStart(2,'0')+':'+String(minute%60).padStart(2,'0')+':21Z',minute]);
    const cells = times.flatMap((_,index)=>[2,7].map(band=>[index,band,1,1,0,0]));
    state.coverage.dayData = {v:1,mode:'grid',cadence:5,official_state:2,times,cells};
    state.coverage.cells = new Map(cells.map(cell=>[cell[0]+':'+cell[1],cell]));`);
}

test('calendar distinguishes absent, partial, completed and indexing days', async () => {
  const h = harness();
  const cases = [
    [[2, 12, 0, 0, 0, 0, 0, 0, 0, 0, 0], 'missing'],
    [[2, 12, 3, 0, 0, 0, 0, 0, 0, 0, 0], 'partial'],
    [[2, 12, 12, 0, 0, 0, 0, 0, 0, 0, 0], 'complete'],
  ];
  for (const [row, expected] of cases) {
    assert.equal(h.context.coverageStatus(row, 2, 0, false, 'grid'), expected);
  }
  const [full, partial, missing] = [
    ['2025-01-01', 'complete', 12, 12, false],
    ['2025-01-02', 'partial', 12, 3, false],
    ['2025-01-03', 'missing', 12, 0, true],
  ].map(([day, status, official_count, local_ok_count, indexing]) => ({day, status, official_count, local_ok_count, indexing}));
  h.run('state.coverage.supported = false;');
  h.run('state.dayState = new Map();');
  for (const item of [full, partial, missing]) h.run(`state.dayState.set(${JSON.stringify(item.day)}, ${JSON.stringify(item)});`);
  h.context.renderCalendar();
  assert.equal(h.element('#year-grid').querySelector('[data-day="2025-01-01"]').className, 'c complete');
  assert.equal(h.element('#year-grid').querySelector('[data-day="2025-01-02"]').className, 'c partial');
  assert.equal(h.element('#year-grid').querySelector('[data-day="2025-01-03"]').className, 'c missing indexing');
  const css = await readFile(new URL('./static/styles.css', import.meta.url), 'utf8');
  assert.match(css, /\.c\.missing\{background:#fff\}/);
  assert.match(css, /\.c\.partial\{background:var\(--orange\)\}/);
  assert.match(css, /\.coverage-year \.c i\.missing[^\n]*background:#fff/);
  assert.match(css, /\.coverage-year \.c i\.processing[^\n]*var\(--yellow\)/);
});

test('multiband days are white only if every band is locally absent', () => {
  const h = harness();
  const makeRow = (ordinal, totals) => [ordinal, 'partial', 12, 0, 2, 0, 0, 12, 0, 0, 0, '', totals, 2, 'grid'];
  const partlySaved = makeRow(0, [
    [2, 12, 0, 2, 0, 1, 0, 0, 0, 0, 0], // One valid file, no completely covered slots.
    [7, 12, 0, 2, 0, 0, 0, 0, 0, 0, 0],
  ]);
  const fullyAbsent = makeRow(1, [
    [2, 12, 0, 2, 0, 0, 0, 0, 0, 0, 0],
    [7, 12, 0, 2, 0, 0, 0, 0, 0, 0, 0],
  ]);
  h.run(`applyCoverageYearRows(2025, ${JSON.stringify([partlySaved, fullyAbsent])})`);
  h.context.renderCoverageYear();
  const grid = h.element('#year-grid');
  const partialDay = grid.querySelector('[data-day="2025-01-01"]');
  const emptyDay = grid.querySelector('[data-day="2025-01-02"]');
  assert.deepEqual(partialDay.children.map(cell => cell.className), ['partial', 'partial']);
  assert.deepEqual(emptyDay.children.map(cell => cell.className), ['missing', 'missing']);
  assert.equal(h.context.coverageStatus(partlySaved[12][1], 2, 5, true, 'grid', true), 'partial',
    'a missing band remains orange when viewing hourly coverage');
  assert.match(h.context.dayTip('2025-01-01'), /部分完成/);
  h.run('state.coverage.band=7');
  assert.match(h.context.dayTip('2025-01-01'), /C07.*部分完成/);
});

test('cold yearly materialization paints fast summary then streams precise band colors', () => {
  const h = harness();
  const placeholder = [0, 'partial', 1, 0, 1, 0, 0, 1, 0, 0, 0, 'rev', [], 2, 'grid', false];
  h.run(`applyCoverageYearRows(2025, ${JSON.stringify([placeholder])})`);
  h.context.renderCoverageYear();
  const cell = h.element('#year-grid').querySelector('[data-day="2025-01-01"]');
  assert.ok(cell.children.every(band => band.className === 'partial'), 'fast initial summary should not be white');
  const hydrated = [0, 'partial', 1, 0, 1, 0, 0, 1, 0, 0, 0, 'rev',
    [[2, 1, 1, 1, 1, 0, 0, 0, 0, 0, 0], [7, 1, 0, 1, 0, 0, 0, 0, 0, 0, 0]], 2, 'grid', true];
  h.run(`applyCoverageYearRows(2025, ${JSON.stringify([hydrated])})`);
  h.context.updateCell('2025-01-01');
  assert.equal(cell.children[0].className, 'complete_official_gap');
  assert.equal(cell.children[1].className, 'partial', 'missing band orange because another band has local data');
});

test('two startup year loaders share one network request', async () => {
  const h = harness();
  h.run("payload={v:1,year:2025,bands:[2,7],days:[]};");
  const first = h.context.loadCoverageYear(false);
  const second = h.context.loadCoverageYear(false);
  await Promise.all([first, second]);
  const requests = h.requests.filter(request => request.url.startsWith('/api/coverage/year?'));
  assert.equal(requests.length, 1);
});

test('MODIS year requests are deduplicated while in flight', async () => {
  const h = harness();
  h.run("payload={product:'modis:C1443536017-LAADS',year:2023,days:[]};");
  const a = h.context.requestYear('modis:C1443536017-LAADS', 2023);
  const b = h.context.requestYear('modis:C1443536017-LAADS', 2023);
  assert.equal(a, b);
  await Promise.all([a, b]);
  assert.equal(h.requests.filter(item => item.url.startsWith('/api/year?')).length, 1);
});

test('month bands and missing time slots use orange if another band has local data', () => {
  const h = harness();
  h.element('#month').value = '5';
  const part = [2, 12, 0, 2, 0, 1, 0, 0, 0, 0, 0];
  const absent = [7, 12, 0, 2, 0, 0, 0, 0, 0, 0, 0];
  const hours = Array.from({length: 24}, (_, hour) => [hour, 2, 0, 0, 0, 0]);
  h.run(`state.coverage.month = {days: [
    [1, 2, 0, 'grid', ${JSON.stringify([part, absent])}, ${JSON.stringify(hours)}],
    [2, 2, 0, 'grid', ${JSON.stringify([[2,12,0,2,0,0,0,0,0,0,0], absent])}, ${JSON.stringify(hours)}]
  ]}`);
  h.context.renderCoverageMonth();
  const grid = h.element('#month-grid');
  const withData = grid.children[0], withoutData = grid.children[1];
  assert.match(withData.children[0].style.background, /var\(--orange\)/);
  assert.doesNotMatch(withData.children[0].style.background, /missing/);
  assert.equal(withData.children[2].children[0].className, 'month-hour partial');
  assert.equal(withoutData.children[2].children[0].className, 'month-hour missing');
});

test('daily missing slots are orange when any band has data that day', () => {
  const h = harness();
  h.run(`state.coverage.dayData={mode:'grid',cadence:60,official_state:2,
    times:[['2025-06-09T00:00:00Z',0],['2025-06-09T01:00:00Z',60]],
    cells:[[0,2,2,1,0,0],[0,7,1,0,0,0],[1,2,1,0,0,0],[1,7,1,0,0,0]]};
    state.coverage.cells=new Map(state.coverage.dayData.cells.map(cell=>[cell[0]+':'+cell[1],cell]));`);
  const day = h.run('state.coverage.dayData');
  assert.equal(h.context.coverageCellState(day, [0], 7).status, 'partial');
  assert.equal(h.context.coverageCellState(day, [1], 7).status, 'partial');
  h.run('state.coverage.dayData.cells[0][3]=0; state.coverage.cells.get("0:2")[3]=0');
  assert.equal(h.context.coverageCellState(day, [0], 7).status, 'missing');
  assert.equal(h.context.coverageCellState(day, [1], 7).status, 'missing');
});

test('daily band overview does not hide file errors behind missing official slots', () => {
  const h = harness();
  h.run(`state.coverage.bands=[2,7]; state.coverage.time='all';
    state.coverage.dayData={mode:'grid',cadence:60,official_state:2,times:[['2025-06-09T00:00:00Z',0]]};
    state.coverage.cells=new Map([['0:2',[0,2,1,0,1,0]]]);`);
  assert.equal(h.context.coverageDayHours(h.run('state.coverage.dayData'))[0].slots[0].status, 'error');
  h.run(`state.coverage.cells.set('0:2',[0,2,1,0,0,1]);`);
  assert.equal(h.context.coverageDayHours(h.run('state.coverage.dayData'))[0].slots[0].status, 'processing');
});

test('daily coverage defaults to the entire day and preserves a chosen hour during incremental refresh', async () => {
  const h = harness(); fixedDay(h);
  assert.equal(h.run('state.coverage.hour'), -1);
  h.context.payload = h.run('state.coverage.dayData');
  h.run('renderCoverageShell = () => {};');
  await h.context.openCoverageDay('2025-06-09');
  assert.equal(h.element('#coverage-hour').value, '-1');
  assert.equal(h.element('#coverage-matrix').children[3].children.length, 288);
  assert.equal(h.element('#coverage-matrix').children[3].style.gridTemplateColumns, 'repeat(288,minmax(3px,1fr))');
  assert.equal(h.element('#coverage-matrix').style.minWidth, '1247px');
  assert.equal(h.element('#coverage-day-overview').children.length, 24);
  const grid = h.element('#year-grid'), replacements = grid.replacements;
  const selected = grid.querySelector('[data-day="2025-06-09"]');
  h.run('state.coverage.hour = 15');
  await h.context.openCoverageDay('2025-06-09');
  assert.equal(h.element('#coverage-hour').value, '15');
  assert.equal(grid.replacements, replacements, 'same-day refresh rebuilt the year');
  assert.equal(grid.querySelector('[data-day="2025-06-09"]'), selected);
  await h.context.openCoverageDay('2025-06-10');
  assert.equal(h.element('#coverage-hour').value, '-1');
});

test('fixed timelines distinguish official gaps, band-specific gaps, local absence and unknown indexing', () => {
  const h = harness(); fixedDay(h);
  const columns = h.context.coverageDayColumns(h.run('state.coverage.dayData'));
  assert.equal(columns.length, 288);
  const gap = columns.find(column => column.label === '15:05');
  assert.equal(h.context.coverageCellState(h.run('state.coverage.dayData'), gap.indices, 2).status, 'official_missing');
  h.run('state.coverage.cells.delete("0:7"); state.coverage.cells.get("0:2")[3] = 0;');
  assert.equal(h.context.coverageCellState(h.run('state.coverage.dayData'), [0], 7).status, 'official_missing');
  assert.equal(h.context.coverageCellState(h.run('state.coverage.dayData'), [0], 2).status, 'partial');
  h.context.renderCoverageDay();
  assert.equal(h.element('#coverage-matrix').children[3].children[181].className, 'matrix-cell band-slot official_missing');
  assert.match(h.element('#coverage-day-summary').textContent, /全天官方缺时次/);
  h.run('state.coverage.dayData.official_state = 0');
  assert.equal(h.context.coverageCellState(h.run('state.coverage.dayData'), [], 2).status, 'unknown');
  h.run('state.coverage.time = "hourly"');
  assert.equal(h.context.coverageDayColumns(h.run('state.coverage.dayData')).length, 24);
  h.run('state.coverage.hour = 15');
  h.context.renderCoverageDay();
  assert.equal(h.element('#coverage-hour').value, '-1');
  assert.equal(h.element('#coverage-matrix').children[3].children.length, 24);
});

test('event timelines use actual timestamps including seconds and never invent fixed-period gaps', () => {
  const h = harness(); fixedDay(h); h.context.renderCoverageDay();
  assert.match(h.element('#coverage-hour').options[16].text, /官方缺/);
  h.run(`state.coverage.bands = [1]; state.coverage.dayData = {mode:'events',official_state:2,
    times:[['2025-06-09T00:01:02.500Z',-1],['2025-06-09T22:30:40Z',-1]],cells:[[0,1,1,1,0,0],[1,1,1,0,0,0]]};
    state.coverage.cells = new Map(state.coverage.dayData.cells.map(cell=>[cell[0]+':'+cell[1],cell]));`);
  h.context.renderCoverageDay();
  assert.equal(h.element('#coverage-hour').options[16].text, '15:00');
  const track = h.element('#coverage-matrix').children[3];
  assert.equal(track.className, 'band-track events');
  assert.equal(track.children.length, 2);
  assert.ok(Math.abs(parseFloat(track.children[0].style.left) - 62.5/864) < 1e-8);
  assert.ok(Math.abs(parseFloat(track.children[1].style.left) - 81040/864) < 1e-8);
  assert.equal(track.children[1].className, 'matrix-cell band-slot partial');
});

test('index SSE stays attached while idle and mutates only changed day cells, including removals', () => {
  const h = harness();
  h.context.renderCoverageYear();
  const grid = h.element('#year-grid'), initialReplacements = grid.replacements;
  const first = grid.querySelector('[data-day="2025-01-01"]'), second = grid.querySelector('[data-day="2025-01-02"]');
  const untouched = second.children[0];
  h.context.ensureIndexStream('goes18:ABI-L1b-RadC', 2025);
  const stream = h.streams[0];
  assert.match(stream.url, /watch=1$/);
  const row = [0,'complete',2,2,2,2,0,0,0,0,false,'',[[2,288,288,24,24,0,0,0,0,0,0]],2,'grid'];
  stream.emit('index', {coverage:true,bands:[2,7],days:[row],active:false});
  assert.equal(first.children[0].className, 'complete');
  assert.equal(grid.replacements, initialReplacements);
  assert.equal(grid.querySelector('[data-day="2025-01-01"]'), first);
  assert.equal(second.children[0], untouched);
  assert.equal(stream.closed, false);
  stream.emit('index', {coverage:true,days:[],removed:['2025-01-01'],active:false});
  assert.equal(first.children[0].className, 'unknown');
  stream.emit('error');
  assert.match(h.element('#archive-scan-state').textContent, /重连中/);
  stream.emit('open');
  assert.doesNotMatch(h.element('#archive-scan-state').textContent, /重连中/);
  h.element('#year').value = '2026';
  h.context.ensureIndexStream('goes18:ABI-L1b-RadC', 2026);
  stream.emit('index', {coverage:true,days:[row],active:false});
  assert.equal(first.children[0].className, 'unknown');
  assert.equal(stream.closed, true);
});

test('all products render pending downloads as processing and unindexed work as breathing grey', () => {
  const h = harness();
  h.run(`state.coverage.supported = false;
    state.dayState = new Map([
      ['2025-01-01',{status:'missing',indexing:1,official_count:0}],
      ['2025-01-02',{status:'processing',indexing:0,official_count:3,download_count:2}],
      ['2025-01-03',{status:'error',indexing:0,official_count:3,error_count:1}],
      ['2025-01-04',{status:'complete',indexing:0,official_count:3,local_ok_count:3}]
    ]);`);
  h.context.renderCalendar();
  const grid = h.element('#year-grid');
  assert.equal(grid.querySelector('[data-day="2025-01-01"]').className, 'c unknown indexing');
  assert.equal(grid.querySelector('[data-day="2025-01-02"]').className, 'c processing');
  assert.equal(grid.querySelector('[data-day="2025-01-03"]').className, 'c error');
  assert.equal(grid.querySelector('[data-day="2025-01-04"]').className, 'c complete');
  assert.match(h.context.dayTip('2025-01-02'), /已选待下载时次 2/);
  h.run(`state.coverage.supported = true;
    state.coverage.yearDays = new Map([['2025-01-01',[0,'unknown',0,0,0,0,0,0,0,0,1,'',[],0,'grid']]]);`);
  h.context.renderCoverageYear();
  assert.match(grid.querySelector('[data-day="2025-01-01"]').className, /indexing/);
});

test('all calendar granularities share yellow stripe and grey animation styling', async () => {
  const css = await readFile(new URL('./static/styles.css', import.meta.url), 'utf8');
  for (const selector of ['.c.processing', '.month-hour.processing', '.matrix-cell.processing', '.day-hour-slot.processing']) {
    assert.ok(css.includes(selector), `missing status styling for ${selector}`);
  }
  assert.match(css, /\.c\.indexing\{[^}]*animation:index-breath/);
  assert.match(css, /prefers-reduced-motion:reduce/);
});

test('archive SSE shows status and available years, without overriding a remembered product year', () => {
  const h = harness({'satellite-data-calendar-years-v1': '{"goes18:ABI-L1b-RadC":"2025"}'});
  h.run('changeCalendarYear = () => { throw new Error("remembered year was overridden"); };');
  h.context.ensureIndexStream('goes18:ABI-L1b-RadC', 2025);
  h.streams[0].emit('archive', {product:'goes18:ABI-L1b-RadC',year:2025,files:287,year_files:287,
    first_day:'2023-06-01',last_day:'2026-06-09',years:[{year:2025,files:287}],official_days:2,
    queued_days:3,running_days:1,retry_days:4,scan:{state:'running',observed_files:128}});
  assert.match(h.element('#archive-scan-state').textContent, /已扫描 128/);
  assert.match(h.element('#archive-product-state').textContent, /2025 年 287/);
  assert.match(h.element('#archive-product-state').textContent, /退避重试 4/);
  assert.equal(h.element('#year').options[1].text, '2025 · 本地 287');
  assert.equal(h.element('#year').value, '2025');
  const fresh = harness();
  fresh.run('changeCalendarYear = () => {};');
  fresh.context.ensureIndexStream('goes18:ABI-L1b-RadC', 2025);
  fresh.streams[0].emit('archive', {product:'goes18:ABI-L1b-RadC',year:2025,last_day:'2023-06-01'});
  assert.equal(fresh.element('#year').value, '2023');
});

function family(h) {
  h.run(`state.collections = ['goes16','goes18','goes19'].map(source => ({id:source+':ABI-L1b-RadF',source_id:source,
    satellite:source.replace('goes','GOES-'),instrument:'ABI',level:'L1b',product:'ABI-L1b-RadF',metadata:{}}));
    state.selected = state.collections[1]; state.coverage.supported = false; satelliteEl.value = '';
    instrumentEl.value = ''; levelEl.value = '';`);
}

test('single-satellite families select their satellite and omit the all-satellites option', () => {
  const h = harness();
  for (const satellite of ['CALIPSO', 'CloudSat', 'EarthCARE', 'MTG', 'ECMWF']) {
    h.context.satellite = satellite;
    h.run(`state.collections = [{id:'fixture',satellite,instrument:'sensor',level:'L2',product:'product',metadata:{}}];
      rebuildFilters('source', false);`);
    assert.equal(h.element('#satellite').value, satellite);
    assert.deepEqual(h.element('#satellite').options.map(option => option.value), [satellite]);
  }
});

test('joint MODIS data never create a third satellite and remain selectable from either platform', () => {
  const h = harness();
  h.run(`state.collections = [{id:'terra',source_id:'modis',satellite:'Terra',instrument:'MODIS',level:'L2',product:'MOD04'},
    {id:'aqua',source_id:'modis',satellite:'Aqua',instrument:'MODIS',level:'L2',product:'MYD04'},
    {id:'joint',source_id:'modis',satellite:'Terra / Aqua',instrument:'MODIS',level:'L3',product:'MCD19'}];
    rebuildFilters('source', false);`);
  assert.deepEqual(h.element('#satellite').options.map(option => option.value), ['', 'Aqua', 'Terra']);
  assert.match(h.element('#product').options.find(option => option.value === 'joint').text, /Terra \+ Aqua 联合产品/);
  h.element('#satellite').value = 'Aqua';
  assert.deepEqual(Array.from(h.run('filteredCollections(1)'), product => product.id), ['aqua', 'joint']);
  assert.match(h.context.calendarProductLabel(h.run('state.collections[2]')), /联合产品/);
  assert.deepEqual(Array.from(h.context.satelliteTimelineData(h.run('state.collections')), row => row.filter), ['Terra', 'Aqua']);
});

test('CALIPSO levels from an older stored catalog have an L prefix without duplicate prefixes', () => {
  const h = harness();
  for (const [input, expected] of [['1', 'L1'], ['1B', 'L1B'], ['2', 'L2'], ['L2', 'L2'], ['', '']]) {
    assert.equal(h.context.catalogLevel('calipso', input), expected);
  }
  assert.equal(h.context.catalogLevel('goes18', 'L1b'), 'L1B');
});

test('MTG level-one options include the precise product type and precede other products', () => {
  const h = harness();
  h.run(`sourceEl.value = 'mtg'; state.collections = [
    {id:'l2',satellite:'MTG',instrument:'FCI',level:'L2',product:'EO:EUM:DAT:0678',metadata:{}},
    {id:'normal',satellite:'MTG',instrument:'FCI',level:'L1c',product:'EO:EUM:DAT:0662',metadata:{product_type:'FCI-1C-RRAD-FDHSI-FD'}},
    {id:'high',satellite:'MTG',instrument:'FCI',level:'L1c',product:'EO:EUM:DAT:0665',metadata:{product_type:'FCI-1C-RRAD-HRFI-FD'}}];
    rebuildFilters('source', false);`);
  assert.deepEqual(h.element('#product').options.map(option => option.value), ['normal', 'high', 'l2']);
  assert.match(h.element('#product').options[0].text, /L1c.*FDHSI.*0662/);
  assert.match(h.element('#product').options[1].text, /L1c.*HRFI.*0665/);
});

test('all satellites use the same scientific product and version, including MODIS and VIIRS counterparts', () => {
  const h = harness(); family(h);
  assert.deepEqual(Array.from(h.context.selectedCalendarProducts(), product => product.satellite), ['GOES-18', 'GOES-16', 'GOES-19']);
  h.element('#satellite').value = 'GOES-18';
  assert.equal(h.context.selectedCalendarProducts().length, 1);
  h.run(`satelliteEl.value = ''; state.collections = [
    {id:'terra',source_id:'modis',satellite:'Terra',instrument:'MODIS',level:'L1B',product:'MOD021KM',metadata:{version:'61'}},
    {id:'aqua',source_id:'modis',satellite:'Aqua',instrument:'MODIS',level:'L1B',product:'MYD021KM',metadata:{version:'61'}},
    {id:'old',source_id:'modis',satellite:'Aqua',instrument:'MODIS',level:'L1B',product:'MYD021KM',metadata:{version:'6'}},
    {id:'other',source_id:'modis',satellite:'Aqua',instrument:'MODIS',level:'L1B',product:'MYD02HKM',metadata:{version:'61'}}];
    state.selected = state.collections[0];`);
  assert.deepEqual(Array.from(h.context.selectedCalendarProducts(), product => product.id), ['terra', 'aqua']);
  assert.equal(h.context.calendarProductType({source_id:'viirs',product:'VNP02MOD'}), h.context.calendarProductType({source_id:'viirs',product:'VJ202MOD'}));
});

test('all satellite calendars read independent indexes and SSE changes only the matching calendar', async () => {
  const h = harness(); family(h);
  h.context.fetch = async url => {
    h.requests.push({url});
    const product = new URL(url, 'https://fixture.invalid').searchParams.get('product');
    return {ok: true, json: async () => ({days: [{day:'2025-01-01',status:product.startsWith('goes18')?'complete':'missing',official_count:144}]})};
  };
  await h.context.loadYear(true);
  assert.equal(h.requests.filter(request => request.url.startsWith('/api/year?')).length, 3);
  assert.equal(h.element('#satellite-year-calendars').children.length, 2);
  assert.equal(h.element('#year-grid').querySelector('[data-day="2025-01-01"]').className, 'c complete');
  const grid = h.run('state.satelliteYears.get("goes16:ABI-L1b-RadF").grid');
  const cell = grid.querySelector('[data-day="2025-01-01"]'), second = grid.children[grid.children.indexOf(cell) + 1];
  const original = grid.replacements;
  const stream = h.streams.find(stream => stream.url.includes('goes16'));
  stream.emit('index', {coverage:true,days:[[0,'partial',144,10,24,2,0,134,0,0,false,'']],active:false});
  assert.equal(cell.className, 'c partial');
  assert.equal(grid.querySelector('[data-day="2025-01-01"]'), cell);
  assert.equal(grid.replacements, original);
  assert.equal(second.className, 'c unknown');
  assert.equal(h.element('#year-grid').querySelector('[data-day="2025-01-01"]').className, 'c complete');
  stream.emit('index', {days:[],removed:['2025-01-01']});
  assert.equal(cell.className, 'c unknown');
  h.context.stopIndexStream();
  assert.ok(h.streams.every(stream => stream.closed));
});

test('stale all-satellite reads cannot overwrite a newly selected year', async () => {
  const h = harness(); family(h);
  const responses = [];
  h.context.fetch = () => new Promise(resolve => responses.push(resolve));
  const pending = h.context.loadYear(true);
  h.element('#year').value = '2026';
  for (const resolve of responses) resolve({ok:true,json:async () => ({days:[{day:'2025-01-01',status:'complete'}]})});
  await pending;
  assert.equal(h.run('state.dayState.size'), 0);
  assert.equal(h.streams.length, 0);
});

test('month index priority applies to every displayed satellite, not only the first', async () => {
  const h = harness(); family(h);
  h.element('#month').value = '5';
  h.context.payload = {days:[]};
  await h.context.queryMonth();
  const posts = h.requests.filter(request => request.method === 'POST');
  assert.equal(posts.length, 3);
  for (const post of posts) {
    const params = new URL(post.url, 'https://fixture.invalid').searchParams;
    assert.equal(params.get('start'), '2025-06-01');
    assert.equal(params.get('end'), '2025-07-01');
  }
  h.context.stopIndexStream();
});

test('homepage has no visible month filter and month indexing uses the internal month view', async () => {
  const {readFileSync} = await import('node:fs');
  const html = readFileSync(new URL('./static/index.html', import.meta.url), 'utf8');
  assert.doesNotMatch(html, /<label>\s*<span>月<\/span>/);
  assert.match(html, /<input[^>]*id="month"[^>]*type="hidden"/);
  const h = harness();
  h.run('state.coverage.supported = true; state.coverage.view = "year";');
  h.context.renderCoverageShell();
  assert.equal(h.element('#query-month').classList.contains('hidden'), true);
  h.run('state.coverage.view = "month";');
  h.context.renderCoverageShell();
  assert.equal(h.element('#query-month').classList.contains('hidden'), false);
  h.element('#month').value = '5';
  h.context.moveCoverageMonth(1);
  assert.equal(h.element('#month').value, '6');
});

test('explicit all-satellite filters survive catalog reloads while legacy single-source filters still migrate', () => {
  const h = harness();
  h.run(`state.sources = [{id:'goes18',name:'GOES-18'},{id:'goes19',name:'GOES-19'}];
    sourceEl.value = 'goes'; updateSelectedProduct = () => {};
    const catalog = state.sources.map(source => ({product_key:source.id+':ABI-L1b-RadF',source_id:source.id,
      satellite:source.name,instrument:'ABI',level:'L1b',data_type:'ABI-L1b-RadF',metadata:{}}));
    applyCatalog(catalog, 'goes', {family:'goes',source:'goes18',satellite:''});`);
  assert.equal(h.element('#satellite').value, '');
  h.run(`applyCatalog(catalog, 'goes', {source:'goes18'});`);
  assert.equal(h.element('#satellite').value, 'GOES-18');
});

test('all-satellite view defaults to the latest data year across all displayed products', () => {
  const h = harness(); family(h);
  h.run(`state.selected.metadata.last_day = '2023-12-31'; state.collections[2].metadata.last_day = '2026-10-01';
    selectCalendarYear(state.selected);`);
  assert.equal(h.element('#year').value, '2026');
  h.run(`localStorage.setItem(CALENDAR_YEARS_KEY, JSON.stringify({[state.selected.id]:'2025'})); selectCalendarYear(state.selected);`);
  assert.equal(h.element('#year').value, '2025');
});

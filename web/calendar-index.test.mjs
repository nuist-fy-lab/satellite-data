import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';

const app = (await readFile(new URL('./static/app.js', import.meta.url), 'utf8')).replace(/\nboot\(\);\s*$/, '\n');

function harness(saved = {}) {
  const elements = new Map(), storage = new Map(Object.entries(saved)), streams = [];
  function node(fragment = false) {
    const result = {fragment, children: [], dataset: {}, style: {setProperty() {}}, className: '', value: '', textContent: '',
      listeners: {}, replacements: 0, clientWidth: 1000,
      get options() { return this.children; },
      append(...items) { for (const item of items) this.children.push(...(item.fragment ? item.children : [item])); },
      replaceChildren(...items) { this.replacements++; this.children = []; this.append(...items); },
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
    setTimeout, clearTimeout, console,
    fetch: async () => ({ok: true, json: async () => context.payload}),
  });
  vm.runInContext(app, context);
  const run = code => vm.runInContext(code, context);
  element('#calendar-view').className = 'active';
  element('#year').replaceChildren(...[2023,2025,2026].map(year => ({text: String(year), value: String(year)})));
  element('#year').value = '2025';
  run(`state.selected = {id:'goes18:ABI-L1b-RadC',source_id:'goes18',product:'ABI-L1b-RadC',cadence:'5m',metadata:{}};
    state.coverage.supported = true; state.coverage.bands = [2,7];`);
  return {context, run, element, streams};
}

function fixedDay(h) {
  h.run(`const times = Array.from({length:288},(_,index)=>index*5).filter(minute=>minute!==905)
    .map(minute=>['2025-06-09T'+String(Math.floor(minute/60)).padStart(2,'0')+':'+String(minute%60).padStart(2,'0')+':21Z',minute]);
    const cells = times.flatMap((_,index)=>[2,7].map(band=>[index,band,1,1,0,0]));
    state.coverage.dayData = {v:1,mode:'grid',cadence:5,official_state:2,times,cells};
    state.coverage.cells = new Map(cells.map(cell=>[cell[0]+':'+cell[1],cell]));`);
}

test('daily coverage defaults to the entire day and preserves a chosen hour during incremental refresh', async () => {
  const h = harness(); fixedDay(h);
  assert.equal(h.run('state.coverage.hour'), -1);
  h.context.payload = h.run('state.coverage.dayData');
  h.run('renderCoverageShell = () => {};');
  await h.context.openCoverageDay('2025-06-09');
  assert.equal(h.element('#coverage-hour').value, '-1');
  assert.equal(h.element('#coverage-matrix').children[3].children.length, 288);
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
  assert.equal(h.context.coverageCellState(h.run('state.coverage.dayData'), [0], 2).status, 'missing');
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
  const h = harness();
  h.run(`state.coverage.bands = [1]; state.coverage.dayData = {mode:'events',official_state:2,
    times:[['2025-06-09T00:01:02.500Z',-1],['2025-06-09T22:30:40Z',-1]],cells:[[0,1,1,1,0,0],[1,1,1,0,0,0]]};
    state.coverage.cells = new Map(state.coverage.dayData.cells.map(cell=>[cell[0]+':'+cell[1],cell]));`);
  h.context.renderCoverageDay();
  const track = h.element('#coverage-matrix').children[3];
  assert.equal(track.className, 'band-track events');
  assert.equal(track.children.length, 2);
  assert.ok(Math.abs(parseFloat(track.children[0].style.left) - 62.5/864) < 1e-8);
  assert.ok(Math.abs(parseFloat(track.children[1].style.left) - 81040/864) < 1e-8);
  assert.equal(track.children[1].className, 'matrix-cell band-slot missing');
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

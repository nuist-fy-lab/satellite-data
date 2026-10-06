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
      return {ok: true, json: async () => ({download: {id: 7}})};
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
  h.run(`state.downloads.requests = [{id: 1, collection_id: 'earthcare:ACM_CAP_2B',
    state: 'done', start: '2026-12-31T20:00:00Z', end: '2027-01-01T02:00:00Z', files_done: 1, assets_queued: 1}];`);
  h.element('#download-timezone').listeners.change({target: {value: 'BJT'}});
  assert.match(h.element('#download-requests').innerHTML, /2027-01-01 04:00 BJT/);
  assert.match(h.element('#download-requests').innerHTML, /2027-01-01 10:00 BJT/);
  assert.equal(h.element('#dl-time-label').textContent, '时次（BJT）');
  assert.equal(h.storage.get('satellite-data-download-zone'), 'BJT');
  assert.equal(h.run('state.downloads.history.displayZone'), 'BJT');
  h.element('#download-timezone').listeners.change({target: {value: 'UTC'}});
  assert.match(h.element('#download-requests').innerHTML, /2026-12-31 20:00 UTC/);
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

test('UTC inclusive end date remains a half-open UTC range', () => {
  const h = harness();
  assert.deepEqual(JSON.parse(JSON.stringify(h.context.downloadRange('2026-12-31', '2026-12-31'))), {
    start: '2026-12-31T00:00:00.000Z', end: '2027-01-01T00:00:00.000Z',
  });
});

test('BJT dates and custom times submit corresponding UTC values including previous-day hours', async () => {
  const h = harness();
  h.context.setDownloadTimezone('BJT');
  h.run("state.downloads.catalog = [{product_key: 'earthcare:ACM_CAP_2B', source_id: 'earthcare'}]");
  for (const [key, value] of Object.entries({source: 'earthcare', product: 'earthcare:ACM_CAP_2B',
    start: '2027-01-01', end: '2027-01-01', 'time-mode': 'custom', custom: '00:30, 08:00, 23:00, 00:30'})) {
    h.element('#dl-' + key).value = value;
  }
  await h.context.submitDownload({preventDefault() {}});
  assert.equal(h.requests.length, 1);
  assert.deepEqual(JSON.parse(h.requests[0].body), {
    source: 'earthcare', collection: 'earthcare:ACM_CAP_2B',
    start: '2026-12-31T16:00:00.000Z', end: '2027-01-01T16:00:00.000Z',
    band: 0, times: ['00:00', '15:00', '16:30'],
  });
  assert.equal(h.element('#dl-submit-status').textContent, '已提交 #7');
});

test('synoptic and hourly time modes also respect BJT while all-times remains unfiltered', () => {
  const h = harness();
  h.context.setDownloadTimezone('BJT');
  h.element('#dl-time-mode').value = 'synoptic';
  assert.deepEqual([...h.context.downloadTimes()], ['04:00', '10:00', '16:00', '22:00']);
  h.element('#dl-time-mode').value = 'hourly';
  assert.deepEqual([...h.context.downloadTimes()], Array.from({length: 24}, (_, hour) => String(hour).padStart(2, '0') + ':00'));
  h.element('#dl-time-mode').value = 'all';
  assert.deepEqual([...h.context.downloadTimes()], []);
});

test('CDS input dates remain explicitly UTC without restricting BJT list display', () => {
  const h = harness();
  h.element('#dl-source').value = 'era5';
  h.context.setDownloadTimezone('BJT');
  assert.equal(h.element('#download-timezone').value, 'BJT');
  assert.equal(h.element('#dl-start-label').textContent, '开始日期（UTC）');
  assert.equal(h.context.downloadRange('2026-10-06', '2026-10-06').start, '2026-10-06T00:00:00.000Z');
  h.element('#dl-time-mode').value = 'synoptic';
  assert.deepEqual([...h.context.downloadTimes()], ['00:00', '06:00', '12:00', '18:00']);
});

test('invalid dates, inverted ranges and invalid times cannot enqueue a request', async () => {
  const h = harness();
  for (const [start, end] of [['', '2026-10-06'], ['2026-02-30', '2026-03-01'], ['2026-10-07', '2026-10-06']]) {
    assert.throws(() => h.context.downloadRange(start, end));
  }
  h.element('#dl-time-mode').value = 'custom';
  for (const times of ['', '24:00', '12:99']) {
    h.element('#dl-custom').value = times;
    assert.throws(() => h.context.downloadTimes());
  }
  h.run("state.downloads.catalog = [{product_key: 'cloudsat:2B-GEOPROF', source_id: 'cloudsat'}]");
  h.element('#dl-source').value = 'cloudsat';
  h.element('#dl-product').value = 'cloudsat:2B-GEOPROF';
  await h.context.submitDownload({preventDefault() {}});
  assert.equal(h.requests.length, 0);
  assert.match(h.element('#dl-submit-status').textContent, /日期/);
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

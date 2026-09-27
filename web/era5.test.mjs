import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';

const app = await readFile(new URL('./static/app.js', import.meta.url), 'utf8');
const withoutBoot = app.replace(/\nboot\(\);\s*$/, '\n');

function harness(product, schema) {
  const elements = new Map();
  const storage = new Map();
  const requests = [];
  const element = id => {
    if (!elements.has(id)) {
      let value = '';
      elements.set(id, {
        options: [], disabled: false, textContent: '',
        get value() { return value || this.options.find(option => option.selected)?.value || this.options[0]?.value || ''; },
        set value(next) { value = next; },
        get selectedOptions() { return this.options.filter(option => option.selected); },
        replaceChildren() { this.options = []; value = ''; },
        add(option) { this.options.push(option); },
        classList: {add() {}, remove() {}, toggle() {}},
      });
    }
    return elements.get(id);
  };
  const context = vm.createContext({
    document: {querySelector: element},
    localStorage: {getItem: key => storage.get(key) || null, setItem: (key, value) => storage.set(key, value)},
    Option: function(text, value) { this.text = text; this.value = value; this.selected = false; },
    fetch: async (url, options = {}) => {
      requests.push({url, ...options});
      const payload = url.includes('/schema?') ? schema : {jobID: 'test-request-1', status: 'accepted'};
      return {ok: true, status: 200, json: async () => payload};
    },
    setTimeout, clearTimeout, console,
  });
  vm.runInContext(withoutBoot, context);
  context.testProduct = product;
  vm.runInContext('state.selected = testProduct; state.era5.seq = 1;', context);
  return {context, element, storage, requests};
}

function schema(monthly, pressure = false) {
  const inputs = {
    variable: {schema: {items: {enum: ['2m_temperature', 'total_precipitation']}}},
    year: {}, month: {}, area: {},
    product_type: {schema: {items: {enum: [monthly ? 'monthly_averaged_reanalysis' : 'reanalysis']}}},
    data_format: {schema: {enum: ['grib', 'netcdf'], default: 'grib'}},
    download_format: {schema: {enum: ['zip', 'unarchived'], default: 'unarchived'}},
    time: {schema: {items: {enum: ['00:00', '01:00', '02:00']}}},
  };
  if (!monthly) inputs.day = {};
  if (pressure) inputs.pressure_level = {schema: {items: {enum: ['850', '500']}}};
  return {id: 'reanalysis-era5-single-levels', inputs};
}

function product(monthly = false) {
  return {id: 'era5:reanalysis-era5-single-levels', cadence: monthly ? 'monthly' : '1h', metadata: {temporal_start: '1940-01-01T00:00:00Z', temporal_end: '2026-09-21T00:00:00Z'}};
}

function selectInputs(h) {
  h.element('#era5-variables').options[0].selected = true;
  for (const [key, value] of [['north', '20'], ['west', '100'], ['south', '10'], ['east', '110']]) h.element('#era5-' + key).value = value;
}

test('hourly subset submits exactly one day, explicit times and pressure levels', async () => {
  const h = harness(product(), schema(false, true));
  await h.context.prepareERA5(h.context.testProduct, 1);
  assert.equal(h.element('#era5-date').type, 'date');
  assert.equal(h.element('#era5-date').min, '1940-01-01');
  assert.equal(h.element('#era5-pressure').required, true);
  h.element('#era5-date').value = '2025-12-31';
  h.element('#era5-pressure').options[0].selected = true;
  selectInputs(h);
  await h.context.submitERA5({preventDefault() {}});
  const request = h.requests.find(item => item.method === 'POST');
  assert.deepEqual(JSON.parse(request.body).inputs, {
    variable: ['2m_temperature'], year: ['2025'], month: ['12'], day: ['31'],
    time: ['00:00', '01:00', '02:00'], pressure_level: ['850'], product_type: ['reanalysis'],
    data_format: 'grib', download_format: 'unarchived', area: [20, 100, 10, 110],
  });
  assert.equal(h.storage.get('satellite-data-era5-job'), 'test-request-1');
  assert.equal(h.element('#era5-submit').disabled, false);
});

test('monthly subset has no day field and defaults to midnight, not a time cross-product', async () => {
  const h = harness(product(true), schema(true));
  await h.context.prepareERA5(h.context.testProduct, 1);
  assert.equal(h.element('#era5-date').type, 'month');
  assert.equal(h.element('#era5-date').value, '2026-09');
  assert.equal(h.element('#era5-pressure').required, false);
  selectInputs(h);
  await Promise.all([h.context.submitERA5({preventDefault() {}}), h.context.submitERA5({preventDefault() {}})]);
  const submissions = h.requests.filter(item => item.method === 'POST');
  assert.equal(submissions.length, 1);
  const inputs = JSON.parse(submissions[0].body).inputs;
  assert.equal(Object.hasOwn(inputs, 'day'), false);
  assert.deepEqual(inputs.time, ['00:00']);
  assert.deepEqual(inputs.product_type, ['monthly_averaged_reanalysis']);
});

test('invalid bounding box never submits', async () => {
  const h = harness(product(), schema(false));
  await h.context.prepareERA5(h.context.testProduct, 1);
  selectInputs(h);
  h.element('#era5-north').value = '0';
  await h.context.submitERA5({preventDefault() {}});
  assert.equal(h.requests.filter(item => item.method === 'POST').length, 0);
});

test('stale process schema cannot replace the selected dataset form', async () => {
  const h = harness(product(), schema(false));
  await h.context.prepareERA5(h.context.testProduct, 0);
  assert.equal(h.element('#era5-variables').options.length, 0);
});

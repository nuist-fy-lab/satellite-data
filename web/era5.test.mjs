import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';

const app = await readFile(new URL('./static/app.js', import.meta.url), 'utf8');
const html = await readFile(new URL('./static/index.html', import.meta.url), 'utf8');
const loginHTML = await readFile(new URL('./static/login.html', import.meta.url), 'utf8');
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
        replaceChildren(...items) { this.options = items; this.innerHTML = ''; value = ''; },
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
  vm.runInContext('state.selected = testProduct; state.era5.selected = testProduct; state.era5.seq = 1;', context);
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

test('portal login precedes data loading and credentials have no second login form', () => {
  assert.equal(html.includes('access-login-form'), false);
  assert.equal(html.includes('access-password'), false);
  assert.match(html, /id="portal-logout"/);
  assert.match(html, /id="cli-panel"/);
  assert.equal(html.includes('id="cli-view"'),false);
  assert.match(html,/https:\/\/github\.com\/nuist-fy-lab\/satellite-data-downloader/);
  assert.match(html,/data-view="calendar-view"[^\n]+数据日历/);
  assert.match(html,/data-view="access-view"[^\n]+访问凭据/);
  assert.match(loginHTML, /name="username"/);
  assert.match(loginHTML, /name="password"[^>]+type="password"|type="password"[^>]+name="password"/);
  const boot = app.slice(app.lastIndexOf('async function boot()'));
  assert.ok(boot.indexOf('/api/v1/auth/session') < boot.indexOf('loadSources()'));
});

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

test('web CLI Key creation is session-only and never persists the returned Key', async () => {
  const h = harness(product(), schema(false));
  const requests = [];
  const secret = 'satellite_v1_fixture-key';
  h.context.fetch = async (url, options = {}) => {
    requests.push({url, ...options});
    const payload = options.method === 'POST' ? {api_key: secret} : [{id:'fixture', name:'Laptop', prefix:'satellite_v1_fixture', created_at:'2026-09-27'}];
    return {ok:true, status:200, json:async () => payload};
  };
  h.element('#cli-key-name').value = 'Laptop';
  await Promise.all([h.context.createCLIKey({preventDefault() {}}), h.context.createCLIKey({preventDefault() {}})]);
  const created = requests.filter(item => item.method === 'POST');
  assert.equal(created.length, 1);
  assert.equal(created[0].url, '/api/v1/me/api-keys');
  assert.equal(created[0].headers['X-Satellite-Action'], '1');
  assert.equal(created[0].headers.Authorization, undefined);
  assert.equal(h.element('#cli-key-value').value, secret);
  assert.equal(h.storage.size, 0);
  assert.equal(h.element('#cli-key-list').innerHTML.includes(secret), false);
  h.context.clearCLIKey();
  assert.equal(h.element('#cli-key-value').value, '');
});

test('late CLI Key creation response cannot restore a dismissed Key', async () => {
  const h = harness(product(), schema(false));
  let resolve;
  h.context.fetch = () => new Promise(done => { resolve = done; });
  h.element('#cli-key-name').value = 'Laptop';
  const pending = h.context.createCLIKey({preventDefault() {}});
  h.context.clearCLIKey();
  resolve({ok:true, status:201, json:async () => ({api_key:'satellite_v1_fixture-key'})});
  await pending;
  assert.equal(h.element('#cli-key-value').value, '');
  assert.equal(h.storage.size, 0);
});

test('unified ERA5 profile saves a new Token and clears secret inputs', async () => {
  const h = harness(product(), schema(false));
  h.element('#account-source').value = 'era5';
  h.context.credentialFields();
  assert.match(h.element('#credential-secret-fields').innerHTML, /credential-token/);
  assert.match(h.element('#credential-secret-fields').innerHTML, /type="password"/);
  h.element('#credential-token').value = 'fixture-cds-token';
  h.element('#account-alias').value = 'ERA5';
  h.element('#account-proxy-mode').value = 'direct';
  h.element('#account-enabled').checked = true;
  await h.context.saveAccount({preventDefault() {}});
  const request = h.requests.find(item => item.method === 'POST');
  assert.equal(request.url, '/api/v1/access/profiles');
  assert.deepEqual(JSON.parse(request.body).secret, {token: 'fixture-cds-token'});
  assert.equal(JSON.parse(request.body).source_id, 'era5');
  assert.equal(h.storage.size, 0);
  assert.equal(h.element('#credential-secret-fields').innerHTML, '');
});

test('ERA5 account selector offers only CDS credentials', () => {
  const h = harness(product(), schema(false));
  h.element('#account-source').value = 'era5';
  vm.runInContext(`state.access.credentials = [{id: 4, alias: 'CDS', kind: 'cds'}, {id: 5, alias: 'NASA', kind: 'laads'}];`, h.context);
  h.context.rebuildAccessSelects();
  assert.deepEqual(h.element('#account-credential').options.map(option => option.value), ['', '4']);
});

test('unified profile binds existing CDS token without sending it to the browser', async () => {
  const h = harness(product(), schema(false));
  vm.runInContext(`state.access.credentials = [{id: 7, alias: 'CDS', kind: 'cds'}];`, h.context);
  h.element('#account-source').value = 'era5';
  h.context.rebuildAccessSelects();
  h.element('#account-credential').value = '7';
  h.element('#account-alias').value = 'ERA5';
  h.element('#account-proxy-mode').value = 'direct';
  h.element('#account-enabled').checked = true;
  await h.context.saveAccount({preventDefault() {}});
  const request = h.requests.find(item => item.method === 'POST');
  assert.equal(request.url, '/api/v1/access/profiles');
  assert.equal(JSON.parse(request.body).credential_id, 7);
  assert.deepEqual(JSON.parse(request.body).secret, {});
});

test('each satellite gets a timeline; only confirmed mission ends show END', () => {
  const h = harness(product(), schema(false));
  const rows = vm.runInContext(`satelliteTimelineData([
    {satellite:'CloudSat',metadata:{}},
    {satellite:'GOES-17',metadata:{time_end:'2023-01-04T00:00:00Z'}},
    {satellite:'Terra / Aqua',metadata:{time_start:'2000-01-01T00:00:00Z'}},
    {satellite:'ECMWF',metadata:{temporal_start:'1940-01-01T00:00:00Z'}}
  ])`, h.context);
  assert.equal(rows.length, 4);
  assert.deepEqual([...rows].map(row => row.name), ['Terra', 'Aqua', 'CloudSat', 'GOES-17']);
  assert.equal(rows.find(row => row.name === 'CloudSat').end, '2023-12-20');
  assert.equal(rows.find(row => row.name === 'GOES-17').end, null);
});

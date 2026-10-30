import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';

const app = (await readFile(new URL('./static/app.js', import.meta.url), 'utf8')).replace(/\nboot\(\);\s*$/, '\n');
const css = await readFile(new URL('./static/styles.css', import.meta.url), 'utf8');

function harness() {
  const nodes = new Map(), requests = [];
  const element = key => {
    if (!nodes.has(key)) nodes.set(key, {value:'', innerHTML:'', textContent:'', disabled:false,
      classList:{add(){},remove(){},toggle(){},contains(){return false;}},
      querySelectorAll(){return [];}, addEventListener(){}, showModal(){this.open=true;}, close(){this.open=false;}});
    return nodes.get(key);
  };
  const context = vm.createContext({document:{querySelector:element,querySelectorAll:()=>[]},
    window:{addEventListener(){}}, localStorage:{getItem(){return null;},setItem(){}},
    fetch:async (url,options={}) => {requests.push({url,options}); return {ok:true,json:async()=>({created:true,task:{id:1,downloads:[]}})};},
    setTimeout,clearTimeout,console});
  vm.runInContext(app,context);
  vm.runInContext('loadDownloads = () => {}',context);
  return {context,element,requests,run:source=>vm.runInContext(source,context)};
}

test('shared controls require matching instrument, scan cadence and complete band definitions', () => {
  const h=harness();
  h.run(`state.downloads.catalog=[
    {product_key:'a',satellite:'GOES-16',instrument:'ABI',time_profile:{kind:'scan',cadence_minutes:10},bands:[{number:2,wavelength:'0.64 µm',resolution:'0.5 km'}]},
    {product_key:'b',satellite:'GOES-18',instrument:'ABI',time_profile:{kind:'scan',cadence_minutes:10},bands:[{number:2,wavelength:'0.64 µm',resolution:'0.5 km'}]}];`);
  const capabilities = () => JSON.parse(h.run('JSON.stringify(sharedDownloadCapabilities(state.downloads.catalog))'));
  assert.deepEqual(capabilities(),{time:true,band:true});
  h.run("state.downloads.catalog[1].bands[0].wavelength='0.51 µm'");
  assert.deepEqual(capabilities(),{time:true,band:false});
  h.run("state.downloads.catalog[1].bands[0].wavelength='0.64 µm';state.downloads.catalog[1].bands[0].resolution='1 km'");
  assert.deepEqual(capabilities(),{time:true,band:false});
  h.run("state.downloads.catalog[1].bands[0].resolution='0.5 km';state.downloads.catalog[1].time_profile.cadence_minutes=5");
  assert.deepEqual(capabilities(),{time:false,band:true});
  h.run("state.downloads.catalog[1].instrument='AHI'");
  assert.deepEqual(capabilities(),{time:false,band:false});
  h.run("state.downloads.catalog[1].instrument='ABI';delete state.downloads.catalog[1].bands[0].wavelength");
  assert.equal(capabilities().band,false);
});

test('shared changes update products, while removal clears only removed settings', () => {
  const h=harness();
  h.run(`state.downloads.catalog=['a','b'].map(product_key=>({product_key,instrument:'ABI',time_profile:{kind:'scan',cadence_minutes:10},bands:[{number:2,wavelength:'0.64 µm',resolution:'0.5 km'}]}));state.downloads.selectedProducts=new Set(['a','b']);`);
  h.context.changeSharedDownloadOption('mode','custom');
  h.context.changeSharedDownloadOption('custom','00:10, 12:00');
  h.context.changeSharedDownloadOption('band','2');
  assert.equal(h.run("downloadSettings(state.downloads.catalog[1]).custom"),'00:10, 12:00');
  assert.equal(h.run("downloadSettings(state.downloads.catalog[1]).band"),2);
  h.context.selectDownloadProduct('a',false);
  h.context.selectDownloadProduct('a',true);
  assert.equal(h.run("downloadSettings(state.downloads.catalog[0]).band"),0);
  assert.equal(h.run("downloadSettings(state.downloads.catalog[1]).band"),2);
  assert.match(h.element('#dl-shared-options').innerHTML,/分别设置/);
});

test('visible product names are searchable and selections survive unrelated search', () => {
  const h=harness();
  h.run("state.downloads.catalog=[{product_key:'modis:a',data_type:'MYD06_L2',display_name:'MYD06_L2',product_name:'Cloud properties'},{product_key:'modis:b',data_type:'other'}];state.downloads.selectedProducts.add('modis:b')");
  h.element('#dl-search').value='Cloud properties';
  assert.deepEqual([...h.context.filteredDownloadProducts()].map(p=>p.product_key),['modis:a']);
  assert.equal(h.run("state.downloads.selectedProducts.has('modis:b')"),true);
});

test('period boundaries, invalid dates, and scan cadence are checked before submission', async () => {
  const h=harness();
  const interval=(kind,start,end,extra={})=>h.context.downloadProductInterval({time_profile:{kind,cadence_minutes:10}},{start,end,mode:'all',...extra},'BJT');
  assert.equal(interval('monthly','2024-02','2024-02').end,'2024-03-01T00:00:00.000Z');
  assert.equal(interval('annual','2024','2024').end,'2025-01-01T00:00:00.000Z');
  assert.equal(interval('observation','2024-02-29T00:00','2024-03-01T00:00').start,'2024-02-28T16:00:00.000Z');
  assert.throws(()=>interval('scan','2024-02-30','2024-03-01'));
  assert.throws(()=>interval('scan','2024-02-29','2024-02-29',{mode:'custom',custom:'00:05'}));
  assert.throws(()=>interval('observation','2024-02-29T01:00','2024-02-29T01:00'));
  assert.equal(h.requests.length,0);
});

test('confirmation freezes mixed selections and blocks concurrent submissions', async () => {
  const h=harness();
  h.run("state.downloads.catalog=[{product_key:'scan',time_profile:{kind:'scan',cadence_minutes:10}},{product_key:'orbit',time_profile:{kind:'orbit'}}];state.downloads.selectedProducts=new Set(['scan','orbit'])");
  h.element('#dl-start').value='2024-02-29'; h.element('#dl-end').value='2024-02-29';
  await h.context.submitDownload({preventDefault(){}});
  h.run('state.downloads.selectedProducts.clear()');
  h.element('#dl-start').value='2025-01-01';
  let resolve;
  h.context.fetch=(url,options)=>{h.requests.push({url,options});return new Promise(r=>{resolve=r;});};
  const a=h.context.confirmDownloadSubmission(), b=h.context.confirmDownloadSubmission();
  assert.equal(h.requests.length,1);
  const body=JSON.parse(h.requests[0].options.body);
  assert.deepEqual(body.products,['scan','orbit']);
  assert.equal(body.start,'2024-02-29T00:00:00.000Z');
  resolve({ok:true,json:async()=>({created:true,task:{id:1,downloads:[]}})});
  await Promise.all([a,b]);
  assert.equal(h.element('#download-confirm').open,false);
});

test('queue and product renderers expose native keyboard-operable expanders', () => {
  const h=harness();
  const queue=h.context.downloadQueueCard('task-1',['等待查询',''],'long name','0','details',false);
  assert.match(queue,/<button[^>]+aria-expanded="false"/);
  const product=h.context.downloadProductOptions({product_key:'x',time_profile:{kind:'observation'}});
  assert.match(product,/<details[^>]*>[\s\S]*<summary>/);
  assert.match(product,/展开设置/);
  assert.match(css,/\.download-queue-toggle[^{}]*\{[^}]*\}/);
});

test('timezone changes preserve minute-exact observation overrides through midnight and back', async () => {
  const h=harness();
  h.run(`state.downloads.catalog=[{product_key:'orbit',time_profile:{kind:'orbit'}},
    {product_key:'observation',time_profile:{kind:'observation'}},
    {product_key:'monthly',time_profile:{kind:'monthly'}}];
    state.downloads.selectedProducts=new Set(['orbit','observation','monthly']);
    state.downloads.productOptions.orbit={customTime:true,start:'2024-12-31T20:45',end:'2025-01-01T01:15'};
    state.downloads.productOptions.observation={customTime:true,start:'2024-02-29T23:30',end:'2024-03-01T00:30'};
    state.downloads.productOptions.monthly={customTime:true,start:'2024-02',end:'2024-03'};`);
  h.element('#dl-start').value='2024-02-29'; h.element('#dl-end').value='2024-03-01';
  const snapshot=async()=>{await h.context.submitDownload({preventDefault(){}});return h.run('JSON.parse(JSON.stringify(state.downloads.confirmation.payload.options))');};
  const before=await snapshot();
  h.context.setDownloadTimezone('BJT');
  const after=await snapshot();
  assert.deepEqual(after,before);
  assert.equal(h.element('#dl-start').value,'2024-02-29');
  assert.equal(h.element('#dl-end').value,'2024-03-01');
  assert.equal(h.run("downloadSettings(state.downloads.catalog[0]).start"),'2025-01-01T04:45');
  assert.equal(h.run("downloadSettings(state.downloads.catalog[1]).end"),'2024-03-01T08:30');
  assert.equal(h.run("downloadSettings(state.downloads.catalog[2]).start"),'2024-02');
  h.context.setDownloadTimezone('UTC');
  assert.deepEqual(await snapshot(),before);
});

test('timezone changes preserve exact overridden scan bounds and selected times', async () => {
  const h=harness();
  h.run(`state.downloads.catalog=[{product_key:'scan',instrument:'ABI',time_profile:{kind:'scan',cadence_minutes:10}}];
    state.downloads.selectedProducts.add('scan');
    state.downloads.productOptions.scan={customTime:true,start:'2024-12-31',end:'2024-12-31',mode:'custom',custom:'00:10, 23:50'};`);
  h.element('#dl-start').value='2024-12-31'; h.element('#dl-end').value='2024-12-31';
  const snapshot=async()=>{await h.context.submitDownload({preventDefault(){}});return h.run('JSON.parse(JSON.stringify(state.downloads.confirmation.payload.options.scan))');};
  const before=await snapshot();
  h.context.setDownloadTimezone('BJT');
  assert.deepEqual(await snapshot(),before);
  assert.equal(h.element('#dl-start').value,'2024-12-31');
  assert.equal(h.element('#dl-end').value,'2024-12-31');
  h.context.setDownloadTimezone('UTC');
  assert.deepEqual(await snapshot(),before);
});

test('shared civil dates stay selected across zone changes while UTC periods stay UTC', async () => {
  const h=harness();
  h.run(`state.downloads.catalog=[{product_key:'observation',time_profile:{kind:'observation'}},
    {product_key:'daily',time_profile:{kind:'daily'}}];state.downloads.selectedProducts=new Set(['observation','daily']);`);
  h.element('#dl-start').value='2024-02-29'; h.element('#dl-end').value='2024-02-29';
  const snapshot=async()=>{await h.context.submitDownload({preventDefault(){}});return h.run('JSON.parse(JSON.stringify(state.downloads.confirmation.payload.options))');};
  const before=await snapshot();
  h.context.setDownloadTimezone('BJT');
  const after=await snapshot();
  assert.equal(h.element('#dl-start').value,'2024-02-29');
  assert.equal(h.element('#dl-end').value,'2024-02-29');
  assert.equal(after.observation.start,'2024-02-28T16:00:00.000Z');
  assert.equal(after.observation.end,'2024-02-29T16:00:00.000Z');
  assert.deepEqual(after.daily,before.daily);
});

test('incompatible shared controls are disabled and cannot alter independent selections', () => {
  const h=harness();
  h.run(`state.downloads.catalog=[{product_key:'a',instrument:'ABI',time_profile:{kind:'scan',cadence_minutes:10},bands:[{number:2,wavelength:'0.64 µm',resolution:'0.5 km'}]},
    {product_key:'b',instrument:'AHI',time_profile:{kind:'scan',cadence_minutes:10},bands:[{number:2,wavelength:'0.51 µm',resolution:'1 km'}]}];
    state.downloads.selectedProducts=new Set(['a','b']);`);
  h.context.renderDownloadSelection();
  assert.match(h.element('#dl-shared-options').innerHTML,/data-shared-option="mode"[^>]* disabled/);
  assert.match(h.element('#dl-shared-options').innerHTML,/data-shared-option="band"[^>]* disabled/);
  h.context.changeSharedDownloadOption('mode','custom');
  h.context.changeSharedDownloadOption('band','2');
  assert.equal(h.run("downloadSettings(state.downloads.catalog[0]).mode"),'all');
  assert.equal(h.run("downloadSettings(state.downloads.catalog[1]).band"),0);
});

test('rendering omits absent metadata and uses the chosen band resolution', () => {
  const h=harness();
  h.run(`state.downloads.catalog=[{product_key:'a',satellite:'GOES-18',data_type:'ABI',instrument:'ABI',
    time_profile:{kind:'scan',cadence_minutes:10},bands:[{number:2,wavelength:'0.64 µm',resolution:'0.5 km'},{number:7,wavelength:'3.9 µm',resolution:'2 km'}]}];
    state.downloads.selectedProducts.add('a');state.downloads.productOptions.a={band:2};`);
  const rendered=h.run('downloadProductRow(state.downloads.catalog[0],true)');
  assert.match(rendered,/标称分辨率：0\.5 km/);
  assert.doesNotMatch(rendered,/版本：|区域：|未知|__not_applicable/);
  assert.match(rendered,/展开设置/);
});

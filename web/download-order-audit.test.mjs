import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';

const app = (await readFile(new URL('./static/app.js', import.meta.url), 'utf8')).replace(/\nboot\(\);\s*$/, '\n');
const css = await readFile(new URL('./static/styles.css', import.meta.url), 'utf8');

test('actual granule snapshot retains ID and BJT instant across midnight', async () => {
  const h = harness();
  h.run(`state.downloads.catalog=[{product_key:'viirs:C3860041042-LAADS',source_id:'viirs',available_granules:true,time_profile:{kind:'observation'}}];
    state.downloads.selectedProducts=new Set(['viirs:C3860041042-LAADS']);
    state.downloads.displayZone='BJT';
    state.downloads.productOptions['viirs:C3860041042-LAADS']={granules:{selected:[{id:'viirs:g1',start:'2024-01-01T16:02:30.123Z'}]}};`);
  h.element('#dl-start').value='2024-01-02'; h.element('#dl-end').value='2024-01-02';
  await h.context.submitDownload({preventDefault(){}});
  assert.deepEqual([...h.run("state.downloads.confirmation.payload.options['viirs:C3860041042-LAADS'].granule_ids")], ['viirs:g1']);
  assert.match(h.element('#dl-confirm-summary').innerHTML,/2024-01-02 00:02:30\.123 BJT/);
  h.run("state.downloads.productOptions['viirs:C3860041042-LAADS'].granules.selected=[]");
  assert.equal(h.run("state.downloads.confirmation.payload.options['viirs:C3860041042-LAADS'].granule_ids.length"),1);
});

test('derived GOES observation submits listed group without a scan grid', async () => {
  for (const code of ['ABI-L2-ACHAF','ABI-L2-ACMF']) {
    const h=harness();
    h.run(`state.downloads.catalog=[{product_key:'goes18:${code}',source_id:'goes18',available_granules:true,time_profile:{kind:'observation'}}];state.downloads.selectedProducts=new Set(['goes18:${code}']);state.downloads.productOptions['goes18:${code}']={granules:{selected:[{id:'group',start:'2024-01-01T00:00:20.9Z'}]}}`);
    h.element('#dl-start').value='2024-01-01';h.element('#dl-end').value='2024-01-01';
    assert.match(h.context.downloadProductOptions(h.run('state.downloads.catalog[0]')),/实际可用场景/);
    await h.context.submitDownload({preventDefault(){}});
    assert.deepEqual([...h.run(`state.downloads.confirmation.payload.options['goes18:${code}'].granule_ids`)],['group']);
    assert.deepEqual([...h.run(`state.downloads.confirmation.payload.options['goes18:${code}'].times`)],[]);
    assert.match(h.element('#dl-confirm-summary').innerHTML,/00:00:20\.9 UTC/);
  }
});

test('regional real intervals use UTC query window and overlap selection', async () => {
  const h=harness(); h.context.AbortController=AbortController;h.context.URLSearchParams=URLSearchParams;
  const calls=[];h.context.fetch=async url=>{calls.push(url);return {ok:true,json:async()=>({granules:[{id:'group',start:'2024-01-01T15:59:59.250Z',end:'2024-01-01T16:00:01.500Z'}]})}};
  h.run(`state.downloads.catalog=[{product_key:'himawari9:AHI-L1b-Japan',source_id:'himawari9',available_granules:true,available_granules_window:'10m',time_profile:{kind:'observation'}}];state.downloads.selectedProducts=new Set(['himawari9:AHI-L1b-Japan']);state.downloads.displayZone='BJT'`);
  h.element('#dl-start').value='2024-01-02';h.element('#dl-end').value='2024-01-02';
  await h.context.loadDownloadGranules('himawari9:AHI-L1b-Japan','2024-01-01','00:03');assert.equal(calls.length,0);
  await h.context.loadDownloadGranules('himawari9:AHI-L1b-Japan','2024-01-01','16:00');
  assert.match(calls[0],/time=16%3A00/);
  h.run("state.downloads.productOptions['himawari9:AHI-L1b-Japan'].granules.selected=[...state.downloads.productOptions['himawari9:AHI-L1b-Japan'].granules.items]");
  await h.context.submitDownload({preventDefault(){}});
  assert.deepEqual([...h.run("state.downloads.confirmation.payload.options['himawari9:AHI-L1b-Japan'].granule_ids")],['group']);
  assert.match(h.element('#dl-confirm-summary').innerHTML,/2024-01-01 23:59:59\.25 BJT/);
  assert.match(h.element('#dl-selected-products').innerHTML,/时段（UTC）/);
});

test('stale actual granule response cannot replace the newer date', async () => {
  const h = harness(), pending = [];
  h.context.AbortController = AbortController; h.context.URLSearchParams = URLSearchParams;
  h.context.fetch = () => new Promise(resolve => pending.push(resolve));
  h.run("state.downloads.catalog=[{product_key:'viirs:C3860041042-LAADS',available_granules:true,time_profile:{kind:'observation'}}];state.downloads.selectedProducts=new Set(['viirs:C3860041042-LAADS'])");
  const first = h.context.loadDownloadGranules('viirs:C3860041042-LAADS','2024-01-01');
  const second = h.context.loadDownloadGranules('viirs:C3860041042-LAADS','2024-01-02');
  pending[1]({ok:true,json:async()=>({granules:[{id:'new',start:'2024-01-02T00:00:00Z'}]})}); await second;
  pending[0]({ok:true,json:async()=>({granules:[{id:'old',start:'2024-01-01T00:00:00Z'}]})}); await first;
  assert.equal(h.run("state.downloads.productOptions['viirs:C3860041042-LAADS'].granules.items[0].id"),'new');
});

test('granule response preserves edits made while loading', async () => {
  const h = harness(); let resolve;
  h.context.AbortController=AbortController; h.context.URLSearchParams=URLSearchParams;
  h.context.fetch=()=>new Promise(r=>{resolve=r;});
  h.run(`state.downloads.catalog=[{product_key:'goes18:ABI-L1b-RadF',available_granules:true,time_profile:{kind:'scan',cadence_minutes:10},bands:[{number:2}]}];state.downloads.selectedProducts=new Set(['goes18:ABI-L1b-RadF']);`);
  h.element('#dl-start').value='2024-01-01'; h.element('#dl-end').value='2024-01-01';
  const pending=h.context.loadDownloadGranules('goes18:ABI-L1b-RadF','2024-01-01');
  h.context.changeDownloadProductOption('goes18:ABI-L1b-RadF','band','2');
  h.context.changeDownloadProductOption('goes18:ABI-L1b-RadF','inherit','',false);
  h.context.setDownloadTimezone('BJT');
  resolve({ok:true,json:async()=>({granules:[{id:'g',start:'2024-01-01T00:00:00Z'}]})}); await pending;
  assert.equal(h.run("state.downloads.productOptions['goes18:ABI-L1b-RadF'].band"),2);
  assert.equal(h.run("state.downloads.productOptions['goes18:ABI-L1b-RadF'].customTime"),true);
  assert.equal(h.run("state.downloads.productOptions['goes18:ABI-L1b-RadF'].granules.items[0].id"),'g');
  assert.match(h.element('#dl-selected-products').innerHTML,/查询日期（UTC）/);
});

test('actual selection preserves prior scan mode until explicitly changed', async () => {
  const h=harness();
  h.run(`state.downloads.catalog=[{product_key:'g',available_granules:true,time_profile:{kind:'scan',cadence_minutes:10}}];state.downloads.selectedProducts=new Set(['g']);state.downloads.productOptions.g={mode:'custom',custom:'12:00',granules:{selected:[{id:'g1',start:'2024-01-01T00:00:00Z'}]}}`);
  h.element('#dl-start').value='2024-01-01';h.element('#dl-end').value='2024-01-01';
  await h.context.submitDownload({preventDefault(){}});
  assert.deepEqual([...h.run('state.downloads.confirmation.payload.options.g.granule_ids')],['g1']);
  assert.equal(h.run('state.downloads.productOptions.g.custom'),'12:00');
  h.run('state.downloads.productOptions.g.granules.selected=[]');
  await h.context.submitDownload({preventDefault(){}});
  assert.deepEqual([...h.run('state.downloads.confirmation.payload.options.g.times')],['12:00']);
  h.run("state.downloads.productOptions.g.granules.selected=[{id:'g1',start:'2024-01-01T00:00:00Z'}]");
  h.context.changeDownloadProductOption('g','mode','hourly');
  assert.equal(h.run('state.downloads.productOptions.g.granules.selected.length'),0);
});

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

test('scientific time semantics appear in facets, product cards and confirmation without inventing scans', async () => {
  const h=harness();
  h.run(`state.downloads.catalog=[
    {product_key:'glm',data_type:'GLM-L2-LCFA',time_profile:{kind:'observation',semantic:'event',semantic_label:'闪电事件'}},
    {product_key:'li',data_type:'LI',time_profile:{kind:'observation',semantic:'accumulation',semantic_label:'闪电累计'}},
    {product_key:'half',data_type:'GEOLEO',time_profile:{kind:'observation',semantic:'composite',semantic_label:'半小时合成',window_minutes:30}},
    {product_key:'unknown',time_profile:{kind:'observation',label:'数据时间'}}
  ];state.downloads.selectedProducts=new Set(['glm','li','half']);`);
  h.element('#dl-start').value='2024-02-29';h.element('#dl-end').value='2024-02-29';
  h.context.refreshDownloadFilters();
  assert.match(h.element('#dl-filters').innerHTML,/事件时间/);
  assert.match(h.element('#dl-filters').innerHTML,/累计时段/);
  assert.match(h.element('#dl-product-results').innerHTML,/半小时合成/);
  assert.doesNotMatch(h.element('#dl-selected-products').innerHTML,/data-option-field="mode"|全部扫描/);
  assert.equal(h.context.downloadTimeLabel({time_profile:{kind:'observation',label:'数据时间'}}),'时间范围');
  for (const [key, label] of [['glm','事件时间'],['li','累计时间'],['half','合成时间']]) {
    h.context.changeDownloadProductOption(key,'inherit','',false);
    const product=h.run(`state.downloads.catalog.find(p=>p.product_key==='${key}')`);
    const editor=h.context.downloadProductOptions(product);
    assert.match(editor,new RegExp(label+'从'));
    assert.doesNotMatch(editor,/时间与波段|data-option-field="mode"/);
  }
  await h.context.submitDownload({preventDefault(){}});
  assert.match(h.element('#dl-confirm-summary').innerHTML,/半小时合成/);
  assert.match(h.element('#dl-confirm-summary').innerHTML,/闪电累计/);
  assert.equal(h.requests.length,0);
});

test('same nominal cadence with different selection steps or ownership cannot share times', () => {
  const h=harness();
  h.run(`state.downloads.catalog=['a','b'].map(product_key=>({product_key,instrument:'ABI',time_profile:{kind:'scan',cadence_minutes:10,selection_minutes:5}}));state.downloads.selectedProducts=new Set(['a','b']);`);
  assert.equal(h.context.sharedDownloadCapabilities().time,true);
  h.run('state.downloads.catalog[1].time_profile.selection_minutes=10');
  assert.equal(h.context.sharedDownloadCapabilities().time,false);
  h.context.changeSharedDownloadOption('mode','hourly');
  assert.equal(h.run('Object.keys(state.downloads.productOptions).length'),0);
  h.run("state.downloads.catalog[1].time_profile.selection_minutes=5;state.downloads.catalog[1].time_profile.selection_basis='file_start'");
  assert.equal(h.context.sharedDownloadCapabilities().time,false);
});

test('time chips normalize, validate and remove explicit selections without expanding to every scan', async () => {
  const h=harness();
  h.run(`state.downloads.catalog=['a','b'].map(product_key=>({product_key,instrument:'ABI',time_profile:{kind:'scan',cadence_minutes:10,selection_minutes:5}}));state.downloads.selectedProducts=new Set(['a','b']);`);
  h.element('#dl-start').value='2024-02-29';h.element('#dl-end').value='2024-02-29';
  h.context.changeDownloadTimeSelection('a','00:05');
  h.context.changeDownloadTimeSelection('a','00:05');
  h.context.changeDownloadTimeSelection('a','23:55');
  assert.equal(h.run("downloadSettings(state.downloads.catalog[0]).custom"),'00:05, 23:55');
  assert.equal(h.run("downloadSettings(state.downloads.catalog[1]).mode"),'all');
  h.context.changeDownloadTimeSelection('a','00:03');
  assert.match(h.element('#dl-submit-status').textContent,/5 分钟/);
  assert.equal(h.run("downloadSettings(state.downloads.catalog[0]).custom"),'00:05, 23:55');
  h.context.changeDownloadTimeSelection('', '12:00', false, true);
  assert.equal(h.run("downloadSettings(state.downloads.catalog[1]).custom"),'00:05, 12:00, 23:55');
  h.context.changeDownloadTimeSelection('a','00:05',true);
  h.context.changeDownloadTimeSelection('a','12:00',true);
  h.context.changeDownloadTimeSelection('a','23:55',true);
  assert.equal(h.run("downloadSettings(state.downloads.catalog[0]).mode"),'custom');
  await h.context.submitDownload({preventDefault(){}});
  assert.match(h.element('#dl-submit-status').textContent,/至少填一个/);
  assert.equal(h.requests.length,0);
});

test('timezone changes preserve each valid bound and time chip while other fields are incomplete', () => {
  const h=harness();
  h.run(`state.downloads.catalog=[{product_key:'scan',time_profile:{kind:'scan',cadence_minutes:10,selection_minutes:5}}];
    state.downloads.selectedProducts.add('scan');
    state.downloads.productOptions.scan={customTime:true,start:'2024-12-31T20:00',end:'2024-12-31T21:00',mode:'custom',custom:''};`);
  h.context.setDownloadTimezone('BJT');
  assert.equal(h.run('state.downloads.productOptions.scan.start'),'2025-01-01T04:00');
  assert.equal(h.run('state.downloads.productOptions.scan.end'),'2025-01-01T05:00');
  h.context.setDownloadTimezone('UTC');
  assert.equal(h.run('state.downloads.productOptions.scan.start'),'2024-12-31T20:00');
  h.run("state.downloads.productOptions.scan.end='';state.downloads.productOptions.scan.custom='20:05'");
  h.context.setDownloadTimezone('BJT');
  assert.equal(h.run('state.downloads.productOptions.scan.start'),'2025-01-01T04:00');
  assert.equal(h.run('state.downloads.productOptions.scan.end'),'');
  assert.equal(h.run('state.downloads.productOptions.scan.custom'),'04:05');
  h.context.setDownloadTimezone('UTC');
  assert.equal(h.run('state.downloads.productOptions.scan.custom'),'20:05');
  h.run("state.downloads.productOptions.scan.start='2024-02-30T00:00';state.downloads.productOptions.scan.end='2024-03-01T00:00'");
  h.context.setDownloadTimezone('BJT');
  assert.equal(h.run('state.downloads.productOptions.scan.start'),'2024-02-30T00:00');
  assert.equal(h.run('state.downloads.productOptions.scan.end'),'2024-03-01T08:00');
  assert.equal(h.run('state.downloads.productOptions.scan.custom'),'04:05');
});

test('regional scans and event ranges retain second precision through payload and timezone round trips', async () => {
  const h=harness();
  h.run(`state.downloads.catalog=[{product_key:'regional',time_profile:{kind:'observation',semantic:'scanning',semantic_label:'区域扫描'}}];
    state.downloads.selectedProducts.add('regional');
    state.downloads.productOptions.regional={customTime:true,start:'2024-12-31T23:57:30',end:'2025-01-01T00:02:30'};`);
  h.context.setDownloadTimezone('BJT');
  assert.equal(h.run('state.downloads.productOptions.regional.start'),'2025-01-01T07:57:30');
  await h.context.submitDownload({preventDefault(){}});
  const payload=JSON.parse(h.run('JSON.stringify(state.downloads.confirmation.payload)'));
  assert.equal(payload.options.regional.start,'2024-12-31T23:57:30.000Z');
  assert.equal(payload.options.regional.end,'2025-01-01T00:02:30.000Z');
  assert.match(h.element('#dl-confirm-summary').innerHTML,/07:57:30/);
  assert.equal(h.context.downloadTimestamp(payload.options.regional.end,'BJT',true,true),'2025-01-01 08:02:30 BJT');
  const details=h.context.downloadTaskDetails({downloads:[{start:payload.options.regional.start,end:payload.options.regional.end}],created_at:'2025-01-01T00:00:00Z'});
  assert.match(details,/08:02:30/);
  h.context.setDownloadTimezone('UTC');
  assert.equal(h.run('state.downloads.productOptions.regional.end'),'2025-01-01T00:02:30');
  assert.equal(h.requests.length,0);
});

test('a typed time must be added or cleared before confirmation', async () => {
  const h=harness();
  h.run("state.downloads.catalog=[{product_key:'scan',time_profile:{kind:'scan',cadence_minutes:10}}];state.downloads.selectedProducts.add('scan')");
  h.element('#dl-start').value='2024-02-29';h.element('#dl-end').value='2024-02-29';
  h.context.document.querySelectorAll=selector=>selector==='[data-download-slot]'?[{value:'12:00'}]:[];
  await h.context.submitDownload({preventDefault(){}});
  assert.match(h.element('#dl-submit-status').textContent,/请先添加已输入的时次/);
  assert.equal(h.run('state.downloads.confirmation'),null);
  assert.equal(h.requests.length,0);
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

test('legacy requests share paging and keep expansion separate from same-number groups', () => {
  const h=harness();
  h.run(`state.downloads.displayZone='UTC'; expandedDownloadTasks.add('legacy:7');`);
  const child={source_id:'modis',satellite:'Aqua',product_code:'MYD06',start:'2024-01-01T00:00:00Z',end:'2024-01-02T00:00:00Z',state:'transferring',assets_queued:9,files_done:2};
  h.context.applyDownloadPage({tasks:[
    {id:7,kind:'group',key:'group:7',state:'done',assets_queued:4,files_done:4,downloads:[{...child,state:'done',assets_queued:4,files_done:4}]},
    {id:7,request_id:7,kind:'legacy',key:'legacy:7',state:'transferring',assets_queued:9,files_done:2,downloads:[child]},
  ],page:2,pages:2,total:32});
  const html=h.element('#download-tasks').innerHTML;
  assert.match(html,/data-queue-key="group:7"/);assert.match(html,/data-queue-key="legacy:7"/);
  assert.match(html,/历史请求 #7/);assert.match(html,/传输中/);assert.match(html,/已发现 9 · 已完成 2/);
  assert.match(html,/download-queue-card expanded" data-queue-key="legacy:7"/);
  assert.doesNotMatch(html,/download-queue-card expanded" data-queue-key="group:7"/);
  assert.doesNotMatch(html,/href=|\/downloads\/batch/);
  assert.equal(h.element('#download-page-current').textContent,'第 2 / 2 页');
  assert.equal(h.element('#download-page-prev').disabled,false);assert.equal(h.element('#download-page-next').disabled,true);
  h.context.renderDownloadTasks(h.run('state.downloads.tasks'));
  assert.match(h.element('#download-tasks').innerHTML,/download-queue-card expanded" data-queue-key="legacy:7"/);
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

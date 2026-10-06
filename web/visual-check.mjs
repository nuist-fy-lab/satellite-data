import assert from 'node:assert/strict';
import {readFile, mkdir, writeFile} from 'node:fs/promises';
import {createServer} from 'node:http';
import {createRequire} from 'node:module';

const require = createRequire(import.meta.url);
const {Builder} = require('selenium-webdriver');
const firefox = require('selenium-webdriver/firefox');
const app = (await readFile(new URL('./static/app.js', import.meta.url), 'utf8')).replace(/\nboot\(\);\s*$/, '\n');
// Synthetic data exercises the real renderers without accounts or upstream APIs.
const fixture = `
window.styleFixture = function(view, mode, wide) {
  document.querySelectorAll('.view').forEach(node => node.classList.toggle('active', node.id === view));
  document.querySelectorAll('[data-view]').forEach(node => node.classList.toggle('active', node.dataset.view === view));
  setSidebar(wide);
  const bands = Array.from({length:16}, (_, i) => i + 1);
  const product = {id:'goes18:ABI-L1b-RadF',source_id:'goes18',product:'ABI-L1b-RadF',satellite:'GOES-18',cadence:'10m',metadata:{}};
  state.selected = product;
  state.collections = [product];
  $('#product-title').textContent = 'GOES-18 · ABI-L1b-RadF';
  $('#product-meta').textContent = 'ABI · L1b · Full disk · UTC';
  state.access.user = {username:'operator', role:'admin'};
  $('#portal-user').textContent = 'operator';
  $('#service-label').textContent = '服务正常';
  $('#service-dot').classList.add('ok');
  for (const node of document.querySelectorAll('select')) {
    if (!node.options.length) node.add(new Option('全部', 'all'));
  }
  yearEl.replaceChildren(new Option('2023','2023'));
  monthEl.replaceChildren(new Option('6 月','5'));
  sourceEl.replaceChildren(new Option('GOES-18','goes18'));
  satelliteEl.replaceChildren(new Option('GOES-18','GOES-18'));
  instrumentEl.replaceChildren(new Option('ABI','ABI'));
  levelEl.replaceChildren(new Option('L1b','L1b'));
  productEl.replaceChildren(new Option('ABI-L1b-RadF','goes18:ABI-L1b-RadF'));
  const totals = bands.map(band => [band,144,band===6?24:0,24,band===6?24:0,0,0,0,0,0,0]);
  const hours = Array.from({length:24}, (_, hour) => [hour,16,1,0,0,0]);
  const coverage = state.coverage;
  Object.assign(coverage,{supported:true,bands,view:mode,time:'all',band:0,day:'2023-06-14',hour:0});
  coverage.yearDays = new Map(Array.from({length:30}, (_, i) => ['2023-06-'+String(i+1).padStart(2,'0'),[151+i,'missing',2304,24,384,24,0,2280,0,0,false,'',totals,2,'grid']]));
  coverage.month = {days:Array.from({length:30}, (_, i) => [i+1,2,false,'grid',totals,hours])};
  const times = Array.from({length:144}, (_, i) => ['2023-06-14T'+String(Math.floor(i/6)).padStart(2,'0')+':'+String(i%6*10).padStart(2,'0')+':21Z',i*10]);
  const cells = times.flatMap((_, i) => bands.map(band => [i,band,1,band===6 && i%6===0?1:0,0,0]));
  coverage.dayData = {mode:'grid',cadence:10,official_state:2,indexing:false,times,cells};
  coverage.cells = new Map(cells.map(cell => [cell[0]+':'+cell[1],cell]));
  renderSatelliteTimelines(); renderCoverageShell(); renderCoverageYear(); renderCoverageMonth(); renderCoverageDay();
  state.sources = [{name:'GOES-18',tier:'anonymous',description:'ABI full disk',method:'NOAA S3'}, {name:'ERA5 / ERA5-Land',tier:'token',description:'ECMWF reanalysis',method:'CDS asynchronous request'}];
  renderSources();
  state.access.credentials = [{id:1,alias:'ERA5 primary',kind:'cds',state:'ready',enabled:true}];
  state.access.accounts = [{id:1,alias:'ERA5 primary',source_id:'era5',credential_id:1,proxy_mode:'direct',state:'ready',enabled:true}];
  state.access.proxies = [{id:1,alias:'US West',scheme:'socks5',endpoint_hint:'proxy.example:1080',providers:['era5','mtg'],state:'ready',enabled:true}];
  renderAccountList(); renderProxyList(); rebuildAccessSelects();
  const points = Array.from({length:24}, (_, i) => ({at:new Date(Date.now()-(23-i)*150000).toISOString(),latency_ms:170+i%5*10,jitter_ms:4+i%3,loss_pct:0}));
  state.monitor.payload = {origin:'c2ln1',links:['GOES-18','EarthCARE','ERA5','MTG'].map((name,i) => ({name,source_id:['goes18','earthcare','era5','mtg'][i],target:['noaa-goes18.s3.amazonaws.com','earthcare.example','cds.climate.copernicus.eu','api.eumetsat.int'][i],port:443,latest:points.at(-1),points}))};
  renderMonitor();
  state.downloads.displayZone = 'UTC';
  state.downloads.history.displayZone = 'BJT';
  state.downloads.history.chartKey = '';
  state.downloads.requests = [{id:20,collection_id:'goes18:ABI-L1b-RadF',state:'done',files_done:720,assets_queued:720,
    start:'2026-12-31T20:00:00Z',end:'2027-01-01T02:00:00Z',band:6,times_count:24}];
  const transferPoints = Array.from({length:24}, (_, i) => ({sampled_at:new Date(Date.now()-(23-i)*150000).toISOString(),bytes_per_second:1048576,active_transfers:2,adaptive_concurrency:4}));
  state.downloads.providers = [{source_id:'goes',enabled:true,bytes_per_second:1048576,active_transfers:2,adaptive_concurrency:4,states:{done:720}}];
  state.downloads.history.payload = {window:'1h',generated_at:new Date().toISOString(),bucket_seconds:15,
    providers:[{source_id:'goes',latest:transferPoints.at(-1),points:transferPoints}]};
  syncDownloadTimezones();
  renderDownloadRequests(state.downloads.requests);
  renderDownloadProviders(state.downloads.providers, true);
  if (mode === 'form') openPoolForm('account', state.access.accounts[0]);
  if (mode === 'era5') {
    $('#era5-panel').classList.remove('hidden');
    $('#coverage-tools').classList.add('hidden');
    $('#year-layout').classList.add('hidden');
    $('#day-coverage').classList.add('hidden');
    $('#era5-variables').replaceChildren(new Option('2m_temperature','2m_temperature'),new Option('total_precipitation','total_precipitation'));
  }
};
window.coverageTimelineStability = function() {
  const timeline = document.querySelector('#satellite-timelines');
  const supportedTop = timeline.getBoundingClientRect().top;
  state.coverage.supported = false;
  renderCoverageShell();
  const limitedTop = timeline.getBoundingClientRect().top;
  return {supportedTop, limitedTop};
};
window.calendarSizingFixture = function(source, product, supported, year) {
  state.selected = {id:source+':'+product,source_id:source,product,satellite:source,cadence:'10m',metadata:{}};
  state.collections = [state.selected];
  state.coverage.supported = supported;
  state.coverage.bands = Array.from({length:16}, (_, i) => i+1);
  yearEl.replaceChildren(new Option(String(year),String(year)));
  renderSatelliteTimelines();renderCalendar();
  const grid=$('#year-grid'),wrap=$('.calendar-wrap'),cell=grid.querySelector('[data-day]');
  const box=grid.getBoundingClientRect(),rect=cell.getBoundingClientRect();
  const gap=parseFloat(getComputedStyle(grid).columnGap);
  const timeline=$('#satellite-timelines').getBoundingClientRect();
  return {available:wrap.clientWidth,width:box.width,cellWidth:rect.width,cellHeight:rect.height,gap,
    columns:yearGridColumns(year),timelineWidth:timeline.width,timelineLeft:timeline.left,timelineRight:timeline.right,
    gridLeft:box.left,gridRight:box.right,
    axisLeft:$('.calendar-axis').getBoundingClientRect().left,sidebarRight:$('.sidebar').getBoundingClientRect().right,
    scroll:document.documentElement.scrollWidth,viewport:innerWidth};
};
bind();
window.styleFixtureReady = true;
`;
const assets = new Map();
for (const file of ['index.html','login.html','styles.css','login.js']) assets.set(file, await readFile(new URL('./static/'+file, import.meta.url)));
assets.set('app.js', Buffer.from(app+fixture));
const server = createServer((req, res) => {
  const path = new URL(req.url,'http://localhost').pathname;
  const file = path==='/'?'index.html':path==='/login'?'login.html':path.slice(1);
  if (!assets.has(file)) {res.writeHead(404);res.end();return;}
  res.setHeader('Content-Type',file.endsWith('.js')?'text/javascript':file.endsWith('.css')?'text/css':'text/html');
  res.end(assets.get(file));
});
await new Promise(resolve => server.listen(0,'127.0.0.1',resolve));
const base = 'http://127.0.0.1:'+server.address().port;
await mkdir('dist/visual',{recursive:true});
let driver;
try {
  const options = new firefox.Options().addArguments('-headless');
  if (process.env.FIREFOX_BIN) options.setBinary(process.env.FIREFOX_BIN);
  driver = await new Builder().forBrowser('firefox').setFirefoxOptions(options).build();
  await driver.manage().setTimeouts({pageLoad:15000,script:15000});
  const cases = [['calendar-view','year'],['calendar-view','month'],['calendar-view','day'],['calendar-view','era5'],['sources-view','year'],['downloads-view','year'],['sync-view','year'],['monitor-view','year'],['cli-view','year'],['access-view','year'],['access-view','form']];
  for (const width of [320,390,1440,1920]) {
    await driver.manage().window().setRect({width,height:900});
    await driver.get(base+'/login');
    const loginSize = await driver.executeScript('return document.querySelector(".login-card").getBoundingClientRect().width');
    assert.equal(loginSize,280);
    await writeFile('dist/visual/login-'+width+'.png',Buffer.from(await driver.takeScreenshot(),'base64'));
    await driver.get(base);
    await driver.wait(() => driver.executeScript('return window.styleFixtureReady===true'),10000);
    await driver.executeScript('window.styleFixture("calendar-view","year",arguments[0])',width>820);
    const calendarCardWidth = await driver.executeScript('return document.querySelector("#calendar-view>.calendar-card").getBoundingClientRect().width');
    assert.ok(calendarCardWidth <= 944.1, 'calendar card exceeds 80% desktop cap: '+calendarCardWidth);
    if (width >= 1440) assert.ok(Math.abs(calendarCardWidth-944) < 1, 'calendar card desktop cap not applied: '+calendarCardWidth);
    const timelineStability = await driver.executeScript('return window.coverageTimelineStability()');
    assert.equal(timelineStability.limitedTop,timelineStability.supportedTop,'coverage capability changed timeline vertical position');
    for (const [source,product,supported] of [['goes18','ABI-L1b-RadF',true],['goes18','GLM-L2-LCFA',false],
      ['himawari9','AHI-L1b-FLDK',true],['earthcare','ACM_CAP_2B',false],['cloudsat','2B-GEOPROF',false],
      ['modis','MOD021KM',false],['viirs','VNP02MOD',false],['mtg','FCI-L1c',true]]) {
      for (const year of [2026,2000]) {
        const size=await driver.executeScript('return window.calendarSizingFixture(...arguments)',source,product,supported,year);
        assert.ok(Math.abs(size.cellWidth-size.cellHeight)<.1,product+' cells not square');
        assert.ok(Math.abs(size.cellWidth-size.gap*4)<.1,product+' spacing not proportional');
        assert.ok(size.scroll<=size.viewport+1,product+' year calendar overflows page');
        assert.ok(size.axisLeft>=size.sidebarRight-.5,product+' weekday labels overlap sidebar');
        if (size.available>=size.columns*12+(size.columns-1)*3) {
          assert.ok(Math.abs(size.width-size.available)<1,product+' does not fill year width: '+JSON.stringify(size));
          assert.ok(Math.abs(size.timelineWidth-size.width)<1,product+' year is not timeline section width');
          assert.ok(Math.abs(size.timelineRight-size.gridRight)<1,product+' year/timeline right edges differ');
          assert.ok(Math.abs(size.timelineLeft-size.gridLeft)<1,product+' year/timeline left edges differ');
        } else assert.ok(size.cellWidth>=11.9,product+' mobile cells became unreadable');
      }
    }
    for (const [view,mode] of cases) {
      await driver.executeScript('window.styleFixture(arguments[0],arguments[1],arguments[2])',view,mode,width>820);
      const metrics = await driver.executeScript(`
        const bad = [];
        for (const node of document.querySelectorAll('.view.active button,.view.active input,.view.active select')) {
          const box = node.getBoundingClientRect();
          if (box.width && box.height && box.width < 8) bad.push(node.id||node.className);
        }
        return {width:innerWidth,scroll:document.documentElement.scrollWidth,bad};
      `);
      assert.ok(metrics.scroll<=metrics.width+1,view+' '+mode+' overflows: '+JSON.stringify(metrics));
      assert.deepEqual(metrics.bad,[],view+' has collapsed controls');
      if (view === 'downloads-view') {
        const initial = await driver.executeScript(`const $=selector=>document.querySelector(selector);return {
          task:$('#download-timezone').value,monitor:$('#download-monitor-timezone').value,
          request:$('#download-requests').textContent,
          axis:[...document.querySelectorAll('#download-chart-rate .plot-time span')].map(node=>node.textContent)
        }`);
        assert.equal(initial.task, 'UTC');
        assert.equal(initial.monitor, 'BJT');
        assert.match(initial.request, /2026-12-31 20:00 UTC/);
        assert.ok(initial.axis.length === 2 && initial.axis.every(value => value.endsWith(' BJT')));
        const changed = await driver.executeScript(`
          const $=selector=>document.querySelector(selector);
          $('#download-timezone').value='BJT';
          $('#download-timezone').dispatchEvent(new Event('change'));
          return {request:$('#download-requests').textContent,
            label:$('#dl-start-label').textContent,
            axis:[...document.querySelectorAll('#download-chart-rate .plot-time span')].map(node=>node.textContent)};
        `);
        assert.match(changed.request, /2027-01-01 04:00 BJT/);
        assert.equal(changed.label, '开始日期（BJT）');
        assert.deepEqual(changed.axis, initial.axis);
        const monitorChanged = await driver.executeScript(`
          const $=selector=>document.querySelector(selector);
          $('#download-monitor-timezone').value='UTC';
          $('#download-monitor-timezone').dispatchEvent(new Event('change'));
          return {request:$('#download-requests').textContent,
            axis:[...document.querySelectorAll('#download-chart-rate .plot-time span')].map(node=>node.textContent)};
        `);
        assert.equal(monitorChanged.request, changed.request);
        assert.ok(monitorChanged.axis.every(value => value.endsWith(' UTC')));
        await driver.executeScript(`
          for(const [id,value] of [['download-timezone','UTC'],['download-monitor-timezone','BJT']]) {
            const node=document.getElementById(id);node.value=value;node.dispatchEvent(new Event('change'));
          }
        `);
      }
      if (['month','era5','form'].includes(mode)||view==='monitor-view'||view==='downloads-view'||(view==='calendar-view'&&mode==='year')) {
        await writeFile('dist/visual/'+view+'-'+mode+'-'+width+'.png',Buffer.from(await driver.takeScreenshot(),'base64'));
      }
      console.log('Layout OK:',width,view,mode);
    }
  }
} finally {
  try {if(driver) await driver.quit();}
  finally {await new Promise(resolve => server.close(resolve));}
}

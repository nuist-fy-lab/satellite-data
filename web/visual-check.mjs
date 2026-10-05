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
  $('#download-requests').innerHTML = '<div class="download-request"><strong>#20 · goes18:ABI-L1b-RadF</strong><span>已完成 · 720/720</span><span>2023-06-01 → 2023-06-30 · C06 · 整点</span></div>';
  if (mode === 'form') openPoolForm('account', state.access.accounts[0]);
  if (mode === 'era5') {
    $('#era5-panel').classList.remove('hidden');
    $('#coverage-tools').classList.add('hidden');
    $('#year-layout').classList.add('hidden');
    $('#day-coverage').classList.add('hidden');
    $('#era5-variables').replaceChildren(new Option('2m_temperature','2m_temperature'),new Option('total_precipitation','total_precipitation'));
  }
};
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
  for (const width of [390,1440]) {
    await driver.manage().window().setRect({width,height:900});
    await driver.get(base+'/login');
    const loginSize = await driver.executeScript('return document.querySelector(".login-card").getBoundingClientRect().width');
    assert.equal(loginSize,280);
    await writeFile('dist/visual/login-'+width+'.png',Buffer.from(await driver.takeScreenshot(),'base64'));
    await driver.get(base);
    await driver.wait(() => driver.executeScript('return window.styleFixtureReady===true'),10000);
    await driver.executeScript('window.styleFixture("calendar-view","year",arguments[0])',width>820);
    const supportedTimelineTop = await driver.executeScript('return document.querySelector("#satellite-timelines").getBoundingClientRect().top');
    const limitedTimelineTop = await driver.executeScript("state.coverage.supported=false;renderCoverageShell();return document.querySelector('#satellite-timelines').getBoundingClientRect().top");
    assert.equal(limitedTimelineTop,supportedTimelineTop,'coverage capability changed timeline vertical position');
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
      if (['month','era5','form'].includes(mode)||view==='monitor-view') {
        await writeFile('dist/visual/'+view+'-'+mode+'-'+width+'.png',Buffer.from(await driver.takeScreenshot(),'base64'));
      }
      console.log('Layout OK:',width,view,mode);
    }
  }
} finally {
  try {if(driver) await driver.quit();}
  finally {await new Promise(resolve => server.close(resolve));}
}

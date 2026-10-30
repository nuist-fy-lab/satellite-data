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
window.styleFixture = async function(view, mode, wide) {
  state.downloads.catalogReady = true;
  state.downloads.catalog = [
    {product_key:'goes18:ABI-L1b-RadF',source_id:'goes18',satellite:'GOES-18',instrument:'ABI',level:'L1B',data_type:'ABI-L1b-RadF',display_name:'Full disk radiance',area_type:'DISK',resolution:'0.5 / 1 / 2 km',bands:[]},
    {product_key:'modis:terra',source_id:'modis',satellite:'Terra',instrument:'MODIS',level:'L2',data_type:'MOD06_L2',display_name:'Cloud properties',area_type:'SWATH',resolution:'1 km',bands:[]},
    {product_key:'modis:aqua',source_id:'modis',satellite:'Aqua',instrument:'MODIS',level:'L2',data_type:'MYD06_L2',display_name:'Cloud properties',area_type:'SWATH',resolution:'1 km',bands:[]}
  ];
  state.downloads.selectedProducts = new Set(['goes18:ABI-L1b-RadF','modis:aqua']);
  stopIndexStream(); state.satelliteYears.clear();
  $('#satellite-year-calendars').replaceChildren(); $('#year-product-heading').classList.add('hidden');
  setView(view);
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
    if (!node.options.length && node.id !== 'coverage-hour') node.add(new Option('全部', 'all'));
  }
  yearEl.replaceChildren(new Option('2023','2023'));
  monthEl.value = '5'; // Hidden month-view state; no homepage month filter.
  sourceEl.replaceChildren(new Option('GOES-18','goes18'));
  satelliteEl.replaceChildren(new Option('GOES-18','GOES-18'));
  instrumentEl.replaceChildren(new Option('ABI','ABI'));
  levelEl.replaceChildren(new Option('L1b','L1b'));
  fillMetadataSelect(areaEl,state.collections,'area_type');
  fillMetadataSelect(resolutionEl,state.collections,'resolution');
  productEl.replaceChildren(new Option('ABI-L1b-RadF','goes18:ABI-L1b-RadF'));
  const totals = bands.map(band => [band,144,band===6?24:0,24,band===6?24:0,0,0,0,0,0,0]);
  const hours = Array.from({length:24}, (_, hour) => [hour,16,1,0,0,0]);
  const coverage = state.coverage;
  Object.assign(coverage,{supported:true,bands,view:mode,time:'all',band:0,day:'2023-06-14',hour:-1});
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
  state.downloads.tasks = [{id:20,state:'done',products:1,files_done:720,assets_queued:720,created_at:'2026-10-30T00:00:00Z',downloads:[{
    id:1,collection_id:'goes18:ABI-L1b-RadF',satellite:'GOES-18',product_code:'ABI-L1b-RadF',product_name:'Full disk radiance',state:'done',files_done:720,assets_queued:720,
    start:'2026-12-31T20:00:00Z',end:'2027-01-01T02:00:00Z',selection:{band:6,times:['00:00','12:00']}}]}];
  const transferPoints = Array.from({length:24}, (_, i) => ({sampled_at:new Date(Date.now()-(23-i)*150000).toISOString(),bytes_per_second:1048576,active_transfers:2,adaptive_concurrency:4}));
  state.downloads.providers = [{source_id:'goes',enabled:true,bytes_per_second:1048576,active_transfers:2,adaptive_concurrency:4,states:{done:720}}];
  state.downloads.history.payload = {window:'1h',generated_at:new Date().toISOString(),bucket_seconds:15,
    providers:[{source_id:'goes',latest:transferPoints.at(-1),points:transferPoints}]};
  syncDownloadTimezones();
  renderDownloadTasks(state.downloads.tasks);
  refreshDownloadFilters();
  $('#dl-start').value='2026-12-31';$('#dl-end').value='2027-01-02';
  renderDownloadProviders(state.downloads.providers, true);
  if (mode === 'form') openPoolForm('account', state.access.accounts[0]);
  if (view === 'era5-view') {
    const download=mode==='download';
    document.querySelectorAll('[data-era5-tab]').forEach(node=>{node.classList.toggle('active',node.dataset.era5Tab===(download?'download':'inventory'));node.setAttribute('aria-selected',String(node.classList.contains('active')));});
    $('#era5-panel').classList.toggle('hidden',!download);
    $('#era5-inventory-panel').classList.toggle('hidden',download);
    $('#era5-dataset').replaceChildren(new Option('ERA5 hourly pressure levels','era5:pressure'));
    $('#era5-pressure').replaceChildren(new Option('850','850'),new Option('500','500'));
    $('#era5-pressure-field').classList.remove('hidden');
    $('#era5-status').textContent='';
    $('#era5-date').value='2024-01-01';
    $('#era5-variables').replaceChildren(new Option('2m_temperature','2m_temperature'),new Option('total_precipitation','total_precipitation'));
  }
  if (view === 'catalog-view') {
    const sources=mode==='sources';
    $('#catalog-products-panel').classList.toggle('hidden',sources);
    $('#catalog-sources-panel').classList.toggle('hidden',!sources);
    $('.catalog-search').classList.toggle('hidden',sources);
  }
  // The sidebar animates for 180ms; measure only after its layout settles.
  await new Promise(resolve=>setTimeout(resolve,250));
};
window.downloadConfirmationFixture = function(open) {
  if (!open) {closeDownloadConfirmation(); return {open:$('#download-confirm').open};}
  submitDownload({preventDefault(){}});
  const dialog=$('#download-confirm'),box=dialog.getBoundingClientRect();
  return {open:dialog.open,content:$('#dl-confirm-summary').textContent,left:box.left,right:box.right,
    width:innerWidth,scroll:dialog.scrollWidth,client:dialog.clientWidth};
};
window.inventoryFixture = async function() {
  const module=await import('/era5-inventory.js');
  const files=[{dataset:'era5:reanalysis-era5-single-levels',name:'temperature_january.nc',bytes:25171,verified:true,state:'ok',start:'2024-01-01T00:00:00Z',end:'2024-02-01T00:00:00Z',variables:['2m_temperature','total_precipitation'],levels:[],times:['00:00','12:00'],area:[32.25,118.5,32,118.75]},
    {dataset:'era5:reanalysis-era5-pressure-levels',name:'pressure.nc',bytes:65536,verified:true,state:'ok',start:'2024-02-01T00:00:00Z',end:'2024-03-01T00:00:00Z',variables:['temperature'],levels:['850','500'],times:['00:00'],area:[90,-180,-90,180]},
    {dataset:'era5:unclassified',name:'historical_unclassified.nc',bytes:128,verified:false,state:'ok',start:'',end:'',variables:[],levels:[],times:[],area:[]}];
  module.initialize({files,unverified_files:1,scanned_at:'2026-10-06T10:00:00Z'});
  document.querySelector('.inventory-month.has-data[data-dataset="era5:reanalysis-era5-single-levels"][data-month="0"]').click();
  await new Promise(resolve=>requestAnimationFrame(resolve));
  return {datasets:document.querySelectorAll('.inventory-dataset').length,months:document.querySelectorAll('.inventory-month.has-data').length,
    details:$('#era5-inventory-detail').textContent,status:$('#era5-inventory-status').textContent};
};
window.catalogFixture = async function() {
  const module=await import('/catalog.js');
  $('#catalog-search').value='';
  const products=[{product_key:'goes18:ABI-L1b-RadF',source_id:'goes18',satellite:'GOES-18',instrument:'ABI',level:'L1B',data_type:'ABI-L1b-RadF',display_name:'ABI Full Disk Radiance',resolution:'0.5 / 1 / 2 km',version:'',official_id:'ABI-L1b-RadF',area_type:'DISK',cadence:'10m',metadata:{official_product_url:'https://storage.googleapis.com/storage/v1/b/gcp-public-data-goes-18/o?prefix=ABI-L1b-RadF',version_status:'per_file'}},
    {product_key:'earthcare:validated:ACM_CAP_2B',source_id:'earthcare',satellite:'EarthCARE',instrument:'Synergy',level:'L2B',data_type:'ACM_CAP_2B',display_name:'Cloud aerosol precipitation synergy product',version:'4.0',official_id:'ACM_CAP_2B',metadata:{official_product_url:'https://catalog.maap.eo.esa.int/catalogue/collections?identifier=EarthCAREL2Validated_MAAP',access_class:'open'}}];
  module.initialize(products);
  document.querySelector('.catalog-product').open=true;
  await new Promise(resolve=>requestAnimationFrame(resolve));
  return {satellites:document.querySelectorAll('.catalog-satellite').length,products:document.querySelectorAll('.catalog-product').length,
    link:document.querySelector('.catalog-product-links a').href};
};
window.coverageTimelineStability = function() {
  const timeline = document.querySelector('#satellite-timelines');
  const supportedTop = timeline.getBoundingClientRect().top;
  state.coverage.supported = false;
  renderCoverageShell();
  const limitedTop = timeline.getBoundingClientRect().top;
  return {supportedTop, limitedTop};
};
window.calendarSizingFixture = async function(source, product, supported, year) {
  state.satelliteYears.clear();
  state.selected = {id:source+':'+product,source_id:source,product,satellite:source,cadence:'10m',metadata:{}};
  state.collections = [state.selected];
  state.coverage.supported = supported;
  state.coverage.bands = Array.from({length:16}, (_, i) => i+1);
  state.coverage.view = 'year';
  yearEl.replaceChildren(new Option(String(year),String(year)));
  renderCoverageShell();
  renderSatelliteTimelines();renderCalendar();
  // Resizing square year cells can change the scrollbar before ResizeObserver runs.
  await new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));
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
window.calendarIndexFixture = async function(mode, wide) {
  await window.styleFixture('calendar-view','day',wide);
  yearEl.replaceChildren(new Option('2025','2025'));
  Object.assign(state.coverage,{bands:[2,6,7,14],day:'2025-06-09',hour:-1});
  state.selected = {id:'goes18:ABI-L1b-RadC',source_id:'goes18',product:'ABI-L1b-RadC',cadence:'5m',metadata:{}};
  const times = mode === 'grid' ? Array.from({length:288},(_,i)=>i*5).filter(minute=>minute!==905)
    .map(minute=>['2025-06-09T'+String(Math.floor(minute/60)).padStart(2,'0')+':'+String(minute%60).padStart(2,'0')+':21Z',minute]) :
    [['2025-06-09T00:01:02.500Z',-1],['2025-06-09T22:30:40Z',-1]];
  const cells = times.flatMap((_,i)=>state.coverage.bands.map(band=>[i,band,1,1,0,0]));
  state.coverage.dayData = {v:1,day:'2025-06-09',mode,cadence:mode==='grid'?5:0,official_state:2,times,cells};
  state.coverage.cells = new Map(cells.map(cell=>[cell[0]+':'+cell[1],cell]));
  renderCoverageDay();
  await new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));
  const track = document.querySelector('.band-track');
  const gap = track.querySelector('[data-minute="905"]');
  const trackBox = track.getBoundingClientRect();
  const slotBox = track.firstElementChild.getBoundingClientRect();
  const mainBox = document.querySelector('main').getBoundingClientRect();
  const sidebarBox = document.querySelector('.sidebar').getBoundingClientRect();
  const scroller = document.querySelector('.matrix-scroll');
  return {range:$('#coverage-hour').value,count:track.children.length,hours:$('#coverage-day-overview').children.length,
    gap:gap?.className,pattern:gap?getComputedStyle(gap).backgroundImage:'',
    positions:[...track.children].map(cell=>parseFloat(cell.style.left)),
    slotWidth:slotBox.width,slotHeight:slotBox.height,mainWidth:mainBox.width,availableWidth:document.documentElement.clientWidth-sidebarBox.right,
    matrixScrollWidth:scroller.scrollWidth,scrollerWidth:scroller.clientWidth,
    matrixWidth:document.querySelector('#coverage-matrix').getBoundingClientRect().width,
    trackWidth:trackBox.width,centres:[...track.children].map(cell=>{
      const box=cell.getBoundingClientRect();return box.left+box.width/2-trackBox.left;
    }),
    scroll:document.documentElement.scrollWidth,viewport:innerWidth};
};
window.incrementalIndexFixture = function() {
  renderCoverageYear();
  const grid=$('#year-grid'),first=grid.querySelector('[data-day="2025-01-01"]'),second=grid.querySelector('[data-day="2025-01-02"]');
  const firstBand=first.children[0],secondBand=second.children[0];
  let stream;
  const previous=window.EventSource;
  window.EventSource=class {
    constructor(url) { this.url=url;this.listeners={};stream=this; }
    addEventListener(name,callback) { this.listeners[name]=callback; }
    close() {}
  };
  try { ensureIndexStream(state.selected.id,2025); } finally { window.EventSource=previous; }
  const changes=[];
  const observer=new MutationObserver(records=>changes.push(...records));
  observer.observe(grid,{childList:true});
  stream.listeners.index({data:JSON.stringify({coverage:true,bands:[2,6,7,14],active:false,
    days:[[0,'complete',1,1,1,1,0,0,0,0,false,'',[[2,288,288,24,24,0,0,0,0,0,0]],2,'grid']]})});
  const replaced=observer.takeRecords().length;observer.disconnect();stopIndexStream();
  return {sameDay:first===grid.querySelector('[data-day="2025-01-01"]'),sameBand:firstBand===first.children[0],
    untouched:secondBand===second.children[0],status:first.children[0].className,replaced};
};
window.coverageZoomFixture = function() {
  document.querySelector('[data-hour="15"]').click();
  const before={range:$('#coverage-hour').value,gaps:document.querySelectorAll('#coverage-matrix .official_missing').length};
  state.coverage.time='hourly';renderCoverageDay();
  before.hourlyRange=$('#coverage-hour').value;
  before.hourlyCount=document.querySelector('.band-track').children.length;
  before.hourlySlotWidth=document.querySelector('.band-slot').getBoundingClientRect().width;
  state.coverage.time='all';renderCoverageDay();
  $('#coverage-hour').value='-1';$('#coverage-hour').dispatchEvent(new Event('change'));
  return {...before,restored:document.querySelector('.band-track').children.length};
};
window.satelliteCalendarsFixture = async function() {
  state.collections = ['goes16','goes18','goes19'].map(source=>({id:source+':ABI-L1b-RadF',source_id:source,
    satellite:source.replace('goes','GOES-'),instrument:'ABI',level:'L1b',product:'ABI-L1b-RadF',metadata:{}}));
  state.selected = state.collections[1]; state.coverage.supported = false;
  satelliteEl.replaceChildren(new Option('全部卫星',''),new Option('GOES-18','GOES-18'));
  instrumentEl.replaceChildren(new Option('全部传感器',''));levelEl.replaceChildren(new Option('全部级别',''));
  fillMetadataSelect(areaEl,state.collections,'area_type');
  fillMetadataSelect(resolutionEl,state.collections,'resolution');
  state.dayState = new Map([['2025-01-01',{day:'2025-01-01',status:'complete'}]]);
  yearEl.replaceChildren(new Option('2025','2025'));
  state.satelliteYears = new Map([state.collections[0],state.collections[2]].map((product,index)=>[product.id,
    {product,days:new Map([['2025-01-01',{day:'2025-01-01',status:index?'partial':'missing'}]])}]));
  renderCoverageShell();renderCalendar();renderSatelliteTimelines();
  await new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));
  const grids = [...document.querySelectorAll('#year-layout .year-grid')];
  return {count:grids.length,matchingProducts:filteredCollections(5).length,statuses:grids.map(grid=>grid.querySelector('[data-day="2025-01-01"]').dataset.status),
    widths:grids.map(grid=>grid.getBoundingClientRect().width),labels:[...document.querySelectorAll('.year-product-heading')].map(node=>node.textContent),
    scroll:document.documentElement.scrollWidth,viewport:innerWidth,
    headingFits:[...document.querySelectorAll('.year-product-heading')].every(node=>node.scrollWidth<=node.clientWidth)};
};
window.sidebarFooterFixture = async function(expanded) {
  setSidebar(expanded);
  await new Promise(resolve=>setTimeout(resolve,250));
  const sidebar=$('.sidebar').getBoundingClientRect();
  const button=$('#portal-logout'),box=button.getBoundingClientRect();
  const toggle=$('.sidebar-toggle').getBoundingClientRect();
  const range=document.createRange();range.selectNodeContents(button);
  const text=range.getBoundingClientRect();
  return {sidebarLeft:sidebar.left,sidebarRight:sidebar.right,sidebarBottom:sidebar.bottom,
    buttonLeft:box.left,buttonRight:box.right,buttonBottom:box.bottom,width:box.width,height:box.height,
    toggleLeft:toggle.left,toggleRight:toggle.right,textLeft:text.left,textRight:text.right,
    textLines:range.getClientRects().length,whiteSpace:getComputedStyle(button).whiteSpace,
    sidebarScroll:$('.sidebar').scrollWidth,sidebarClient:$('.sidebar').clientWidth,viewportHeight:innerHeight};
};
bind();
window.styleFixtureReady = true;
`;
const assets = new Map();
for (const file of ['index.html','login.html','styles.css','login.js','catalog.js','era5-inventory.js','icons.svg']) assets.set(file, await readFile(new URL('./static/'+file, import.meta.url)));
assets.set('app.js', Buffer.from(app+fixture));
const server = createServer((req, res) => {
  const path = new URL(req.url,'http://localhost').pathname;
  const file = path==='/'?'index.html':path==='/login'?'login.html':path.slice(1);
  if (!assets.has(file)) {res.writeHead(404);res.end();return;}
  res.setHeader('Content-Type',file.endsWith('.js')?'text/javascript':file.endsWith('.css')?'text/css':file.endsWith('.svg')?'image/svg+xml':'text/html');
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
  const cases = [['calendar-view','year'],['calendar-view','month'],['calendar-view','day'],['era5-view','inventory'],['era5-view','download'],['catalog-view','products'],['catalog-view','sources'],['downloads-view','year'],['monitor-view','year'],['access-view','year'],['access-view','form']];
  for (const width of [320,390,1440,1920,2560]) {
    await driver.manage().window().setRect({width,height:900});
    await driver.get(base+'/login');
    const loginSize = await driver.executeScript('return document.querySelector(".login-card").getBoundingClientRect().width');
    assert.equal(loginSize,280);
    await writeFile('dist/visual/login-'+width+'.png',Buffer.from(await driver.takeScreenshot(),'base64'));
    await driver.get(base);
    await driver.wait(() => driver.executeScript('return window.styleFixtureReady===true'),10000);
    await driver.executeScript('return window.styleFixture("calendar-view","year",arguments[0])',width>820);
    const calendarCardSizing = await driver.executeScript(`
      const view=document.querySelector("#calendar-view");
      const header=view.querySelector(".page-header");
      const card=view.querySelector(".calendar-card");
      const vb=view.getBoundingClientRect(),hb=header.getBoundingClientRect(),cb=card.getBoundingClientRect();
      return {width:vb.width,headerWidth:hb.width,cardWidth:cb.width,
        left:vb.left,headerLeft:hb.left,cardLeft:cb.left,
        right:vb.right,headerRight:hb.right,cardRight:cb.right,
        fontSize:parseFloat(getComputedStyle(view).fontSize),viewport:innerWidth,contentWidth:document.documentElement.clientWidth,
        sidebar:parseFloat(getComputedStyle(document.body).getPropertyValue("--sidebar-width"))||0};
    `);
    if (calendarCardSizing.viewport >= 1200) {
      const expected=Math.min(1600,calendarCardSizing.contentWidth-calendarCardSizing.sidebar-48);
      assert.ok(Math.abs(calendarCardSizing.width-expected)<1,
        'calendar view wide layout mismatch: '+JSON.stringify({...calendarCardSizing,expected}));
      assert.ok(Math.abs(calendarCardSizing.headerWidth-calendarCardSizing.cardWidth)<1,
        'calendar header/card widths differ: '+JSON.stringify(calendarCardSizing));
      assert.ok(Math.abs(calendarCardSizing.headerLeft-calendarCardSizing.cardLeft)<1 &&
        Math.abs(calendarCardSizing.headerRight-calendarCardSizing.cardRight)<1,
        'calendar header/card edges differ: '+JSON.stringify(calendarCardSizing));
    }
    assert.equal(calendarCardSizing.fontSize,14);
    for (const expanded of [false,true]) {
      const footer=await driver.executeScript('return window.sidebarFooterFixture(arguments[0])',expanded);
      assert.equal(footer.width,32);assert.equal(footer.height,28);
      assert.equal(footer.whiteSpace,'nowrap');assert.equal(footer.textLines,1,'logout wrapped: '+JSON.stringify(footer));
      assert.ok(footer.buttonLeft>=footer.sidebarLeft && footer.buttonRight<=footer.sidebarRight,'logout escapes sidebar: '+JSON.stringify(footer));
      assert.ok(footer.textLeft>=footer.buttonLeft && footer.textRight<=footer.buttonRight,'logout text escapes button: '+JSON.stringify(footer));
      assert.ok(footer.toggleLeft>=footer.sidebarLeft && footer.toggleRight<=footer.sidebarRight,'toggle escapes sidebar');
      assert.ok(footer.buttonBottom<=footer.sidebarBottom,'logout is below viewport');
      assert.ok(footer.sidebarScroll<=footer.sidebarClient,'sidebar scrolls horizontally');
    }
    await driver.executeScript('return window.sidebarFooterFixture(arguments[0])',width>820);
    const timelineStability = await driver.executeScript('return window.coverageTimelineStability()');
    assert.equal(timelineStability.limitedTop,timelineStability.supportedTop,'coverage capability changed timeline vertical position');
    const fixed=await driver.executeScript('return window.calendarIndexFixture("grid",arguments[0])',width>820);
    assert.equal(fixed.range,'-1','daily coverage is not all-day by default');
    assert.equal(fixed.count,288,'fixed-period timeline omitted nominal slots');
    assert.equal(fixed.hours,24,'hour overview missing');
    assert.match(fixed.gap,/official_missing/);
    assert.match(fixed.pattern,/repeating-linear-gradient/,'official gap has no hatch');
    assert.ok(fixed.scroll<=fixed.viewport+1,'all-day timeline overflows page');
    assert.ok(fixed.slotWidth>=2.99,'fixed slot was compressed: '+JSON.stringify(fixed));
    assert.ok(fixed.slotWidth<=5.01,'fixed slot exceeds 5px: '+JSON.stringify(fixed));
    assert.equal(fixed.slotHeight,28);
    assert.ok(Math.abs(fixed.matrixScrollWidth-Math.max(fixed.matrixWidth,fixed.scrollerWidth))<=1,'matrix overflow was not isolated: '+JSON.stringify(fixed));
    assert.ok(Math.abs(fixed.mainWidth-fixed.availableWidth)<1,'calendar does not use available width: '+JSON.stringify(fixed));
    const zoom=await driver.executeScript('return window.coverageZoomFixture()');
    assert.equal(zoom.range,'15');assert.equal(zoom.gaps,4);assert.equal(zoom.restored,288);
    assert.equal(zoom.hourlyRange,'-1');assert.equal(zoom.hourlyCount,24);
    assert.ok(zoom.hourlySlotWidth>=2.99 && zoom.hourlySlotWidth<=5.01,'hourly slot is outside 3-5px: '+JSON.stringify(zoom));
    await writeFile('dist/visual/calendar-all-day-'+width+'.png',Buffer.from(await driver.takeScreenshot(),'base64'));
    const incremental=await driver.executeScript('return window.incrementalIndexFixture()');
    assert.deepEqual(incremental,{sameDay:true,sameBand:true,untouched:true,status:'complete',replaced:0});
    const events=await driver.executeScript('return window.calendarIndexFixture("events",arguments[0])',width>820);
    assert.equal(events.count,2,'event timeline invented gaps');
    assert.ok(Math.abs(events.positions[0]-62.5/864)<1e-4,JSON.stringify(events));
    assert.ok(Math.abs(events.positions[1]-81040/864)<1e-4,JSON.stringify(events));
    assert.ok(Math.abs(events.centres[0]-events.trackWidth*62.5/86400)<.1,JSON.stringify(events));
    assert.ok(Math.abs(events.centres[1]-events.trackWidth*81040/86400)<.1,JSON.stringify(events));
    assert.ok(events.scroll<=events.viewport+1,'event timeline overflows page');
    assert.equal(events.slotWidth,3);
    assert.equal(events.slotHeight,26);
    await writeFile('dist/visual/calendar-events-'+width+'.png',Buffer.from(await driver.takeScreenshot(),'base64'));
    for (const [source,product,supported] of [['goes18','ABI-L1b-RadF',true],['goes18','GLM-L2-LCFA',false],
      ['himawari9','AHI-L1b-FLDK',true],['earthcare','ACM_CAP_2B',false],['cloudsat','2B-GEOPROF',false],
      ['modis','MOD021KM',false],['viirs','VNP02MOD',false],['mtg','FCI-L1c',true]]) {
      for (const year of [2026,2000]) {
        const size=await driver.executeScript('return window.calendarSizingFixture(...arguments)',source,product,supported,year);
        assert.ok(Math.abs(size.cellWidth-size.cellHeight)<.1,product+' cells not square');
        assert.ok(Math.abs(size.cellWidth-size.gap*4)<.1,product+' spacing not proportional: '+JSON.stringify(size));
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
    const satellites=await driver.executeScript('return window.satelliteCalendarsFixture()');
    assert.equal(satellites.matchingProducts,3,'satellite fixture metadata filters exclude expected peers');
    assert.equal(satellites.count,3);
    assert.deepEqual(satellites.statuses,['complete','missing','partial']);
    assert.ok(satellites.labels[0].includes('GOES-18') && satellites.labels[1].includes('GOES-16') && satellites.labels[2].includes('GOES-19'));
    assert.ok(satellites.widths.every(value=>Math.abs(value-satellites.widths[0])<1),'satellite year widths differ');
    assert.ok(satellites.scroll<=satellites.viewport+1,'all satellites overflow page');
    assert.equal(satellites.headingFits,true,'satellite heading overflows');
    await writeFile('dist/visual/calendar-all-satellites-'+width+'.png',Buffer.from(await driver.takeScreenshot(),'base64'));
    for (const [view,mode] of cases) {
      await driver.executeScript('return window.styleFixture(arguments[0],arguments[1],arguments[2])',view,mode,width>820);
      if(view==='era5-view') {
        const inventory=await driver.executeScript('return window.inventoryFixture()');
        assert.equal(inventory.datasets,3);assert.equal(inventory.months,2);
        assert.match(inventory.details,/2m_temperature/);assert.match(inventory.status,/待核验/);
      }
      if(view==='catalog-view') {
        const catalog=await driver.executeScript('return window.catalogFixture()');
        assert.equal(catalog.satellites,2);assert.equal(catalog.products,1);
        assert.ok(catalog.link.startsWith('https://storage.googleapis.com/'));
      }
      const metrics = await driver.executeScript(`
        const bad = [];
        for (const node of document.querySelectorAll('.view.active button,.view.active input,.view.active select')) {
          const box = node.getBoundingClientRect();
          if (box.width && box.height && box.width < 8 && !node.classList.contains('band-slot')) bad.push(node.id||node.className);
        }
        return {width:innerWidth,scroll:document.documentElement.scrollWidth,bad};
      `);
      assert.ok(metrics.scroll<=metrics.width+1,view+' '+mode+' overflows: '+JSON.stringify(metrics));
      assert.deepEqual(metrics.bad,[],view+' has collapsed controls');
      if (view === 'downloads-view') {
        const picker = await driver.executeScript(`
          const $=selector=>document.querySelector(selector);
          const checked=[...document.querySelectorAll('[data-download-product]:checked')].map(node=>node.dataset.downloadProduct);
          return {checked,selected:$('#dl-selected-count').textContent,results:$('#dl-filtered-count').textContent,
            legacy:!!document.querySelector('#dl-extra-products,#download-batches,#download-requests')};
        `);
        assert.equal(picker.checked.length,2);
        assert.equal(picker.selected,'已选 2 个');
        assert.equal(picker.results,'3');
        assert.equal(picker.legacy,false);
        const initial = await driver.executeScript(`const $=selector=>document.querySelector(selector);return {
          task:$('#download-timezone').value,monitor:$('#download-monitor-timezone').value,
          request:$('#download-tasks').textContent,
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
          return {request:$('#download-tasks').textContent,
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
          return {request:$('#download-tasks').textContent,
            axis:[...document.querySelectorAll('#download-chart-rate .plot-time span')].map(node=>node.textContent)};
        `);
        assert.equal(monitorChanged.request, changed.request);
        assert.ok(monitorChanged.axis.every(value => value.endsWith(' UTC')));
        await driver.executeScript(`
          for(const [id,value] of [['download-timezone','UTC'],['download-monitor-timezone','BJT']]) {
            const node=document.getElementById(id);node.value=value;node.dispatchEvent(new Event('change'));
          }
        `);
        const confirmation=await driver.executeScript('return window.downloadConfirmationFixture(true)');
        assert.equal(confirmation.open,true);
        assert.match(confirmation.content,/GOES-18、Aqua/);
        assert.match(confirmation.content,/产品清单 · 2 个/);
        assert.ok(confirmation.left>=0&&confirmation.right<=confirmation.width+1,'task confirmation exceeds viewport');
        assert.ok(confirmation.scroll<=confirmation.client+1,'task confirmation has horizontal overflow');
        await writeFile('dist/visual/download-task-confirm-'+width+'.png',Buffer.from(await driver.takeScreenshot(),'base64'));
        await driver.executeScript('return window.downloadConfirmationFixture(false)');
      }
      if (['month','era5','form'].includes(mode)||view==='monitor-view'||view==='downloads-view'||(view==='calendar-view'&&mode==='year')) {
        await writeFile('dist/visual/'+view+'-'+mode+'-'+width+'.png',Buffer.from(await driver.takeScreenshot(),'base64'));
      }
      console.log('Layout OK:',width,view,mode);
    }
  }
  await driver.manage().window().setRect({width:2560,height:900});
  await driver.get(base);
  await driver.wait(() => driver.executeScript('return window.styleFixtureReady===true'),10000);
  await driver.executeScript('return window.styleFixture("calendar-view","year",false)');
  const wideCalendar = await driver.executeScript(`
    const view=document.querySelector("#calendar-view").getBoundingClientRect();
    const header=document.querySelector("#calendar-view>.page-header").getBoundingClientRect();
    const card=document.querySelector("#calendar-view>.calendar-card").getBoundingClientRect();
    const grid=document.querySelector("#year-grid").getBoundingClientRect();
    return {viewWidth:view.width,headerWidth:header.width,cardWidth:card.width,gridWidth:grid.width,viewport:innerWidth,
      viewLeft:view.left,headerLeft:header.left,cardLeft:card.left,
      viewRight:view.right,headerRight:header.right,cardRight:card.right,
      sidebar:parseFloat(getComputedStyle(document.body).getPropertyValue("--sidebar-width"))||0};
  `);
  assert.ok(Math.abs(wideCalendar.viewWidth-1600)<1,'2K calendar view is not 1600px wide: '+JSON.stringify(wideCalendar));
  assert.ok(Math.abs(wideCalendar.headerWidth-wideCalendar.cardWidth)<1,'2K header/card widths differ: '+JSON.stringify(wideCalendar));
  assert.ok(Math.abs(wideCalendar.headerLeft-wideCalendar.cardLeft)<1 &&
    Math.abs(wideCalendar.headerRight-wideCalendar.cardRight)<1,'2K header/card edges differ: '+JSON.stringify(wideCalendar));
  assert.ok(Math.abs(wideCalendar.gridWidth-wideCalendar.cardWidth)<1,'2K year grid does not fill calendar card: '+JSON.stringify(wideCalendar));
  await writeFile('dist/visual/calendar-view-year-2560.png',Buffer.from(await driver.takeScreenshot(),'base64'));
  for (const height of [240,480]) {
    await driver.manage().window().setRect({width:390,height});
    const contentHeight=await driver.executeScript('return innerHeight');
    if (contentHeight!==height) {
      const frame=await driver.manage().window().getRect();
      await driver.manage().window().setRect({width:390,height:frame.height+height-contentHeight});
    }
    assert.equal(await driver.executeScript('return innerHeight'),height,'short-window content height was not set');
    await driver.executeScript('return window.styleFixture("calendar-view","year",false)');
    for (const expanded of [false,true]) {
      const footer=await driver.executeScript('return window.sidebarFooterFixture(arguments[0])',expanded);
      assert.equal(footer.textLines,1,'short-window logout wrapped: '+JSON.stringify(footer));
      assert.ok(footer.buttonRight<=footer.sidebarRight,'short-window logout escapes sidebar');
      assert.ok(footer.buttonBottom<=Math.min(footer.sidebarBottom,footer.viewportHeight),'short-window logout is clipped: '+JSON.stringify(footer));
      await writeFile('dist/visual/sidebar-footer-'+height+'-'+(expanded?'expanded':'collapsed')+'.png',Buffer.from(await driver.takeScreenshot(),'base64'));
    }
  }
} finally {
  try {if(driver) await driver.quit();}
  finally {await new Promise(resolve => server.close(resolve));}
}

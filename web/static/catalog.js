const $ = selector => document.querySelector(selector);
const esc = value => String(value ?? '').replaceAll('&','&amp;').replaceAll('<','&lt;').replaceAll('>','&gt;').replaceAll('"','&quot;');
const unique = values => [...new Set(values)].sort((a,b)=>a.localeCompare(b,undefined,{numeric:true}));
const JPSS = 'https://www.nesdis.noaa.gov/s3/2024-09/24-PL-861-NOAA-NESDIS-FY25_Major_Annual_Satellite_Report.pdf';
const HIMAWARI = 'https://www.data.jma.go.jp/mscweb/en/general/history.html';
const MTG = 'https://www.esa.int/Applications/Observing_the_Earth/Meteorological_missions/meteosat_third_generation';
export const missions = {
  Terra: {launch:'1999-12-18T18:57Z',url:'https://science.nasa.gov/earth/earth-observatory/terra-launches-499/',text:'NASA 地球观测系统晨间极轨平台，观测陆地、大气、海洋与辐射收支。'},
  Aqua: {launch:'2002-05-04T09:55Z',url:'https://aqua.nasa.gov/content/about-aqua',text:'NASA 地球观测系统午后极轨平台，侧重水循环、云、海洋和陆地。'},
  'Suomi-NPP': {launch:'2011-10-28T09:48Z',url:JPSS,text:'NOAA / NASA 联合极轨平台，连接研究观测与业务气象观测。'},
  'NOAA-20': {launch:'2017-11-18T09:47Z',url:JPSS,text:'原 JPSS-1，全球极轨气象与环境观测平台。'},
  'NOAA-21': {launch:'2022-11-10T09:49Z',url:'https://www.nesdis.noaa.gov/our-satellites/currently-flying/jpss-2-launch',text:'原 JPSS-2，延续全球气象、海洋与陆地观测。'},
  'GOES-16': {launch:'2016-11-19T23:42Z',url:'https://www.nasa.gov/missions/goes/nasa-participates-in-the-noaa-goes-16-field-campaign/',text:'原 GOES-R。静止轨道平台，搭载地球成像、闪电和空间天气仪器。'},
  'GOES-17': {launch:'2018-03-01T22:02Z',url:'https://www.nasa.gov/news-release/nasa-ula-launch-advanced-noaa-weather-satellite/',text:'原 GOES-S，第二颗 GOES-R 系列静止气象卫星。'},
  'GOES-18': {launch:'2022-03-01T21:38Z',url:'https://www.nesdis.noaa.gov/news/noaas-goes-t-reaches-geostationary-orbit-now-designated-goes-18',text:'原 GOES-T，第三颗 GOES-R 系列静止气象卫星。'},
  'GOES-19': {launch:'2024-06-25T21:26Z',url:'https://science.nasa.gov/blogs/goes/',text:'原 GOES-U，第四颗 GOES-R 系列卫星，增加日冕观测载荷。'},
  'Himawari-8': {launch:'2014-10-07T05:16Z',url:HIMAWARI,text:'JMA 静止气象卫星，AHI 覆盖东亚、西太平洋与澳大利亚。'},
  'Himawari-9': {launch:'2016-11-02T06:20Z',url:HIMAWARI,text:'JMA 静止气象卫星，与 Himawari-8 形成主备观测体系。'},
  CloudSat: {launch:'2006-04-28T10:02Z',launchURL:'https://science.nasa.gov/earth/earth-observatory/cloudsatcalipso-launch-successfully-6510/',ended:'2023-12-20',endLabel:'任务退役',url:'https://science.nasa.gov/mission/cloudsat/',text:'以 94 GHz 云剖面雷达观测云的垂直结构，与 CALIPSO 等平台形成协同观测。'},
  CALIPSO: {launch:'2006-04-28T10:02Z',launchURL:'https://science.nasa.gov/earth/earth-observatory/cloudsatcalipso-launch-successfully-6510/',ended:'2023-08-01',endLabel:'科学观测结束',url:'https://science.nasa.gov/mission/calipso/',text:'NASA / CNES 联合任务，利用激光雷达与成像仪观测云和气溶胶的垂直结构。'},
  EarthCARE: {launch:'2024-05-28T22:20Z',url:'https://www.esa.int/Newsroom/Press_Releases/EarthCARE_launched_to_study_role_of_clouds_and_aerosols_in_Earth_s_climate',text:'ESA / JAXA 云、气溶胶和辐射联合任务，协同使用激光雷达、云雷达、成像仪和辐射计。'},
  MTG: {url:MTG,text:'EUMETSAT / ESA 第三代静止气象任务，分成像型 MTG-I 和探测型 MTG-S。官方集合按 MTG 系列组织，不限定单颗载星。',launches:[['MTG-I1 / Meteosat-12','2022-12-13'],['MTG-S1','2025-07-01'],['MTG-I2','2026-08-27']]},
};

const instruments = {
  MODIS: ['中分辨率成像光谱仪','36 个波段，观测云、气溶胶、陆地、海洋和地表温度。','https://modis.gsfc.nasa.gov/about/'],
  VIIRS: ['可见光红外成像辐射计','可见光至热红外成像，包含夜间低照度观测。','https://www.nesdis.noaa.gov/our-satellites/currently-flying/joint-polar-satellite-system/viirs'],
  ABI: ['先进基线成像仪','16 波段静止轨道成像，用于天气、云、火灾与地表观测。','https://www.goes-r.gov/spacesegment/abi.html'],
  AHI: ['先进向日葵成像仪','16 波段成像，完整圆盘名义 10 分钟一轮，另有区域扫描。','https://www.data.jma.go.jp/mscweb/en/himawari89/space_segment/spsg_ahi.html'],
  GLM: ['静止轨道闪电测绘仪','探测闪电光学事件，并形成事件、组和闪电层级。','https://www.goes-r.gov/spacesegment/glm.html'],
  EXIS: ['极紫外与 X 射线辐照度传感器','测量太阳辐照度，监测耀斑和太阳对高层大气的影响。','https://www.goes-r.gov/spacesegment/exis.html'],
  SEISS: ['空间环境原位传感器套件','测量卫星周围高能粒子环境。','https://www.goes-r.gov/spacesegment/seiss.html'],
  SUVI: ['太阳紫外成像仪','观测太阳日冕，支持空间天气监测。','https://www.goes-r.gov/spacesegment/suvi.html'],
  MAG: ['磁强计','测量地球同步轨道附近的磁场。','https://www.goes-r.gov/spacesegment/mag.html'],
  CPR: ['云剖面雷达','主动雷达观测云和降水的垂直结构；EarthCARE 另具多普勒测量能力。','https://science.nasa.gov/mission/cloudsat/'],
  CALIOP: ['云与气溶胶激光雷达','测量云和气溶胶垂直剖面的后向散射与偏振特征。','https://www-calipso.larc.nasa.gov/about/payload.php'],
  IIR: ['成像红外辐射计','红外辐射观测用于云的辐射特征与微物理研究。','https://www-calipso.larc.nasa.gov/about/payload.php'],
  WFC: ['广角相机','为激光雷达剖面提供周围云场的可见光图像。','https://www-calipso.larc.nasa.gov/about/payload.php'],
  ATLID: ['大气激光雷达','高光谱分辨率激光雷达，区分分子、云和气溶胶散射。','https://visuals.earth.esa.int/satellites/earthcare'],
  MSI: ['多光谱成像仪','提供云和气溶胶的水平分布，为主动仪器的窄条带剖面补充背景。','https://visuals.earth.esa.int/satellites/earthcare'],
  BBR: ['宽波段辐射计','测量地球向外的短波与长波辐射，与云和气溶胶剖面配合。','https://visuals.earth.esa.int/satellites/earthcare'],
  FCI: ['灵活组合成像仪','MTG-I 多光谱成像，用于欧洲及周边区域的天气监测。',MTG],
  LI: ['闪电成像仪','MTG-I 光学闪电观测。',MTG],
  IRS: ['红外探测仪','MTG-S 红外高光谱探测，提供大气温湿结构信息。',MTG],
  Synergy: ['多仪器协同产品','组合多个仪器的观测与反演，不是独立的传感器。','https://portal.maap.eo.esa.int/earthcare/earthcare-data-offer/'],
  Auxiliary: ['辅助数据','校准、几何定位或反演所需的辅助信息，不是影像观测产品。','https://portal.maap.eo.esa.int/earthcare/earthcare-data-offer/'],
  Orbit: ['轨道资料','轨道预测、重建与几何定位所需的数据，不是影像观测产品。','https://portal.maap.eo.esa.int/earthcare/earthcare-data-offer/'],
};
const familyNames = {goes:'GOES',himawari:'Himawari',modis:'MODIS',viirs:'VIIRS',cloudsat:'CloudSat',calipso:'CALIPSO',earthcare:'EarthCARE',mtg:'MTG'};
export const family = id => id.replace(/^goes\d+$/,'goes').replace(/^himawari\d+$/,'himawari');
export const dateText = value => !value ? '' : /^\d{4}-\d{2}-\d{2}$/.test(value) ? value+' · 日期精度' : value.replace('T',' ').replace(/Z$/,' UTC');
export function safeURL(value) {
  try { const url=new URL(value); return url.protocol==='https:' && !url.username && !url.password ? url.href : ''; } catch { return ''; }
}
const link = (url,label) => safeURL(url) ? `<a href="${esc(safeURL(url))}" target="_blank" rel="noopener noreferrer">${esc(label)}<svg class="icon" aria-hidden="true"><use href="/icons.svg#external"/></svg></a>` : '';

export function productDescription(product) {
  const title=(product.display_name || product.data_type || '').toLowerCase();
  const type=(product.data_type || '').toUpperCase();
  if(product.instrument==='Auxiliary')return '提供校准、环境或几何定位所需的辅助资料。';
  if(product.instrument==='Orbit')return '提供载星轨道与位置资料，支持观测的几何定位。';
  if(product.source_id==='earthcare') {
    if(product.level?.startsWith('L1'))return `${product.instrument}的${product.level}仪器测量产品，供校准、定位及后续地球物理量反演使用；具体测量模式见官方定义。`;
    const kind=type.split('_').filter(Boolean)[1];
    const names={AER:'气溶胶光学属性',ALD:'气溶胶层描述',CTH:'云顶高度',EBD:'消光、后向散射与退偏特征',FM:'大气特征掩膜',ICE:'冰云属性',TC:'观测目标分类',CD:'雷达多普勒测量',CLD:'云物理属性',FMR:'前向模拟雷达量',AOT:'气溶胶光学厚度',CM:'云掩膜',COP:'云光学属性',RAD:'辐射观测',CAP:'云、气溶胶与降水综合属性',COM:'多仪器组合观测',FLX:'辐射通量'};
    if(names[kind])return `${names[kind]}，用于云、气溶胶及其辐射影响的研究。`;
  }
  const topics = [
    [/geolocation|geolocated|location|03(?:_|$)|0[2-4]_1[bcd]/,'观测点的地理位置与几何信息，用于定位和配准。'],
    [/cloud mask|clear sky mask|cldmsk|acm[fcm]?|feature mask|vfm/,'识别云或大气特征层，供筛选、分类及后续反演使用。'],
    [/motion vector|derived motion|dmw|wind/,'跟踪云或大气特征，估计运动与风场。'],
    [/aerosol|aod|aer_|lid_a|extinction/,'气溶胶光学特征或垂直分布，用于空气质量和气候研究。'],
    [/cloud top|cth|ctt|acha|acht|ac[hpt]/,'云顶高度、温度或气压等云顶属性。'],
    [/cloud type|cloud class|cldclass|phase/,'云的类型、相态或分类信息。'],
    [/precipitation|rain|rrqpe|snow-profile/,'云与降水属性，用于研究降水分布和强度。'],
    [/fire|hot.?spot|fdc/,'火点、热异常或相关燃烧特征，支持火灾监测。'],
    [/snow|ice|sno|sic/,'积雪、冰或冻融相关特征。'],
    [/vegetation|ndvi|evi|lai|fpar|phenology/,'植被状态、物候或生产力相关信息。'],
    [/land surface temperature|sst|sea surface temperature|lst/,'陆地或海洋表面温度，用于环境与能量交换研究。'],
    [/reflectance|albedo|brdf/,'地表或大气反射特征，用于陆表与辐射研究。'],
    [/radiative|radiation|flux|flxhr|olr|flx/,'辐射通量或辐射收支，用于云与气候能量平衡研究。'],
    [/lightning|glm|^li[ _-]/,'光学闪电事件及其空间和时间分布。'],
    [/radiance|rad[fcm]?\b|nom_1b|1b-cpr|ahi-l1b/,'经校准的辐射观测，供成像、分析和物理量反演使用。'],
    [/temperature|humidity|water vapor|moisture|sounding|profile/,'大气或地表物理量及其剖面，支持天气和环境研究。'],
    [/geoprof|cloud|cld|synergy/,'云的空间分布、垂直结构或物理属性。'],
    [/x.ray|euv|proton|electron|magnet|seis|exis|suvi/,'太阳或近地空间环境观测，支持空间天气监测。'],
  ];
  const matched=topics.find(([pattern])=>pattern.test(title+' '+type.toLowerCase()));
  if(matched)return matched[1];
  if(product.level?.startsWith('L0'))return '原始仪器或遥测数据，通常需要进一步处理。';
  return `${product.instrument || '任务'}发布的${product.level || ''}数据产品；具体变量与适用范围见官方集合说明。`;
}

export function groupCatalog(products) {
  const groups=new Map();
  for(const product of products) {
    const id=family(product.source_id);
    if(!groups.has(id))groups.set(id,{id,name:familyNames[id] || id,satellites:new Map()});
    const satellites=groups.get(id).satellites;
    const name=product.satellite || '载星未提供';
    if(!satellites.has(name))satellites.set(name,{name,instruments:new Map()});
    const kinds=satellites.get(name).instruments,kind=product.instrument || '产品组';
    if(!kinds.has(kind))kinds.set(kind,[]);
    kinds.get(kind).push(product);
  }
  return [...groups.values()];
}

let catalog=[],selected={satellite:'',instrument:''},bound=false;
function matchedProducts() {
  const query=$('#catalog-search').value.trim().toLowerCase();
  return catalog.filter(item=>!query || [item.satellite,item.instrument,item.data_type,item.display_name,item.level,item.version,productDescription(item)].join(' ').toLowerCase().includes(query));
}
function renderTree(products) {
  const node=$('#catalog-tree');node.replaceChildren();
  for(const group of groupCatalog(products)) {
    const section=document.createElement('details');section.open=true;
    const summary=document.createElement('summary');summary.textContent=group.name;section.append(summary);
    for(const satellite of group.satellites.values()) {
      const button=document.createElement('button');button.type='button';button.className='catalog-satellite';button.textContent=satellite.name;
      button.classList.toggle('selected',selected.satellite===satellite.name && !selected.instrument);
      button.addEventListener('click',()=>{selected={satellite:satellite.name,instrument:''};render();});section.append(button);
      for(const kind of satellite.instruments.keys()) {
        const child=document.createElement('button');child.type='button';child.className='catalog-instrument';child.textContent=kind;
        child.classList.toggle('selected',selected.satellite===satellite.name && selected.instrument===kind);
        child.addEventListener('click',()=>{selected={satellite:satellite.name,instrument:kind};render();});section.append(child);
      }
    }
    node.append(section);
  }
}
function descriptionPanel(name,items) {
  const mission=missions[name];
  const start=unique(items.map(item=>item.metadata?.time_start).filter(Boolean))[0];
  const ends=unique(items.map(item=>item.metadata?.time_end).filter(Boolean));
  const end=ends.at(-1);
  let facts='';
  if(mission?.launch)facts+=`<dt>发射</dt><dd>${link(mission.launchURL || mission.url,dateText(mission.launch))}</dd>`;
  if(!mission && name.split(/\s*[\/+]\s*/).length>1) {
    for(const platform of name.split(/\s*[\/+]\s*/))if(missions[platform]?.launch)facts+=`<dt>${esc(platform)} 发射</dt><dd>${link(missions[platform].url,dateText(missions[platform].launch))}</dd>`;
  }
  if(mission?.launches)facts+=mission.launches.map(([platform,date])=>`<dt>${esc(platform)}</dt><dd>${link(mission.url,dateText(date))}</dd>`).join('');
  if(mission?.ended)facts+=`<dt>${esc(mission.endLabel)}</dt><dd>${esc(mission.ended)}</dd>`;
  if(start || end)facts+=`<dt>官方集合时间范围</dt><dd>${esc(dateText(start))} 至 ${esc(dateText(end))}</dd>`;
  return `<header class="catalog-mission"><h1>${esc(name)}</h1><p>${esc(mission?.text || '该载星与产品归属来自官方集合目录。')}</p><dl>${facts}</dl>${link(mission?.url,'任务官网')}</header>`;
}
function render() {
  const products=matchedProducts();
  if(!products.some(item=>item.satellite===selected.satellite && (!selected.instrument || item.instrument===selected.instrument)))selected={satellite:products[0]?.satellite || '',instrument:''};
  renderTree(products);
  const node=$('#catalog-content');node.replaceChildren();
  if(!products.length){node.textContent='没有匹配的产品';return;}
  const items=products.filter(item=>item.satellite===selected.satellite);
  const header=document.createElement('div');header.innerHTML=descriptionPanel(selected.satellite,items);node.append(header);
  for(const kind of unique(items.map(item=>item.instrument || '产品组'))) {
    if(selected.instrument && selected.instrument!==kind)continue;
    const own=items.filter(item=>(item.instrument || '产品组')===kind);
    const [title,text,url]=instruments[kind] || [kind,'官方集合使用的仪器或产品分类。',missions[selected.satellite]?.url];
    const section=document.createElement('section');section.className='catalog-products';
      const physical=['MODIS','VIIRS','ABI','AHI','GLM','EXIS','SEISS','SUVI','MAG','CPR','CALIOP','IIR','WFC','ATLID','MSI','BBR'];
    const launch=physical.includes(kind)?missions[selected.satellite]?.launch:null;
    section.innerHTML=`<header><h2>${esc(kind)} <span>${esc(title)}</span></h2>${link(url,'仪器官网')}</header><p>${esc(text)}</p>${launch?`<div class="catalog-instrument-date">随载星发射 · ${esc(dateText(launch))}</div>`:''}<div class="catalog-product-count">${own.length} 个官方集合</div>`;
    if(selected.satellite==='MTG' && ['FCI','LI','IRS'].includes(kind)) {
      const dates=missions.MTG.launches.filter(([platform])=>kind==='IRS'?platform.startsWith('MTG-S'):platform.startsWith('MTG-I'));
      const note=document.createElement('div');note.className='catalog-instrument-date';
      note.textContent=dates.map(([platform,date])=>platform+' 随载星发射 '+dateText(date)).join(' · ');section.append(note);
    }
    for(const product of own.sort((a,b)=>(a.data_type || '').localeCompare(b.data_type || '') || (a.version || '').localeCompare(b.version || ''))) {
      const details=document.createElement('details');details.className='catalog-product';details.dataset.product=product.product_key;
      const range=[dateText(product.metadata?.time_start),dateText(product.metadata?.time_end)].filter(Boolean).join(' 至 ');
      const rows=[['官方集合 ID',product.official_id],['处理级别',['AUX','ORBIT'].includes(product.level)?'':product.level],['集合版本',product.version],['分辨率',product.resolution],['空间范围',product.area_type],['数据频率',product.cadence==='event'?'':product.cadence],['官方时间范围',range]].filter(([,value])=>value);
      details.innerHTML=`<summary><strong>${esc(product.data_type)}</strong><span>${esc([product.level,product.version,product.metadata?.access_class].filter(Boolean).join(' · '))}</span></summary><div class="catalog-product-body"><h3>${esc(product.display_name || product.data_type)}</h3><p>${esc(productDescription(product))}</p><dl>${rows.map(([label,value])=>`<dt>${esc(label)}</dt><dd>${esc(value)}</dd>`).join('')}</dl>${product.metadata?.description?`<details class="catalog-official-description"><summary>官方摘要</summary><p>${esc(product.metadata.description)}</p></details>`:''}<div class="catalog-product-links">${link(product.metadata?.official_product_url,'官方产品目录')}${link(product.metadata?.official_documentation_url,'官方产品说明')}${link(product.metadata?.level_evidence,'元数据依据')}</div></div>`;
      section.append(details);
    }
    node.append(section);
  }
}
export function initialize(products) {
  catalog=products || [];
  if(!bound){$('#catalog-search').addEventListener('input',render);bound=true;}
  render();
}

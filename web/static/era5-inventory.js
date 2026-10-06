const $ = selector => document.querySelector(selector);
const escape = value => String(value ?? '').replaceAll('&','&amp;').replaceAll('<','&lt;').replaceAll('>','&gt;').replaceAll('"','&quot;');
const unique = values => [...new Set(values)].sort((a,b)=>String(a).localeCompare(String(b),undefined,{numeric:true}));
const bytes = value => value >= 1073741824 ? (value/1073741824).toFixed(1)+' GiB' : value >= 1048576 ? (value/1048576).toFixed(1)+' MiB' : value+' B';
const datasetName = id => id==='era5:unclassified'?'历史文件 · 数据集待识别':id.replace(/^era5:reanalysis-/,'').replaceAll('-',' ');
const areaName = area => area?.length===4 ? `N ${area[0]} / W ${area[1]} / S ${area[2]} / E ${area[3]}` : '区域未记录';
let inventory = {files:[]}, bound = false;

export function interval(file) {
  const start = Date.parse(file.start),end = Date.parse(file.end);
  return Number.isFinite(start) && Number.isFinite(end) && end>start ? {start,end} : null;
}

export function intersectsMonth(file,year,month) {
  const bounds=interval(file);
  return bounds && bounds.start<Date.UTC(year,month+1,1) && bounds.end>Date.UTC(year,month,1);
}

export function inventorySummary(files) {
  return {files:files.length,verified:files.filter(file=>file.verified).length,bytes:files.reduce((sum,file)=>sum+file.bytes,0),datasets:new Set(files.filter(file=>file.dataset!=='era5:unclassified').map(file=>file.dataset)).size,
    variables:new Set(files.flatMap(file=>file.variables || [])).size,unknownDates:files.filter(file=>!interval(file)).length};
}

export function monthDays(files,year,month) {
  const length=new Date(Date.UTC(year,month+1,0)).getUTCDate();
  return Array.from({length},(_,index)=>{
    const start=Date.UTC(year,month,index+1),end=Date.UTC(year,month,index+2);
    const own=files.filter(file=>{const bounds=interval(file);return bounds && bounds.start<end && bounds.end>start;});
    return {day:index+1,files:own.length,verified:own.filter(file=>file.verified).length};
  });
}

function select(id,values,label,format=value=>value) {
  const node=$('#era5-inventory-'+id),previous=node.value;
  node.replaceChildren(new Option(label,''));
  for(const value of values) node.add(new Option(format(value),value));
  if(values.includes(previous)) node.value=previous;
}

function fileLevels(file) {return file.levels?.length?file.levels:[file.dataset.includes('pressure-levels')?'层未记录':'不适用'];}

function filteredFiles() {
  return inventory.files.filter(file=>{
    const dataset=$('#era5-inventory-dataset').value,variable=$('#era5-inventory-variable').value;
    const level=$('#era5-inventory-level').value,area=$('#era5-inventory-area').value;
    return (!dataset || dataset===file.dataset) && (!variable || (file.variables?.length?file.variables:['变量未记录']).includes(variable)) &&
      (!level || fileLevels(file).includes(level)) && (!area || areaName(file.area)===area);
  });
}

function refreshFilters(changed='dataset') {
  if(changed==='dataset') {
    const subset=inventory.files.filter(file=>!$('#era5-inventory-dataset').value || file.dataset===$('#era5-inventory-dataset').value);
    select('variable',unique(subset.flatMap(file=>file.variables?.length?file.variables:['变量未记录'])),'全部变量');
    select('level',unique(subset.flatMap(fileLevels)),'全部层');
    select('area',unique(subset.map(file=>areaName(file.area))),'全部区域');
  }
  const files=filteredFiles(),years=new Set();
  for(const file of files) {
    const bounds=interval(file);
    if(!bounds)continue;
    const first=new Date(bounds.start).getUTCFullYear(),last=new Date(bounds.end-1).getUTCFullYear();
    for(let year=first;year<=last && year-first<100;year++)years.add(String(year));
  }
  const node=$('#era5-inventory-year'),previous=node.value;
  const values=[...years].sort().reverse();
  node.replaceChildren(...values.map(year=>new Option(year,year)));
  if(values.includes(previous))node.value=previous;
  render();
}

function render() {
  const files=filteredFiles(),summary=inventorySummary(files),year=Number($('#era5-inventory-year').value);
  const metrics=[['数据集',summary.datasets],['本地文件',summary.files],['已核验',summary.verified],['容量',bytes(summary.bytes)]];
  $('#era5-inventory-summary').innerHTML=metrics.map(([name,value])=>`<div><span>${name}</span><strong>${escape(value)}</strong></div>`).join('');
  $('#era5-inventory-empty').classList.toggle('hidden',files.length>0);
  $('#era5-inventory-calendars').replaceChildren();
  const unknown=files.filter(file=>!interval(file));
  for(const dataset of unique(files.map(file=>file.dataset))) {
    const own=files.filter(file=>file.dataset===dataset),article=document.createElement('article');
    article.className='inventory-dataset';
    const names=unique(own.flatMap(file=>file.variables || []));
    article.innerHTML=`<header><strong>${escape(datasetName(dataset))}</strong><span>${own.length} 文件 · ${bytes(own.reduce((sum,file)=>sum+file.bytes,0))}</span></header><div class="inventory-variables">${names.map(name=>`<span title="${escape(name)}">${escape(name.replaceAll('_',' '))}</span>`).join('')}</div>`;
    const months=document.createElement('div');months.className='inventory-months';
    for(let month=0;year && own.some(interval) && month<12;month++) {
      const matched=own.filter(file=>intersectsMonth(file,year,month));
      const button=document.createElement('button');button.type='button';button.className='inventory-month'+(matched.length?' has-data':'');
      if(matched.some(file=>!file.verified))button.classList.add('unverified');
      button.disabled=!matched.length;button.dataset.month=String(month);button.dataset.dataset=dataset;
      button.innerHTML=`<span>${month+1} 月</span><strong>${matched.length || '·'}</strong>`;
      button.title=matched.length?`${year}-${String(month+1).padStart(2,'0')} · ${matched.length} 本地文件 · ${matched.filter(file=>file.verified).length} 已核验${dataset.includes('monthly-means')?' · 月均':''}`:`${year || '无覆盖日期'}-${month+1} · 未索引到本地文件`;
      button.addEventListener('click',()=>showDetails(matched,`${datasetName(dataset)} · ${year}-${String(month+1).padStart(2,'0')}`,{year,month}));
      months.append(button);
    }
    article.append(months);
    const unresolved=own.filter(file=>!interval(file));
    if(unresolved.length){const button=document.createElement('button');button.type='button';button.className='inventory-unresolved';button.textContent=`${unresolved.length} 文件 · 覆盖日期未记录`;button.addEventListener('click',()=>showDetails(unresolved,datasetName(dataset)));article.append(button);}
    $('#era5-inventory-calendars').append(article);
  }
  const note=[];
  if(summary.files-summary.verified)note.push(`${summary.files-summary.verified} 本地文件待核验`);
  if(summary.unknownDates)note.push(`${summary.unknownDates} 文件缺少日期溯源`);
  if(inventory.scanned_at)note.push('扫描 '+inventory.scanned_at.replace('T',' ').replace(/\.\d+Z$/,'Z'));
  $('#era5-inventory-status').textContent=note.join(' · ');
  $('#era5-inventory-detail').classList.add('hidden');
  if(!year && unknown.length)showDetails(unknown,'已有文件');
}

function showDetails(files,title,month) {
  const node=$('#era5-inventory-detail');node.classList.remove('hidden');
  let calendar='';
  if(month){
    const offset=new Date(Date.UTC(month.year,month.month,1)).getUTCDay();
    calendar=`<div class="inventory-day-grid" aria-label="本月逐日文件覆盖 UTC">${['日','一','二','三','四','五','六'].map(day=>`<span class="inventory-weekday">${day}</span>`).join('')}${'<i></i>'.repeat(offset)}${monthDays(files,month.year,month.month).map(day=>`<div class="inventory-day${day.files?' has-data':''}${day.files>day.verified?' unverified':''}" title="${month.year}-${String(month.month+1).padStart(2,'0')}-${String(day.day).padStart(2,'0')} UTC · ${day.files} 本地文件"><span>${day.day}</span><strong>${day.files || '·'}</strong></div>`).join('')}</div>`;
  }
  node.innerHTML=`<h2>${escape(title)}</h2>${calendar}<div class="inventory-table-wrap"><table><thead><tr><th>核验</th><th>日期 UTC</th><th>变量</th><th>层</th><th>区域</th><th>时次 UTC</th><th>文件 / 容量</th></tr></thead><tbody>${files.slice(0,100).map(file=>`<tr><td>${file.verified?'已核验':file.state==='error'?'文件异常':'待核验'}</td><td>${file.start?escape(file.start.slice(0,10)+' ≤ t < '+file.end.slice(0,10)):'缺少日期溯源'}</td><td>${escape((file.variables?.length?file.variables:['缺少变量溯源']).join(', '))}</td><td>${escape(fileLevels(file).join(', '))}</td><td>${escape(areaName(file.area))}</td><td>${escape(file.times?.length?file.times.join(', '):'时次未记录')}</td><td>${escape(file.name)}<small>${bytes(file.bytes)}</small></td></tr>`).join('')}</tbody></table></div>${files.length>100?'<p>前 100 / '+files.length+' 文件</p>':''}`;
}

export function initialize(payload) {
  inventory={...payload,files:payload.files || []};
  select('dataset',unique(inventory.files.map(file=>file.dataset)),'全部数据集',datasetName);
  if(!bound) {
    for(const id of ['dataset','variable','level','area'])$('#era5-inventory-'+id).addEventListener('change',()=>refreshFilters(id));
    $('#era5-inventory-year').addEventListener('change',render);
    bound=true;
  }
  refreshFilters();
}

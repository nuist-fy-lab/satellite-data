import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import test from 'node:test';
import {groupCatalog,productDescription,safeURL,dateText,missions} from './static/catalog.js';
import {inventorySummary,intersectsMonth,interval,monthDays} from './static/era5-inventory.js';

test('catalog hierarchy retains joint platforms, source families and official identities',()=>{
  const items=[{source_id:'goes18',satellite:'GOES-18',instrument:'ABI',product_key:'goes18:RadF'},{source_id:'goes19',satellite:'GOES-19',instrument:'GLM',product_key:'goes19:LCFA'},{source_id:'modis',satellite:'Terra + Aqua',instrument:'MODIS',product_key:'modis:joint'}];
  const before=JSON.stringify(items),groups=groupCatalog(items);
  assert.equal(groups.length,2);assert.equal(groups[0].satellites.size,2);
  assert.ok(groups[1].satellites.has('Terra + Aqua'));assert.equal(JSON.stringify(items),before);
});
test('mission times retain actual precision and never fabricate midnight',()=>{
  assert.equal(dateText(missions.EarthCARE.launch),'2024-05-28 22:20 UTC');
  assert.equal(dateText(missions['Himawari-8'].launch),'2014-10-07 05:16 UTC');
  assert.equal(dateText('2022-12-13'),'2022-12-13 · 日期精度');
  assert.equal(missions.MTG.launches.at(-1)[1],'2026-08-27');
});
test('catalog links reject script URLs, credentials and insecure links',()=>{
  for(const value of ['javascript:alert(1)','http://example.com','https://user:password@example.com','relative/path'])assert.equal(safeURL(value),'');
  assert.equal(safeURL('https://example.com/product?id=2'),'https://example.com/product?id=2');
});
test('purpose distinguishes non-image data and familiar products',()=>{
  assert.match(productDescription({instrument:'Orbit'}),/轨道/);
  assert.match(productDescription({display_name:'MODIS Aerosol Optical Depth'}),/气溶胶/);
  assert.match(productDescription({display_name:'GOES Cloud Top Height'}),/云顶/);
});
test('inventory uses half-open coverage and counts unknown files without inventing dates',()=>{
  const files=[{dataset:'era5:single',bytes:100,verified:true,start:'2024-01-01T00:00:00Z',end:'2024-02-01T00:00:00Z',variables:['temperature']},{dataset:'era5:unclassified',bytes:50,verified:false,variables:[]}];
  assert.equal(intersectsMonth(files[0],2024,0),true);assert.equal(intersectsMonth(files[0],2024,1),false);
  assert.equal(interval(files[1]),null);assert.equal(Boolean(intersectsMonth(files[1],2024,0)),false);
  assert.deepEqual(inventorySummary(files),{files:2,verified:1,bytes:150,datasets:1,variables:1,unknownDates:1});
  assert.equal(monthDays(files,2024,1).length,29);
  assert.equal(monthDays(files,2024,0).filter(day=>day.files).length,31);
  assert.equal(monthDays(files,2024,1).filter(day=>day.files).length,0);
});
test('removed routes are aliases, not additional navigation or subscription functionality',async()=>{
  const html=await readFile(new URL('./static/index.html',import.meta.url),'utf8');
  for(const route of ['sync','cli','sources'])assert.equal(html.includes(`href="/${route}"`),false);
  assert.match(html,/data-era5-tab="inventory" class="active"/);
  assert.ok(html.indexOf('id="cli-panel"')>html.indexOf('id="access-manager"'));
});

const sourcesNode = document.querySelector('#sources');
const sourceSelect = document.querySelector('#source-select');
const catalogNode = document.querySelector('#catalog');
const healthNode = document.querySelector('#health');

const tierLabels = {
  anonymous: 'A · 匿名直连',
  token: 'B · 登录 / Token',
  special: 'C · 特殊访问',
};

async function json(url) {
  const response = await fetch(url, {headers: {'Accept': 'application/json'}});
  const payload = await response.json();
  if (!response.ok) throw new Error(payload.error || `HTTP ${response.status}`);
  return payload;
}

function renderSources(sources) {
  sourcesNode.replaceChildren();
  sourceSelect.replaceChildren();
  const counts = {anonymous: 0, token: 0, special: 0};
  for (const source of sources) {
    counts[source.tier] = (counts[source.tier] || 0) + 1;
    const card = document.createElement('article');
    card.className = 'source-card';
    const capabilities = source.capabilities.map(value => `<span>${value}</span>`).join('');
    card.innerHTML = `
      <div class="source-head"><strong>${source.name}</strong><span class="tier ${source.tier}">${tierLabels[source.tier] || source.tier}</span></div>
      <p>${source.description || ''}</p>
      <small>${source.method}</small>
      <div class="caps">${capabilities}</div>`;
    sourcesNode.append(card);

    const option = document.createElement('option');
    option.value = source.id;
    option.textContent = source.name;
    sourceSelect.append(option);
  }
  for (const tier of Object.keys(counts)) {
    const node = document.querySelector(`#tier-${tier}`);
    if (node) node.textContent = counts[tier];
  }
}

async function loadSources() {
  const payload = await json('/api/v1/sources');
  renderSources(payload.sources);
}

async function loadCatalog() {
  const source = sourceSelect.value;
  if (!source) return;
  catalogNode.className = 'catalog empty';
  catalogNode.textContent = '读取官方目录…';
  try {
    const payload = await json(`/api/v1/catalog?source=${encodeURIComponent(source)}`);
    if (!payload.collections.length) {
      catalogNode.textContent = '当前未发现产品。';
      return;
    }
    const table = document.createElement('table');
    table.innerHTML = '<thead><tr><th>产品</th><th>仪器</th><th>级别</th><th>时次</th></tr></thead>';
    const body = document.createElement('tbody');
    for (const collection of payload.collections) {
      const row = document.createElement('tr');
      for (const value of [collection.product, collection.instrument, collection.level || '—', collection.cadence || '—']) {
        const cell = document.createElement('td');
        cell.textContent = value;
        row.append(cell);
      }
      body.append(row);
    }
    table.append(body);
    catalogNode.replaceChildren(table);
    catalogNode.className = 'catalog';
  } catch (error) {
    catalogNode.textContent = error.message;
  }
}

async function boot() {
  try {
    await json('/healthz');
    healthNode.textContent = '服务正常';
    healthNode.classList.add('ok');
    await loadSources();
  } catch (error) {
    healthNode.textContent = '服务异常';
    healthNode.title = error.message;
  }
}

document.querySelector('#refresh').addEventListener('click', loadSources);
document.querySelector('#load-catalog').addEventListener('click', loadCatalog);
boot();

import {mkdir, writeFile, appendFile} from 'node:fs/promises';

const target = 'https://satellite.rs.fyzure.fyi/downloads';
const directory = 'dist/audit';
await mkdir(directory, {recursive: true});
const results = [];
for (const strategy of ['mobile', 'desktop']) {
  const endpoint = new URL('https://www.googleapis.com/pagespeedonline/v5/runPagespeed');
  endpoint.searchParams.set('url', target);
  endpoint.searchParams.set('strategy', strategy);
  for (const category of ['performance', 'accessibility', 'best-practices', 'seo']) endpoint.searchParams.append('category', category);
  let payload, summary;
  try {
    const response = await fetch(endpoint, {signal: AbortSignal.timeout(120_000)});
    payload = await response.json();
    if (!response.ok || !payload.lighthouseResult || payload.lighthouseResult.runtimeError) {
      summary = {strategy, status: 'unavailable', http_status: response.status,
        reason: payload.error?.status || payload.lighthouseResult?.runtimeError?.code || 'no_lighthouse_result'};
    } else {
      const result = payload.lighthouseResult;
      summary = {strategy, status: 'completed', requested_url: result.requestedUrl,
        final_url: result.finalDisplayedUrl || result.finalUrl,
        categories: Object.fromEntries(Object.entries(result.categories || {}).map(([id, category]) => [id, category.score])),
        authenticated_download_page_tested: false,
        scope: 'Public entry only; no credentials or session cookies supplied. A login redirect is not a download-page audit.'};
    }
  } catch (error) {
    summary = {strategy, status: 'unavailable', reason: error.name};
    payload = summary;
  }
  await writeFile(`${directory}/pagespeed-${strategy}.json`, JSON.stringify(payload, null, 2));
  results.push(summary);
  console.log(JSON.stringify(summary));
}
await writeFile(`${directory}/pagespeed-summary.json`, JSON.stringify(results, null, 2));
if (process.env.GITHUB_STEP_SUMMARY) {
  await appendFile(process.env.GITHUB_STEP_SUMMARY, '## Public cloud Lighthouse audit\n\n' +
    'PageSpeed Insights service only; no browser is installed or launched by this job. No portal credentials are shared.\n\n' +
    results.map(item => `- ${item.strategy}: ${item.status}` + (item.status === 'unavailable'
      ? ` (${item.reason}, HTTP ${item.http_status ?? 'n/a'})`
      : `; final URL ${item.final_url}; scores ${JSON.stringify(item.categories)}`)).join('\n') +
    '\n\nPublic login-page results are not scores for the authenticated download page.\n');
}
if (results.some(item => item.status !== 'completed')) process.exitCode = 1;

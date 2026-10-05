import {createDecipheriv, createHash} from 'node:crypto';
import {mkdirSync, readFileSync, statSync, writeFileSync} from 'node:fs';
import {homedir} from 'node:os';
import {join} from 'node:path';
import {DatabaseSync} from 'node:sqlite';

// Run only on the credential-owning host as the trusted service user.
const root = join(homedir(), '.local/state/satellite-data');
const directory = join(root, 'verification');
const stateFile = join(directory, 'era5-access.json');
const output = join(directory, 'era5-access.nc');
const collection = 'reanalysis-era5-single-levels';
const inputs = {
  product_type: ['reanalysis'], variable: ['2m_temperature'],
  year: ['2024'], month: ['01'], day: ['01'], time: ['00:00'],
  area: [32.25, 118.5, 32, 118.75], data_format: 'netcdf', download_format: 'unarchived',
};
const maxBytes = 1024 * 1024;
const report = (event, values = {}) => console.log(JSON.stringify({event, ...values}));
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
const idPattern = /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/;

async function main() {
  process.umask(0o077);
  mkdirSync(directory, {recursive: true, mode: 0o700});
  const db = new DatabaseSync(join(root, 'access.sqlite3'));
  let saved;
  try {
    saved = JSON.parse(readFileSync(stateFile, 'utf8'));
  } catch (error) {
    if (error.code !== 'ENOENT') throw new Error('Cannot read existing verification state');
  }
  const rows = db.prepare(`SELECT a.id AS account_id, c.id AS credential_id,
    a.metadata AS account, c.metadata AS credential, c.encrypted_secret
    FROM accounts a JOIN credentials c ON a.credential_id=c.id`).all();
  const row = rows.find(row => {
    const account = JSON.parse(row.account);
    const credential = JSON.parse(row.credential);
    return account.source_id === 'era5' && account.enabled && credential.kind === 'cds' && credential.enabled;
  });
  if (!row) throw new Error('No enabled ERA5 account with a CDS credential');
  const keyPath = join(homedir(), '.config/satellite-data/master.key');
  if (statSync(keyPath).mode & 0o077) throw new Error('Credential key permissions are not private');
  const key = readFileSync(keyPath);
  if (key.length !== 32) throw new Error('Unexpected credential key length');
  const encrypted = Buffer.from(row.encrypted_secret);
  const decipher = createDecipheriv('aes-256-gcm', key, encrypted.subarray(0, 12));
  decipher.setAAD(Buffer.from('satellite-data-central-v1'));
  decipher.setAuthTag(encrypted.subarray(-16));
  const plain = Buffer.concat([decipher.update(encrypted.subarray(12, -16)), decipher.final()]);
  key.fill(0);
  const fingerprint = createHash('sha256').update(plain).digest('hex');
  const secret = JSON.parse(plain.toString('utf8'));
  plain.fill(0);
  if (typeof secret.token !== 'string' || !secret.token) throw new Error('CDS credential has no Token');
  const persist = () => writeFileSync(stateFile, JSON.stringify(saved), {mode: 0o600});
  async function api(path, body) {
    const response = await fetch('https://cds.climate.copernicus.eu/api' + path, {
      method: body ? 'POST' : 'GET', redirect: 'error', signal: AbortSignal.timeout(60000),
      headers: {'PRIVATE-TOKEN': secret.token, 'Content-Type': 'application/json', Accept: 'application/json'},
      body: body ? JSON.stringify(body) : undefined,
    });
    if (!response.ok) throw new Error(`CDS API HTTP ${response.status}; check Token, licence and parameters`);
    return response.json();
  }
  if (!saved) {
    const schema = await api('/retrieve/v1/processes/' + collection);
    if (schema.id !== collection || Object.keys(inputs).some(name => !schema.inputs?.[name])) {
      throw new Error('CDS process schema does not match bounded request');
    }
    // Persist before POST so a network ambiguity never triggers another submission.
    saved = {collection, inputs, account_id: row.account_id, credential_id: row.credential_id,
      created: new Date().toISOString(), submission: 'pending'};
    persist();
    const job = await api('/retrieve/v1/processes/' + collection + '/execution', {inputs});
    if (!idPattern.test(job.jobID || '') || !job.status) throw new Error('CDS response lacks a valid job');
    saved.jobID = job.jobID;
    saved.submission = 'accepted';
    persist();
    report('submitted', {jobID: saved.jobID, status: job.status});
  }
  if (!idPattern.test(saved.jobID || '') || saved.account_id !== row.account_id || saved.credential_id !== row.credential_id) {
    throw new Error('Submission identity is ambiguous; inspect CDS before resubmitting');
  }
  const binding = db.prepare('SELECT * FROM cds_jobs WHERE job_id=?').get(saved.jobID);
  if (binding && (binding.account_id !== row.account_id || binding.credential_id !== row.credential_id || binding.token_hash !== fingerprint)) {
    throw new Error('Existing CDS job binding differs from this credential');
  }
  db.prepare('INSERT OR IGNORE INTO cds_jobs(job_id,account_id,credential_id,token_hash) VALUES(?,?,?,?)')
    .run(saved.jobID, row.account_id, row.credential_id, fingerprint);
  const deadline = Date.now() + 10 * 60 * 1000;
  let previous = '';
  while (true) {
    const job = await api('/retrieve/v1/jobs/' + saved.jobID);
    if (job.jobID !== saved.jobID) throw new Error('CDS returned an unexpected job');
    if (job.status !== previous) report('job', {jobID: saved.jobID, status: job.status});
    previous = job.status;
    saved.status = job.status;
    persist();
    if (job.status === 'successful') break;
    if (['failed', 'dismissed', 'cancelled'].includes(job.status)) throw new Error(`CDS job ${job.status}`);
    if (Date.now() >= deadline) throw new Error('CDS queue wait exceeded ten minutes; resume this same job later');
    await sleep(15000);
  }
  const result = await api('/retrieve/v1/jobs/' + saved.jobID + '/results');
  const asset = result.asset?.value;
  const url = new URL(asset?.href || '');
  if (url.protocol !== 'https:' || url.username || url.password || !Number.isFinite(asset['file:size']) || asset['file:size'] <= 0 || asset['file:size'] > maxBytes) {
    throw new Error('Result URL or size does not match bounded request');
  }
  const start = Date.now();
  // Retrieval URLs may carry their own authorization. Never log or persist them.
  const response = await fetch(url, {redirect: 'error', signal: AbortSignal.timeout(60000)});
  if (!response.ok) throw new Error(`CDS result HTTP ${response.status}`);
  const chunks = [];
  let size = 0;
  for await (const chunk of response.body) {
    size += chunk.length;
    if (size > maxBytes) throw new Error('Result exceeded bounded download limit');
    chunks.push(chunk);
  }
  const data = Buffer.concat(chunks);
  const classic = data.subarray(0, 3).equals(Buffer.from('CDF'));
  const hdf5 = data.subarray(0, 8).equals(Buffer.from([137, 72, 68, 70, 13, 10, 26, 10]));
  if (size !== asset['file:size'] || (!classic && !hdf5)) throw new Error('Result size or NetCDF signature mismatch');
  writeFileSync(output, data, {mode: 0o600});
  saved.bytes = size;
  saved.sha256 = createHash('sha256').update(data).digest('hex');
  saved.format = classic ? 'NetCDF classic' : 'NetCDF4/HDF5';
  saved.verified_at = new Date().toISOString();
  persist();
  db.close();
  secret.token = '';
  report('download_verified', {jobID: saved.jobID, bytes: size, format: saved.format, elapsed_ms: Date.now() - start, sha256: saved.sha256});
}

main().catch(error => {
  // Exception causes may contain request headers or URLs, so only known messages are printed.
  const message = error instanceof Error ? error.message : '';
  const safe = /^(CDS |No enabled |Credential key |Unexpected credential |Cannot read |Submission identity |Existing CDS job |Result |CDS process)/.test(message);
  report('error', {message: safe ? message : 'Verification failed; no secret or URL details logged'});
  process.exitCode = 1;
});

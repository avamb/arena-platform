#!/usr/bin/env node
/**
 * ops/loadtest/monitor.mjs — one CSV row per INTERVAL seconds with the
 * stand's resource use (docker stats of the arena containers) and a Postgres
 * snapshot (connections, lock waits, longest transaction, deadlocks, queue
 * backlogs, expired-but-unreleased holds, database size). Run it on the
 * machine that can see the containers — the stand itself, or a laptop
 * running the local compose — for the whole staircase / soak, and read the
 * "first vs peak vs last" of every column afterwards: a column that ends
 * above where it started is a leak or a queue that never drained.
 *
 *   node ops/loadtest/monitor.mjs                          # until Ctrl+C
 *   UNTIL_CONTAINER_GONE=arena_k6_soak node ops/loadtest/monitor.mjs
 *   DURATION_SECONDS=3600 OUT=./results/soak.csv node ops/loadtest/monitor.mjs
 *
 * Env:
 *   OUT                   CSV path, default ops/loadtest/results/monitor-<ts>.csv
 *   INTERVAL              seconds between samples, default 60
 *   CONTAINERS            comma-separated container names, default the local compose set
 *   PG_CONTAINER          postgres container for `docker exec ... psql`, default arena_postgres
 *   PG_USER / PG_DB       default arena / arena
 *   DURATION_SECONDS      stop after this long (default: run until stopped)
 *   UNTIL_CONTAINER_GONE  stop once this container is no longer running (a k6 run)
 *
 * Summarise a file: node ops/loadtest/monitor.mjs --summary <csv>
 */
import { execFileSync } from 'node:child_process';
import { appendFileSync, writeFileSync, readFileSync, mkdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = dirname(fileURLToPath(import.meta.url));

if (process.argv[2] === '--summary') {
  const rows = readFileSync(process.argv[3], 'utf8').trim().split('\n').filter((l) => !l.startsWith('#'));
  const h = rows[0].split(',');
  const d = rows.slice(1).map((r) => r.split(','));
  console.log(`samples ${d.length}  first ${d[0][0]}  last ${d[d.length - 1][0]}`);
  console.log('column'.padEnd(28) + 'first'.padStart(10) + 'peak'.padStart(10) + 'last'.padStart(10));
  for (let i = 1; i < h.length; i += 1) {
    const v = d.map((r) => parseFloat(r[i])).filter((x) => !Number.isNaN(x));
    if (!v.length) continue;
    console.log(h[i].padEnd(28) + String(v[0]).padStart(10) + String(Math.max(...v)).padStart(10) + String(v[v.length - 1]).padStart(10));
  }
  process.exit(0);
}

const INTERVAL = parseInt(process.env.INTERVAL || '60', 10);
const CONTAINERS = (process.env.CONTAINERS || 'arena_api,arena_worker,arena_postgres,arena_stripe_stub').split(',').map((s) => s.trim()).filter(Boolean);
const PG_CONTAINER = process.env.PG_CONTAINER || 'arena_postgres';
const PG_USER = process.env.PG_USER || 'arena';
const PG_DB = process.env.PG_DB || 'arena';
const DURATION_SECONDS = parseInt(process.env.DURATION_SECONDS || '0', 10);
const UNTIL = process.env.UNTIL_CONTAINER_GONE || '';
const OUT = process.env.OUT || join(HERE, 'results', `monitor-${new Date().toISOString().replace(/[-:T]/g, '').slice(0, 12)}.csv`);

const SQL = `select
  (select count(*) from pg_stat_activity where datname='${PG_DB}') as conns,
  (select count(*) from pg_stat_activity where datname='${PG_DB}' and state='active') as active,
  (select count(*) from pg_stat_activity where wait_event_type='Lock') as waiting_on_lock,
  (select coalesce(extract(epoch from max(now()-xact_start)),0)::int from pg_stat_activity where state<>'idle' and datname='${PG_DB}') as longest_tx_s,
  (select deadlocks from pg_stat_database where datname='${PG_DB}') as deadlocks_total,
  (select count(*) from outbox_events where processed_at is null) as outbox_backlog,
  (select count(*) from worker_jobs where status='pending') as jobs_pending,
  (select count(*) from delivery_jobs where status='pending') as delivery_pending,
  (select count(*) from outbox_events where dead_lettered_at is not null) as dead_letters,
  (select count(*) from reservations where state in ('draft','active') and expires_at < now()) as expired_unreleased,
  pg_database_size('${PG_DB}')/1024/1024 as db_mb`.replace(/\n/g, ' ');

function sh(cmd, args) {
  try { return execFileSync(cmd, args, { encoding: 'utf8', timeout: 20000, stdio: ['ignore', 'pipe', 'ignore'] }).trim(); } catch (e) { return ''; }
}

function running(name) {
  return sh('docker', ['inspect', '-f', '{{.State.Running}}', name]) === 'true';
}

function mib(mem) {
  const m = /([\d.]+)\s*(B|KiB|MiB|GiB)/.exec(mem || '');
  if (!m) return '';
  const n = parseFloat(m[1]);
  return { B: n / 1048576, KiB: n / 1024, MiB: n, GiB: n * 1024 }[m[2]].toFixed(1);
}

mkdirSync(dirname(OUT), { recursive: true });
writeFileSync(OUT, 'ts,' + CONTAINERS.flatMap((c) => [`${c}_cpu`, `${c}_mem_mib`]).join(',')
  + ',conns,active,waiting_on_lock,longest_tx_s,deadlocks_total,outbox_backlog,jobs_pending,delivery_pending,dead_letters,expired_unreleased,db_mb\n');
console.log(`monitor: writing ${OUT} every ${INTERVAL}s`);

const started = Date.now();
let ticks = 0;
function sample() {
  const ts = new Date().toISOString();
  const stats = sh('docker', ['stats', '--no-stream', '--format', '{{.Name}}|{{.CPUPerc}}|{{.MemUsage}}', ...CONTAINERS]);
  const byName = {};
  for (const line of stats.split('\n')) {
    const [name, cpu, mem] = line.trim().split('|');
    if (!name) continue;
    byName[name] = [String(cpu || '').replace('%', ''), mib(mem)];
  }
  const db = sh('docker', ['exec', PG_CONTAINER, 'psql', '-U', PG_USER, '-d', PG_DB, '-Atc', SQL]).replace(/\|/g, ',');
  appendFileSync(OUT, [ts, ...CONTAINERS.flatMap((c) => byName[c] || ['', '']), db].join(',') + '\n');
  ticks += 1;
}

function done() {
  if (DURATION_SECONDS > 0 && Date.now() - started >= DURATION_SECONDS * 1000) return true;
  if (UNTIL && ticks > 1 && !running(UNTIL)) return true;
  return false;
}

sample();
const timer = setInterval(() => {
  sample();
  if (done()) {
    clearInterval(timer);
    appendFileSync(OUT, `# stopped after ${ticks} samples\n`);
    console.log(`monitor: stopped after ${ticks} samples, ${OUT}`);
  }
}, INTERVAL * 1000);
process.on('SIGINT', () => { clearInterval(timer); appendFileSync(OUT, `# stopped after ${ticks} samples\n`); process.exit(0); });

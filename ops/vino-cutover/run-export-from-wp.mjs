// Reads a PHP-serialized bil24_acf_sync option from stdin and runs
// ops/bil24-export/export.mjs with the credentials in the child's environment
// only. Never prints or writes the token; prints fid + sha256 prefix.
import { createHash } from 'node:crypto';
import { spawn } from 'node:child_process';

let raw = '';
for await (const c of process.stdin) raw += c;
const str = (k) => raw.match(new RegExp(`s:\\d+:"${k}";s:\\d+:"([^"]*)"`))?.[1] ?? '';
const fid = raw.match(/s:3:"fid";(?:i:(\d+)|s:\d+:"(\d+)")/);
const FID = fid ? (fid[1] || fid[2]) : '';
const TOKEN = str('token');
const env = str('environment');
const base = env === 'test' ? str('base_url_test') : str('base_url_prod');
if (!FID || !TOKEN) { console.error('fid/token not found in option'); process.exit(2); }
console.log(`fid=${FID} env=${env} base=${base} locale=${str('locale')} token_len=${TOKEN.length} token_sha256=${createHash('sha256').update(TOKEN).digest('hex').slice(0, 12)}`);
if (process.argv[2] === '--dry') process.exit(0);

const url = (base || 'https://api.bil24.pro').replace(/\/+$/, '').replace(/\/json$/, '') + '/json';
const child = spawn(process.execPath, ['ops/bil24-export/export.mjs'], {
  stdio: 'inherit',
  env: {
    ...process.env,
    BIL24_ENV_FILE: 'NUL-no-file',
    BIL24_FID: FID,
    BIL24_TOKEN: TOKEN,
    BIL24_URL: url,
    BIL24_LOCALES: process.env.BIL24_LOCALES || 'ru-RU,en-GB,he-IL',
  },
});
child.on('exit', (code) => process.exit(code ?? 1));

// GET_ALL_ACTIONS against the site's configured base_url_prod with the site's
// own fid/token (read from a PHP-serialized bil24_acf_sync option on stdin).
// Never prints the token.
let raw = '';
for await (const c of process.stdin) raw += c;
const s = (k) => raw.match(new RegExp(`s:\\d+:"${k}";s:\\d+:"([^"]*)"`))?.[1];
const fm = raw.match(/s:3:"fid";(?:i:(\d+)|s:\d+:"(\d+)")/);
const fid = fm ? Number(fm[1] || fm[2]) : 0;
const url = s('base_url_prod');
console.log('url', url, 'fid', fid, 'token_len', (s('token') || '').length);
const r = await fetch(url, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ fid, token: s('token'), locale: 'en', command: 'GET_ALL_ACTIONS' }) });
const j = await r.json();
console.log('http', r.status, 'resultCode', j.resultCode, j.description);
for (const a of j.actionList || []) for (const e of a.actionEventList || []) console.log(' ', e.actionEventId, e.day, e.time, a.actionName, 'venue', e.venueId, 'avail', e.availability);

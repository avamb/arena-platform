import { createHash } from 'node:crypto';

const expected = process.argv.find(x => x.startsWith('--expected-sha256='))?.split('=', 2)[1];
if (!/^[0-9a-f]{64}$/i.test(expected ?? '')) throw new Error('Probe requires a SHA-256 test digest.');
const chunks = [];
for await (const chunk of process.stdin) chunks.push(Buffer.from(chunk));
const value = Buffer.concat(chunks);
if (!value.length || createHash('sha256').update(value).digest('hex') !== expected.toLowerCase()) throw new Error('Credential pipe check failed.');
process.stdout.write('{"status":"PASS","received":"credential","api_calls":0}\n');

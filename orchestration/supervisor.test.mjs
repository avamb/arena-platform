import assert from 'node:assert/strict';
import test from 'node:test';
import { spawnSync } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const SUPERVISOR = path.join(ROOT, 'orchestration', 'supervisor.mjs');
const QUEUE = path.join(ROOT, 'orchestration', 'supervisor-queue-v1.json');

function run(args) {
  return spawnSync(process.execPath, [SUPERVISOR, ...args], { cwd: ROOT, encoding: 'utf8', windowsHide: true });
}

test('supervisor self-test proves mock, budget, retry, isolation and recovery guards', () => {
  const result = run(['--self-test']);
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /SUPERVISOR_SELF_TEST_PASSED/);
  assert.match(result.stdout, /api_calls":0/);
  assert.match(result.stdout, /codex_review":"not_invoked_in_mock/);
});

test('mock queue preflight never reads credentials or calls API', () => {
  const result = run(['--preflight', '--mock', '--queue', 'orchestration/supervisor-queue-v1.json']);
  assert.equal(result.status, 0, result.stderr);
  const payload = JSON.parse(result.stdout.trim());
  assert.equal(payload.status, 'SUPERVISOR_PREFLIGHT_PASSED');
  assert.equal(payload.api_calls, 0);
});

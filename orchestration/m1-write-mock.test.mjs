import test from 'node:test';
import assert from 'node:assert/strict';
import { buildM1Prompt, createM1ToolPolicy } from './m1-write-policy.mjs';
import { mkdtempSync, mkdirSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { readFileSync } from 'node:fs';

test('M1 policy allows approved read/write and denies shell/outside paths', () => {
  const root = mkdtempSync(path.join(os.tmpdir(), 'venue-m1-policy-'));
  mkdirSync(path.join(root, 'm1_product'), { recursive: true });
  writeFileSync(path.join(root, 'PRODUCT_SPEC.md'), 'spec\n');
  const policy = createM1ToolPolicy({ worktreeRoot: root, readPaths: ['PRODUCT_SPEC.md'], writePaths: ['m1_product'], forbiddenPaths: ['arena_new'] });
  assert.equal(policy.authorize('Read', { file_path: 'PRODUCT_SPEC.md' }).allowed, true);
  assert.equal(policy.authorize('Write', { file_path: 'm1_product/change.md' }).allowed, true);
  assert.equal(policy.authorize('Write', { file_path: '../arena_new/change.md' }).allowed, false);
  assert.equal(policy.authorize('Bash', { command: 'echo no' }).allowed, false);
});

test('M1 policy denies traversal and symlink escape', () => {
  const root = mkdtempSync(path.join(os.tmpdir(), 'venue-m1-symlink-'));
  mkdirSync(path.join(root, 'm1_product'), { recursive: true });
  const policy = createM1ToolPolicy({ worktreeRoot: root, readPaths: ['PRODUCT_SPEC.md'], writePaths: ['m1_product'], forbiddenPaths: ['arena_new'] });
  assert.equal(policy.authorize('Write', { file_path: '../../outside.txt' }).allowed, false);
});

test('M1 task packet excludes private sources, reference SVGs and shell commands', () => {
  const task = JSON.parse(readFileSync(new URL('./tasks/m1-xlsx-productization-v1.json', import.meta.url), 'utf8'));
  assert.deepEqual(task.commands, []);
  assert.equal(task.inputs.some(item => /ChatExport|\.svg$|evaluator_only|console-confirmation|workspace-confirmation/i.test(item)), false);
  assert.equal(task.forbidden_paths.includes('arena_new'), true);
  assert.equal(task.max_retries, 0);
});

test('M1 v2 prompt carries paths, outputs and non-success review guard', () => {
  const task = JSON.parse(readFileSync(new URL('./tasks/m1-xlsx-productization-v2.json', import.meta.url), 'utf8'));
  const prompt = buildM1Prompt(task);
  assert.equal(task.task_id, 'm1-xlsx-productization-v2');
  assert.deepEqual({ budget: task.budget_usd, turns: task.max_turns, timeout: task.timeout_ms, retries: task.max_retries }, { budget: 10, turns: 6, timeout: 300000, retries: 0 });
  for (const item of [...task.inputs, ...task.read_paths, ...task.write_paths, ...task.expected_outputs]) assert.equal(prompt.includes(`- ${item}`), true, `missing prompt item: ${item}`);
  assert.match(prompt, /Report incomplete work explicitly/);
  assert.match(prompt, /never claim PASS without evidence/);
});

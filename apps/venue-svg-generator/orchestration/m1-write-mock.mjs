import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { copyFileSync, mkdirSync, readFileSync, rmSync, writeFileSync, existsSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { buildM1Prompt, createM1ToolPolicy, redactM1, validateM1Task } from './m1-write-policy.mjs';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const taskFile = process.env.M1_MOCK_TASK ?? 'orchestration/tasks/m1-xlsx-productization-v1.json';
const TASK = JSON.parse(readFileSync(path.join(ROOT, taskFile), 'utf8'));
const RUN = path.join(ROOT, 'orchestration/runs', `m1-write-mock-${TASK.task_id}`);
const WORKTREE = path.join(RUN, 'worktree');

function sha256(file) { return createHash('sha256').update(readFileSync(file)).digest('hex'); }
function writeJson(file, value) { mkdirSync(path.dirname(file), { recursive: true }); writeFileSync(file, JSON.stringify(value, null, 2) + '\n', { flag: 'wx' }); }
function check(command, args) {
  const result = spawnSync(command, args, { cwd: ROOT, encoding: 'utf8', timeout: 120000, windowsHide: true });
  const output = `${result.stdout ?? ''}${result.stderr ?? ''}`.trim();
  return { command: [command, ...args].join(' '), exit_code: result.status ?? 1, output_tail: output.slice(-800) };
}

function run() {
  rmSync(RUN, { recursive: true, force: true });
  mkdirSync(WORKTREE, { recursive: true });
  writeFileSync(path.join(WORKTREE, '.git'), 'gitdir: mock-worktree\n', { flag: 'wx' });
  for (const file of TASK.inputs) {
    if (!existsSync(path.join(ROOT, file))) continue;
    mkdirSync(path.dirname(path.join(WORKTREE, file)), { recursive: true });
    copyFileSync(path.join(ROOT, file), path.join(WORKTREE, file));
  }
  validateM1Task(TASK, WORKTREE);
  const prompt = buildM1Prompt(TASK);
  for (const item of [...TASK.inputs, ...TASK.read_paths, ...TASK.write_paths]) assert.equal(prompt.includes(`- ${item}`), true, `prompt missing approved path: ${item}`);
  for (const item of TASK.expected_outputs ?? []) assert.equal(prompt.includes(item), true, `prompt missing expected output: ${item}`);
  const policy = createM1ToolPolicy({ worktreeRoot: WORKTREE, readPaths: TASK.read_paths, writePaths: TASK.write_paths, forbiddenPaths: TASK.forbidden_paths });
  const read = policy.authorize('Read', { file_path: 'PRODUCT_SPEC.md' });
  assert.equal(read.allowed, true);
  const write = policy.authorize('Write', { file_path: 'm1_product/mock-change.md' });
  assert.equal(write.allowed, true);
  const outside = policy.authorize('Write', { file_path: path.join(ROOT, '..', 'arena_new', 'SHOULD_NOT_CHANGE.txt') });
  assert.equal(outside.allowed, false);
  const shell = policy.authorize('Bash', { command: 'echo forbidden' });
  assert.equal(shell.allowed, false);
  const changed = path.join(WORKTREE, 'm1_product/mock-change.md');
  mkdirSync(path.dirname(changed), { recursive: true });
  writeFileSync(changed, '# Mock Claude change\n\nNo API call; this file is a disposable write-flow fixture.\n', { flag: 'wx' });
  const diff = { files: ['m1_product/mock-change.md'], outside_scope: false, secrets_detected: false };
  const tests = [
    check(process.execPath, ['--test', 'orchestration/m1-write-mock.test.mjs']),
  ];
  assert.equal(tests.every(item => item.exit_code === 0), true);
  const deferred = ['Hunter/Aribau regression fixtures are intentionally unavailable in this disposable mock worktree and require a separate Codex review run.'];
  const result = { task_id: TASK.task_id, status: 'PASS', mode: 'mock-write', api_calls: 0, reported_api_cost_usd: 0, billing_status: 'NOT_APPLICABLE', usage: null, prompt_paths_verified: true, product_milestone: 'NOT_EVALUATED', deferred, diff, tests, policy_events: { read, write, denied_outside_write: outside, denied_shell: shell } };
  const review = { review_version: 1, status: 'PASS', checks: { write_inside_worktree: true, forbidden_write_denied: true, shell_denied: true, no_secrets: true, no_api_calls: true, no_production_scope: true, product_milestone_not_marked_pass: true }, deferred, notes: 'Policy/path mock passed; product acceptance remains for independent Codex review.' };
  writeJson(path.join(RUN, 'task.json'), TASK);
  writeJson(path.join(RUN, 'result.json'), JSON.parse(redactM1(result)));
  writeJson(path.join(RUN, 'review.json'), review);
  writeJson(path.join(RUN, 'checkpoint.json'), { task_id: TASK.task_id, status: 'reviewed', result: 'result.json', review: 'review.json', changed_files_sha256: { 'm1_product/mock-change.md': sha256(changed) }, api_calls: 0, note: 'Offline mock only; no SDK/API query.' });
  writeFileSync(path.join(RUN, 'changes.diff'), 'Mock write: m1_product/mock-change.md\n', { flag: 'wx' });
  assert.equal(existsSync(changed), true);
  console.log(JSON.stringify({ status: 'M1_WRITE_MOCK_PASSED', api_calls: 0, run: path.relative(ROOT, RUN), denied_outside_write: true, denied_shell: true }));
}

run();

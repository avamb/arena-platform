import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { existsSync, mkdirSync, readFileSync, writeFileSync, unlinkSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';
import { runCodexReview } from './codex-review.mjs';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const RUN_ROOT = path.join(ROOT, 'orchestration', 'runs', 'supervisor');
const CHECKPOINT_ROOT = path.join(ROOT, 'orchestration', 'checkpoints');
const DEFAULT_QUEUE = 'orchestration/supervisor-queue-v1.json';
const MAX_SESSION_BUDGET = 20;
const MAX_TASK_BUDGET = 5;
const MAX_FIX_ATTEMPTS = 2;
const FORBIDDEN_MARKERS = ['arena_new', 'production', 'staging', 'ssh', 'checkout', 'payment', 'reserve'];
const SECRET_PATTERNS = [/sk-ant-[A-Za-z0-9_-]+/gi, /ANTHROPIC_API_KEY\s*[=:]\s*[^\s]+/gi, /Bearer\s+[^\s]+/gi];

function projectPath(value) {
  const resolved = path.resolve(ROOT, value);
  const relative = path.relative(ROOT, resolved);
  if (relative.startsWith('..') || path.isAbsolute(relative)) throw new Error(`path outside project: ${value}`);
  return resolved;
}
function safeId(value) {
  assert.match(value, /^[A-Za-z0-9][A-Za-z0-9._-]{2,80}$/);
  return value;
}
function readJson(file) { return JSON.parse(readFileSync(projectPath(file), 'utf8')); }
function redact(value) {
  let text = typeof value === 'string' ? value : JSON.stringify(value);
  for (const pattern of SECRET_PATTERNS) text = text.replace(pattern, '[REDACTED]');
  return text.slice(0, 4000);
}
function sha256(file) { return createHash('sha256').update(readFileSync(file)).digest('hex'); }
function writeJson(file, value) { mkdirSync(path.dirname(file), { recursive: true }); writeFileSync(file, JSON.stringify(value, null, 2) + '\n', { flag: 'wx' }); }

function validateScopedPaths(task, key) {
  assert.ok(Array.isArray(task[key]), `${key} must be an array`);
  for (const value of task[key]) {
    assert.equal(typeof value, 'string', `${key} entries must be strings`);
    const resolved = projectPath(value);
    assert.ok(task.allowed_paths.some(prefix => {
      const base = projectPath(prefix);
      return resolved === base || resolved.startsWith(base + path.sep);
    }), `${key} path outside allowed_paths: ${value}`);
    assert.equal(task.forbidden_paths.some(prefix => {
      const base = projectPath(prefix);
      return resolved === base || resolved.startsWith(base + path.sep);
    }), false, `${key} path is forbidden: ${value}`);
  }
}

function validateTask(taskFile, mode) {
  const task = readJson(taskFile);
  for (const key of ['task_id', 'objective', 'inputs', 'allowed_paths', 'read_paths', 'write_paths', 'forbidden_paths', 'commands', 'acceptance', 'api_allowed', 'budget_usd', 'max_turns', 'max_retries']) assert.ok(Object.hasOwn(task, key), `missing task field: ${key}`);
  safeId(task.task_id);
  for (const key of ['inputs', 'allowed_paths', 'forbidden_paths', 'commands', 'acceptance']) assert.ok(Array.isArray(task[key]), `${key} must be an array`);
  assert.equal(typeof task.api_allowed, 'boolean');
  assert.ok(Number.isFinite(task.budget_usd) && task.budget_usd >= 0 && task.budget_usd <= MAX_TASK_BUDGET);
  assert.ok(Number.isInteger(task.max_retries) && task.max_retries === 0, 'automatic retries are forbidden');
  if (mode === 'mock') assert.equal(task.api_allowed, false, 'mock queue cannot contain API tasks');
  const serialized = JSON.stringify({ objective: task.objective, inputs: task.inputs, allowed_paths: task.allowed_paths, commands: task.commands }).toLowerCase();
  for (const marker of FORBIDDEN_MARKERS) assert.equal(serialized.includes(marker), false, `forbidden marker in task: ${marker}`);
  validateScopedPaths(task, 'read_paths');
  validateScopedPaths(task, 'write_paths');
  for (const input of task.inputs) {
    const p = projectPath(input);
    assert.ok(existsSync(p), `missing task input: ${input}`);
    const allowed = task.allowed_paths.some(prefix => {
      const base = projectPath(prefix);
      return p === base || p.startsWith(base + path.sep);
    });
    assert.ok(allowed, `input outside allowed_paths: ${input}`);
    assert.ok(!task.forbidden_paths.some(prefix => {
      const base = projectPath(prefix);
      return p === base || p.startsWith(base + path.sep);
    }), `input is forbidden: ${input}`);
  }
  return task;
}

function validateQueue(queueFile, mode = 'mock') {
  const queue = readJson(queueFile);
  safeId(queue.session_id);
  assert.equal(queue.schema_version, 1);
  assert.ok(Number.isFinite(queue.session_budget_usd) && queue.session_budget_usd >= 0 && queue.session_budget_usd <= MAX_SESSION_BUDGET);
  assert.ok(Number.isFinite(queue.max_task_budget_usd) && queue.max_task_budget_usd >= 0 && queue.max_task_budget_usd <= MAX_TASK_BUDGET);
  assert.ok(Number.isInteger(queue.max_fix_attempts) && queue.max_fix_attempts >= 0 && queue.max_fix_attempts <= MAX_FIX_ATTEMPTS);
  assert.ok(Array.isArray(queue.tasks) && queue.tasks.length > 0);
  const ids = new Set();
  let configured = 0;
  const tasks = queue.tasks.map(entry => {
    safeId(entry.task_id);
    assert.equal(ids.has(entry.task_id), false, `duplicate task id: ${entry.task_id}`);
    ids.add(entry.task_id);
    assert.ok(typeof entry.task_file === 'string');
    const task = validateTask(entry.task_file, mode);
    assert.equal(task.task_id, entry.task_id);
    assert.equal(entry.mode, mode, `task ${entry.task_id} mode mismatch`);
    assert.ok(task.budget_usd <= queue.max_task_budget_usd, `task ${entry.task_id} exceeds queue task budget`);
    if (mode === 'real') {
      assert.ok(['m1', 'generic'].includes(entry.launcher), `unsupported launcher for ${entry.task_id}`);
      assert.equal(typeof entry.worktree, 'string', `worktree is required for ${entry.task_id}`);
      assert.ok(['run', 'disabled'].includes(entry.codex_review), `invalid Codex review mode for ${entry.task_id}`);
    }
    configured += task.budget_usd;
    assert.ok(configured <= queue.session_budget_usd, 'configured task budgets exceed session budget');
    return { entry, task };
  });
  return { queue, tasks, configured_budget_usd: configured };
}

function consoleFresh() {
  const file = path.join(ROOT, 'sdk_experiments', 'console-confirmation.json');
  if (!existsSync(file)) return { status: 'BLOCKED', reason: 'console confirmation is missing' };
  const data = JSON.parse(readFileSync(file, 'utf8'));
  const required = ['promotional_credit_eligible', 'auto_reload_disabled', 'spending_limits_reviewed'];
  if (!required.every(key => data[key] === true)) return { status: 'BLOCKED', reason: 'console checks are not confirmed' };
  const checked = Date.parse(data.checked_at);
  if (!Number.isFinite(checked) || Date.now() - checked >= 24 * 3600000) return { status: 'BLOCKED', reason: 'console confirmation is older than 24 hours' };
  if (Number(data.promotional_balance_usd) < MAX_SESSION_BUDGET) return { status: 'BLOCKED', reason: 'confirmed promotional balance is below session budget' };
  return { status: 'PASS' };
}

function checkpoint(sessionDir, state) {
  mkdirSync(CHECKPOINT_ROOT, { recursive: true });
  // The run state is replaced only inside the managed run directory.
  const tmp = path.join(sessionDir, 'state.json.tmp');
  const final = path.join(sessionDir, 'state.json');
  writeFileSync(tmp, JSON.stringify(state, null, 2) + '\n', { flag: 'w' });
  writeFileSync(final, readFileSync(tmp), { flag: 'w' });
  unlinkSync(tmp);
  writeFileSync(path.join(CHECKPOINT_ROOT, `${state.session_id}.json`), JSON.stringify({ session_id: state.session_id, status: state.status, state: path.relative(ROOT, final), updated_at: new Date().toISOString() }, null, 2) + '\n', { flag: 'w' });
}

function mockTaskResult(task, sessionDir, index) {
  const taskDir = path.join(sessionDir, 'tasks', task.task_id);
  mkdirSync(taskDir, { recursive: true });
  const result = { task_id: task.task_id, status: 'PASS', mode: 'mock', api_calls: 0, reported_api_cost_usd: 0, changed_files: [], write_scope: path.relative(ROOT, taskDir), codex_review: { status: 'MOCK_PASS', invoked: false }, incomplete_components: [] };
  const review = { task_id: task.task_id, status: 'PASS', reviewer: 'offline-supervisor-mock', codex_cli_invoked: false, notes: 'Synthetic review only; no Claude or Codex API/CLI call.' };
  writeJson(path.join(taskDir, 'result.json'), result);
  writeJson(path.join(taskDir, 'review.json'), review);
  return { index, status: 'PASS', api_calls: 0, reported_api_cost_usd: 0, task_id: task.task_id, result: path.relative(ROOT, path.join(taskDir, 'result.json')) };
}

function assertCleanWorktree(worktree) {
  const resolved = path.resolve(worktree);
  const parent = path.dirname(ROOT);
  assert.ok(resolved.startsWith(parent + path.sep), 'worktree must stay under the project parent');
  assert.notEqual(path.basename(resolved).toLowerCase(), 'arena_new', 'arena_new is forbidden');
  assert.ok(existsSync(resolved), `worktree does not exist: ${resolved}`);
  const status = spawnSync('git', ['-C', resolved, 'status', '--porcelain'], { encoding: 'utf8', windowsHide: true });
  if (status.status !== 0) throw new Error(`cannot inspect worktree: ${resolved}`);
  if ((status.stdout ?? '').trim()) throw new Error(`worktree has uncommitted changes: ${resolved}`);
}

function realTaskResult(task, entry, sessionDir, approved) {
  if (!approved) throw new Error('real supervisor execution requires --approved');
  const ttl = consoleFresh();
  if (ttl.status !== 'PASS') throw new Error(ttl.reason);
  assertCleanWorktree(entry.worktree ?? ROOT);
  const launcher = entry.launcher === 'm1' ? path.join(ROOT, 'orchestration', 'run-m1-approved.ps1') : path.join(ROOT, 'orchestration', 'run-task-approved.ps1');
  const args = ['-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', launcher, '-Task', entry.task_file, '-Approved'];
  if (entry.launcher === 'm1') {
    assert.equal(typeof entry.worktree, 'string');
    args.push('-Worktree', entry.worktree);
    args.push('-RuntimeRoot', path.resolve(path.dirname(ROOT), 'Venue_SVG_Generator'));
  }
  const child = spawnSync('powershell.exe', args, { cwd: ROOT, encoding: 'utf8', windowsHide: true, timeout: task.timeout_ms + 10000 });
  const resultFile = path.join(ROOT, 'orchestration', 'runs', task.task_id, 'result.json');
  let reportedCost = null;
  let sdkResult = null;
  if (existsSync(resultFile)) { try { sdkResult = JSON.parse(readFileSync(resultFile, 'utf8')); reportedCost = Number.isFinite(sdkResult.reported_api_cost_usd) ? sdkResult.reported_api_cost_usd : null; } catch {} }
  const timedOut = child.error?.code === 'ETIMEDOUT';
  const status = timedOut ? 'FAIL' : (sdkResult?.status === 'PENDING_CODEX_REVIEW' ? 'PENDING_CODEX_REVIEW' : 'FAIL');
  const apiCalls = Number.isInteger(sdkResult?.sdk_actual_api_calls) ? sdkResult.sdk_actual_api_calls : (Number.isInteger(sdkResult?.api_calls) ? sdkResult.api_calls : 0);
  const safe = { task_id: task.task_id, status, api_calls: apiCalls, reported_api_cost_usd: reportedCost, sdk_result_present: Boolean(sdkResult), timed_out: timedOut, stdout: redact(child.stdout), stderr: redact(child.stderr), codex_review: { status: 'PENDING', invoked: false } };
  writeJson(path.join(sessionDir, 'tasks', task.task_id, 'supervisor-result.json'), safe);
  return safe;
}

function reviewPendingTask(task, entry, sessionDir, result) {
  if (entry.codex_review !== 'run') return result;
  const worktree = entry.worktree ?? ROOT;
  const prompt = [
    'Review the current uncommitted task implementation in this worktree.',
    `Task objective: ${task.objective}`,
    `Acceptance criteria: ${task.acceptance.join('; ')}`,
    'Run only safe read-only checks. Return only the required JSON review schema.',
  ].join('\n');
  const review = runCodexReview({ worktree, prompt, schemaPath: path.join(ROOT, 'orchestration', 'codex-review.schema.json'), timeoutMs: task.timeout_ms });
  writeJson(path.join(sessionDir, 'tasks', task.task_id, 'codex-review.json'), review);
  return { ...result, status: review.verdict === 'PASS' ? 'PASS' : 'CODEX_REVIEW_REJECTED', codex_review: review };
}

function budgetAllows(actual, next, limit) {
  return Number.isFinite(actual) && Number.isFinite(next) && Number.isFinite(limit) && actual + next <= limit;
}

function runQueue(queueFile, { mock = false, approved = false, resume = false, stopAfter = null, sessionId = null } = {}) {
  const checked = validateQueue(queueFile, mock ? 'mock' : 'real');
  const id = sessionId ?? checked.queue.session_id;
  safeId(id);
  const sessionDir = path.join(RUN_ROOT, id);
  mkdirSync(sessionDir, { recursive: true });
  const stateFile = path.join(sessionDir, 'state.json');
  let state;
  if (existsSync(stateFile)) {
    if (!resume) throw new Error(`supervisor session already exists; use --resume: ${id}`);
    state = JSON.parse(readFileSync(stateFile, 'utf8'));
  } else {
    state = { schema_version: 1, session_id: id, status: 'RUNNING', created_at: new Date().toISOString(), mode: mock ? 'mock' : 'approved-sdk', configured_budget_usd: checked.configured_budget_usd, actual_cost_usd: 0, tasks: checked.tasks.map(({ task }) => ({ task_id: task.task_id, status: 'PENDING', api_calls: 0 })) };
    checkpoint(sessionDir, state);
  }
  if (state.tasks.some(task => task.status === 'PENDING_CODEX_REVIEW')) {
    state.status = 'PENDING_CODEX_REVIEW';
    checkpoint(sessionDir, state);
    return { status: state.status, session_id: id, api_calls: state.tasks.reduce((sum, task) => sum + (Number.isFinite(task.api_calls) ? task.api_calls : 0), 0), actual_cost_usd: state.actual_cost_usd };
  }
  const done = state.tasks.filter(task => task.status === 'PASS').length;
  if (stopAfter !== null && done >= stopAfter && state.tasks.some(task => task.status === 'PENDING')) {
    state.status = 'INTERRUPTED';
    checkpoint(sessionDir, state);
    return { status: 'INTERRUPTED', session_id: id, api_calls: state.tasks.reduce((sum, task) => sum + task.api_calls, 0), actual_cost_usd: state.actual_cost_usd };
  }
  for (let i = 0; i < checked.tasks.length; i += 1) {
    const current = state.tasks[i];
    if (current.status !== 'PENDING') continue;
    const { entry, task } = checked.tasks[i];
    if (!budgetAllows(state.actual_cost_usd, task.budget_usd, checked.queue.session_budget_usd)) {
      state.status = 'BUDGET_STOP';
      checkpoint(sessionDir, state);
      return { status: state.status, session_id: id, api_calls: state.tasks.reduce((sum, item) => sum + item.api_calls, 0), actual_cost_usd: state.actual_cost_usd };
    }
    if (stopAfter !== null && i >= stopAfter) {
      state.status = 'INTERRUPTED';
      checkpoint(sessionDir, state);
      return { status: state.status, session_id: id, api_calls: state.tasks.reduce((sum, item) => sum + item.api_calls, 0), actual_cost_usd: state.actual_cost_usd };
    }
    let result = mock ? mockTaskResult(task, sessionDir, i) : realTaskResult(task, entry, sessionDir, approved);
    if (!mock && result.status === 'PENDING_CODEX_REVIEW') result = reviewPendingTask(task, entry, sessionDir, result);
    current.status = result.status;
    current.api_calls = result.api_calls ?? 0;
    current.result = result.result ?? null;
    current.codex_review = result.codex_review ?? { status: 'PENDING', invoked: false };
    const taskCost = Number.isFinite(result.reported_api_cost_usd) ? result.reported_api_cost_usd : null;
    if (taskCost !== null && (!budgetAllows(0, taskCost, task.budget_usd) || !budgetAllows(state.actual_cost_usd, taskCost, checked.queue.session_budget_usd))) {
      current.status = 'BUDGET_VIOLATION';
      state.status = 'BUDGET_STOP';
      checkpoint(sessionDir, state);
      return { status: state.status, session_id: id, api_calls: state.tasks.reduce((sum, item) => sum + (Number.isFinite(item.api_calls) ? item.api_calls : 0), 0), actual_cost_usd: state.actual_cost_usd };
    }
    state.actual_cost_usd += taskCost ?? 0;
    checkpoint(sessionDir, state);
    if (result.status === 'FAIL' || result.status === 'CODEX_REVIEW_REJECTED') { state.status = result.status === 'CODEX_REVIEW_REJECTED' ? 'STOPPED_ON_CODEX_REVIEW' : 'STOPPED_ON_ERROR'; checkpoint(sessionDir, state); return { status: state.status, session_id: id, api_calls: state.tasks.reduce((sum, item) => sum + (Number.isFinite(item.api_calls) ? item.api_calls : 0), 0), actual_cost_usd: state.actual_cost_usd }; }
    if (!mock && result.status === 'PENDING_CODEX_REVIEW' && !Number.isFinite(result.reported_api_cost_usd)) { current.status = 'UNKNOWN_BILLING'; state.status = 'STOPPED_UNKNOWN_BILLING'; checkpoint(sessionDir, state); return { status: state.status, session_id: id, api_calls: state.tasks.reduce((sum, item) => sum + item.api_calls, 0), actual_cost_usd: state.actual_cost_usd }; }
    if (!mock && result.status === 'PENDING_CODEX_REVIEW') { state.status = 'PENDING_CODEX_REVIEW'; checkpoint(sessionDir, state); return { status: state.status, session_id: id, api_calls: state.tasks.reduce((sum, item) => sum + (Number.isFinite(item.api_calls) ? item.api_calls : 0), 0), actual_cost_usd: state.actual_cost_usd }; }
  }
  state.status = state.tasks.every(task => task.status === 'PASS') ? 'COMPLETED' : 'INTERRUPTED';
  checkpoint(sessionDir, state);
  return { status: state.status, session_id: id, api_calls: state.tasks.reduce((sum, item) => sum + item.api_calls, 0), actual_cost_usd: state.actual_cost_usd };
}

function selfTest() {
  const sessionId = `supervisor-self-test-${Date.now()}`;
  assert.equal(budgetAllows(19, 2, 20), false);
  assert.equal(budgetAllows(18, 2, 20), true);
  const queueCheck = validateQueue(DEFAULT_QUEUE, 'mock');
  assert.ok(queueCheck.tasks.every(({ task }) => task.max_retries === 0));
  const interrupted = runQueue(DEFAULT_QUEUE, { mock: true, sessionId, stopAfter: 1 });
  assert.equal(interrupted.status, 'INTERRUPTED');
  const completed = runQueue(DEFAULT_QUEUE, { mock: true, sessionId, resume: true });
  assert.equal(completed.status, 'COMPLETED');
  assert.equal(completed.api_calls, 0);
  assert.equal(completed.actual_cost_usd, 0);
  assert.ok(existsSync(path.join(RUN_ROOT, sessionId, 'state.json')));
  console.log(JSON.stringify({ status: 'SUPERVISOR_SELF_TEST_PASSED', session_id: sessionId, api_calls: 0, budget_stop_guard: true, retry_stop_guard: true, interruption_recovery: true, isolated_write_scope: true, codex_review: 'not_invoked_in_mock' }));
}

try {
  const args = new Set(process.argv.slice(2));
  if (args.has('--self-test')) selfTest();
  else {
    const queueIndex = process.argv.indexOf('--queue');
    const queueFile = queueIndex >= 0 ? process.argv[queueIndex + 1] : DEFAULT_QUEUE;
    if (args.has('--preflight')) {
      const checked = validateQueue(queueFile, args.has('--mock') ? 'mock' : 'real');
      console.log(JSON.stringify({ status: 'SUPERVISOR_PREFLIGHT_PASSED', api_calls: 0, session_id: checked.queue.session_id, configured_budget_usd: checked.configured_budget_usd, task_count: checked.tasks.length, mode: args.has('--mock') ? 'mock' : 'approved-sdk' }));
    } else if (args.has('--run')) {
      const stopIndex = process.argv.indexOf('--stop-after');
      const stopAfter = stopIndex >= 0 ? Number(process.argv[stopIndex + 1]) : null;
      const result = runQueue(queueFile, { mock: args.has('--mock'), approved: args.has('--approved'), resume: args.has('--resume'), stopAfter: Number.isInteger(stopAfter) ? stopAfter : null });
      console.log(JSON.stringify(result));
      process.exitCode = result.status === 'COMPLETED' ? 0 : 1;
    } else throw new Error('Use --self-test, --preflight --queue <file>, or --run --queue <file> [--mock|--approved]');
  }
} catch (error) {
  console.error(JSON.stringify({ status: 'SUPERVISOR_BLOCKED', api_calls: 0, error: redact(error?.message ?? error) }));
  process.exitCode = 1;
}

import assert from 'node:assert/strict';
import { existsSync, readFileSync, mkdirSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const TASK_DIR = path.join(ROOT, 'orchestration', 'tasks');
const CHECKPOINT_DIR = path.join(ROOT, 'orchestration', 'checkpoints');
const SECRET_PATTERNS = [/sk-ant-[A-Za-z0-9_-]+/i, /ANTHROPIC_API_KEY\s*=/i, /CLAUDE_CODE_OAUTH_TOKEN\s*=/i];

function repoPath(value) {
  const resolved = path.resolve(ROOT, value);
  const rel = path.relative(ROOT, resolved);
  if (rel.startsWith('..') || path.isAbsolute(rel)) throw new Error(`Path outside project: ${value}`);
  return resolved;
}

function validateTask(task) {
  assert.equal(typeof task, 'object');
  for (const key of ['task_id', 'objective', 'inputs', 'allowed_paths', 'forbidden_paths', 'commands', 'acceptance', 'status']) {
    assert.ok(Object.hasOwn(task, key), `Missing task field: ${key}`);
  }
  assert.match(task.task_id, /^[A-Za-z0-9][A-Za-z0-9._-]{2,80}$/);
  for (const key of ['inputs', 'allowed_paths', 'forbidden_paths', 'commands', 'acceptance']) assert.ok(Array.isArray(task[key]), `${key} must be an array`);
  assert.equal(typeof task.api_allowed, 'boolean');
  assert.ok(Number.isFinite(task.budget_usd) && task.budget_usd >= 0);
  assert.ok(Number.isInteger(task.max_turns) && task.max_turns >= 0);
  assert.ok(Number.isInteger(task.max_retries) && task.max_retries >= 0);
  if (!task.api_allowed) {
    assert.equal(task.budget_usd, 0, 'non-API task must have zero budget');
    assert.equal(task.max_retries, 0, 'non-API task cannot retry');
  }
  for (const input of task.inputs) {
    assert.equal(typeof input, 'string');
    const p = repoPath(input);
    assert.ok(existsSync(p), `Input does not exist: ${input}`);
    const allowed = task.allowed_paths.some(prefix => {
      const a = repoPath(prefix);
      return p === a || p.startsWith(a + path.sep);
    });
    assert.ok(allowed, `Input is outside allowed_paths: ${input}`);
    assert.ok(!task.forbidden_paths.some(prefix => {
      const f = repoPath(prefix);
      return p === f || p.startsWith(f + path.sep);
    }), `Input is forbidden: ${input}`);
  }
  return task;
}

function readTask(file) {
  const absolute = repoPath(file);
  const task = JSON.parse(readFileSync(absolute, 'utf8'));
  return validateTask(task);
}

function selfTest() {
  const files = ['PRODUCT_SPEC.md', 'ROADMAP.md', 'AGENT_WORKFLOW.md', 'BUDGET_POLICY.md', 'orchestration/tasks/example-task.json'];
  for (const file of files) assert.ok(existsSync(repoPath(file)), `Missing ${file}`);
  const task = readTask('orchestration/tasks/example-task.json');
  assert.equal(task.api_allowed, false);
  assert.equal(task.budget_usd, 0);
  assert.equal(task.max_retries, 0);
  for (const file of files) {
    const text = readFileSync(repoPath(file), 'utf8');
    for (const pattern of SECRET_PATTERNS) assert.equal(pattern.test(text), false, `Secret-like value in ${file}`);
  }
  // This validator intentionally does not import the SDK or call query().
  console.log(JSON.stringify({status: 'LOCAL_WORKFLOW_CHECKS_PASSED', api_calls: 0, task_id: task.task_id, budget_usd: 0}));
}

function checkpoint(file) {
  const task = readTask(file);
  mkdirSync(CHECKPOINT_DIR, { recursive: true });
  const output = path.join(CHECKPOINT_DIR, `${task.task_id}.json`);
  const payload = {
    task_id: task.task_id,
    status: 'prepared',
    created_at: new Date().toISOString(),
    api_allowed: task.api_allowed,
    budget_usd: task.budget_usd,
    note: 'Preparation checkpoint only; no SDK/API call was made.'
  };
  writeFileSync(output, JSON.stringify(payload, null, 2) + '\n', { flag: 'wx' });
  console.log(JSON.stringify({status: 'CHECKPOINT_CREATED', path: path.relative(ROOT, output)}));
}

try {
  if (process.argv.includes('--self-test')) selfTest();
  else if (process.argv[2] === '--validate-task' && process.argv[3]) {
    const task = readTask(process.argv[3]);
    console.log(JSON.stringify({status: 'TASK_VALID', task_id: task.task_id, api_allowed: task.api_allowed, budget_usd: task.budget_usd}));
  } else if (process.argv[2] === '--checkpoint' && process.argv[3]) checkpoint(process.argv[3]);
  else throw new Error('Use --self-test, --validate-task <task.json> or --checkpoint <task.json>');
} catch (error) {
  console.error(JSON.stringify({status: 'LOCAL_WORKFLOW_CHECK_FAILED', error: String(error?.message ?? error)}));
  process.exitCode = 1;
}

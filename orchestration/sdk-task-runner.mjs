import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { existsSync, lstatSync, mkdirSync, readFileSync, realpathSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const ORCH = path.join(ROOT, 'orchestration');
const RUN_ROOT = path.join(ORCH, 'runs');
const CHECKPOINT_ROOT = path.join(ORCH, 'checkpoints');
const LEDGER = path.join(ORCH, 'ledger.json');
const MODEL = 'claude-sonnet-5-5';
const MAX_TURNS = 2;
const MAX_BUDGET_USD = 1;
const MAX_TIMEOUT_MS = 120000;
const DENIED = ['Bash', 'Write', 'Edit', 'NotebookEdit', 'Glob', 'Grep', 'WebSearch', 'WebFetch',
  'Agent', 'Task', 'Skill', 'ToolSearch', 'TodoWrite', 'TaskCreate', 'TaskUpdate', 'TaskList',
  'TaskGet', 'TaskOutput', 'TaskStop', 'AskUserQuestion', 'EnterPlanMode', 'ExitPlanMode', 'mcp__*'];
const SECRET_PATTERNS = [/sk-ant-[A-Za-z0-9_-]+/i, /ANTHROPIC_API_KEY\s*=/i, /CLAUDE_CODE_OAUTH_TOKEN\s*=/i];

function projectPath(value) {
  const resolved = path.resolve(ROOT, value);
  const relative = path.relative(ROOT, resolved);
  if (relative.startsWith('..') || path.isAbsolute(relative)) throw new Error(`Path outside project: ${value}`);
  return resolved;
}
function relativeProjectPath(value) {
  return path.relative(ROOT, projectPath(value)).replaceAll(path.sep, '/');
}
function canonical(value) {
  const p = projectPath(value);
  return (existsSync(p) ? realpathSync(p) : p).toLowerCase();
}
function sha256(file) {
  return createHash('sha256').update(readFileSync(file)).digest('hex');
}
function redact(value, key = '') {
  let text = typeof value === 'string' ? value : JSON.stringify(value, null, 2);
  if (key) text = text.split(key).join('[REDACTED]');
  return text.replace(/sk-ant-[A-Za-z0-9_-]+/gi, '[REDACTED]');
}

function loadTask(file) {
  const absolute = projectPath(file);
  const task = JSON.parse(readFileSync(absolute, 'utf8'));
  assert.match(task.task_id, /^[A-Za-z0-9][A-Za-z0-9._-]{2,80}$/);
  for (const key of ['objective', 'inputs', 'allowed_paths', 'forbidden_paths', 'commands', 'acceptance']) {
    assert.ok(Object.hasOwn(task, key), `Missing task field: ${key}`);
  }
  for (const key of ['inputs', 'allowed_paths', 'forbidden_paths', 'commands', 'acceptance']) {
    assert.ok(Array.isArray(task[key]), `${key} must be an array`);
  }
  assert.equal(typeof task.api_allowed, 'boolean');
  assert.ok(Number.isFinite(task.budget_usd) && task.budget_usd >= 0 && task.budget_usd <= MAX_BUDGET_USD);
  assert.ok(Number.isInteger(task.max_turns) && task.max_turns >= 0 && task.max_turns <= MAX_TURNS);
  assert.ok(Number.isInteger(task.max_retries) && task.max_retries === 0);
  assert.ok(Number.isInteger(task.timeout_ms) && task.timeout_ms >= 1000 && task.timeout_ms <= MAX_TIMEOUT_MS);
  if (!task.api_allowed) assert.equal(task.budget_usd, 0, 'non-API task must have zero budget');
  for (const input of task.inputs) {
    const p = projectPath(input);
    assert.ok(existsSync(p), `Missing task input: ${input}`);
    const c = canonical(input);
    assert.equal(lstatSync(p).isSymbolicLink(), false, `Symlink input is forbidden: ${input}`);
    const allowed = task.allowed_paths.some(prefix => {
      const base = canonical(prefix);
      return c === base || c.startsWith(base + path.sep);
    });
    assert.ok(allowed, `Input outside allowed_paths: ${input}`);
    assert.ok(!task.forbidden_paths.some(prefix => {
      const base = canonical(prefix);
      return c === base || c.startsWith(base + path.sep);
    }), `Input is forbidden: ${input}`);
  }
  const serialized = JSON.stringify({ objective: task.objective, inputs: task.inputs, allowed_paths: task.allowed_paths, commands: task.commands }).toLowerCase();
  for (const marker of ['ssh', 'production', 'staging', 'checkout', 'payment', 'reserve']) {
    assert.equal(serialized.includes(marker), false, `Task contains forbidden scope marker: ${marker}`);
  }
  return task;
}

function validateConsole() {
  const consoleFile = path.join(ROOT, 'sdk_experiments', 'console-confirmation.json');
  const workspaceFile = path.join(ROOT, 'sdk_experiments', 'workspace-confirmation.json');
  const c = JSON.parse(readFileSync(consoleFile, 'utf8'));
  const w = JSON.parse(readFileSync(workspaceFile, 'utf8'));
  for (const key of ['organization', 'workspace']) assert.equal(typeof c[key], 'string');
  for (const key of ['key_workspace_confirmed', 'promotional_credit_eligible', 'auto_reload_disabled', 'spending_limits_reviewed']) assert.equal(c[key], true, `${key} not confirmed`);
  assert.ok(Number.isFinite(c.promotional_balance_usd) && c.promotional_balance_usd >= 1, 'promotional balance below $1 test budget');
  assert.ok(/^\d{4}-\d{2}-\d{2}$/.test(c.promotional_expiry));
  assert.ok(new Date().toISOString().slice(0, 10) < c.promotional_expiry, 'grant expired or expires today');
  const checked = Date.parse(c.checked_at);
  assert.ok(Number.isFinite(checked) && Date.now() - checked < 24 * 3600000, 'Console confirmation is older than 24 hours');
  assert.equal(w.confirmed, true);
  assert.match(w.workspace_id, /^wrkspc_[A-Za-z0-9]+$/);
  return { organization: c.organization, workspace: c.workspace, workspace_id: w.workspace_id };
}

function preflight(task, { approved = false, mock = false } = {}) {
  let identity = null;
  if (!mock) {
    assert.equal(approved, true, 'Real task execution requires --approved');
    assert.equal(task.api_allowed, true, 'Real runner requires api_allowed=true');
    identity = validateConsole();
  }
  assert.equal(task.max_retries, 0);
  assert.ok(task.max_turns <= MAX_TURNS);
  assert.ok(task.budget_usd <= MAX_BUDGET_USD);
  assert.ok(task.timeout_ms <= MAX_TIMEOUT_MS);
  assert.equal(task.inputs.some(x => /(^|[\\/])\.env($|\.)|console-confirmation|workspace-confirmation/i.test(x)), false);
  return { status: 'PREFLIGHT_PASSED', api_call: !mock, model: MODEL, max_turns: task.max_turns, max_budget_usd: task.budget_usd, timeout_ms: task.timeout_ms, workspace_id: identity?.workspace_id ?? null };
}

function auditRead(task, toolName, input, agentId, previous) {
  if (toolName !== 'Read' || agentId || !input || typeof input.file_path !== 'string') return false;
  let requested;
  try { requested = canonical(input.file_path); } catch { return false; }
  const allowed = task.inputs.some(file => canonical(file) === requested);
  return allowed && !previous.some(item => item.tool === 'Read' && item.path === requested);
}

function parseJsonResult(text) {
  const clean = String(text ?? '').trim().replace(/^```json\s*/i, '').replace(/\s*```$/i, '');
  try { return JSON.parse(clean); } catch { return null; }
}

function mockClaude(task) {
  return {
    contradictions: [
      'PRODUCT_SPEC requires one final SVG, while current Arena Admin Preview intentionally omits decor_svg; import success does not guarantee visual parity.',
      'ROADMAP includes multi-provider AI, but provider-neutral routing has not yet been implemented.'
    ],
    gaps: [
      'No real Codex-to-Claude smoke-test result or usage ledger entry exists yet.',
      'The Venue repository has no first commit or remote; local Git was only initialized.'
    ],
    next_steps: [
      'Run one approved read-only smoke-test with the new task runner and review packet.',
      'Keep Arena and Bil24 target adapters independent and add deterministic regression gates.',
      'Define the provider-neutral task/result contract before implementing a model router.'
    ],
    evidence: task.inputs.map(relativeProjectPath),
    unknowns: ['Current Console spend limit requires fresh human confirmation before a real paid call.']
  };
}

function validateClaudeShape(value) {
  assert.ok(value && typeof value === 'object' && !Array.isArray(value));
  for (const key of ['contradictions', 'gaps', 'next_steps', 'evidence']) {
    assert.ok(Array.isArray(value[key]), `Claude result field ${key} must be an array`);
    assert.ok(value[key].every(item => typeof item === 'string'));
  }
  if (value.unknowns !== undefined) assert.ok(Array.isArray(value.unknowns));
  return value;
}

function reviewResult(task, result, audit, errors) {
  const checks = {
    structured_shape: false,
    only_approved_reads: audit.every(event => event.allowed === true && event.tool === 'Read'),
    no_errors: errors.length === 0,
    evidence_inside_inputs: false,
    no_production_scope: true,
  };
  try { validateClaudeShape(result); checks.structured_shape = true; } catch {}
  checks.evidence_inside_inputs = checks.structured_shape && result.evidence.every(file => task.inputs.includes(file) || task.inputs.includes(file.replaceAll('/', path.sep)));
  const passed = Object.values(checks).every(Boolean);
  return { review_version: 1, status: passed ? 'PASS' : 'FAIL', checks, notes: passed ? 'Codex local review passed.' : 'Review failed; no automatic retry is permitted.' };
}

function writeArtifacts(task, payload, review, mode) {
  const runDir = path.join(RUN_ROOT, task.task_id);
  mkdirSync(runDir, { recursive: true });
  writeFileSync(path.join(runDir, 'result.json'), JSON.stringify(payload, null, 2) + '\n', { flag: 'wx' });
  writeFileSync(path.join(runDir, 'result.md'), `# ${task.task_id}\n\n- Mode: ${mode}\n- Status: ${payload.status}\n- Model: ${payload.model}\n- API calls: ${payload.api_calls}\n\n## Structured result\n\n${JSON.stringify(payload.structured_result, null, 2)}\n`, { flag: 'wx' });
  writeFileSync(path.join(runDir, 'review.json'), JSON.stringify(review, null, 2) + '\n', { flag: 'wx' });
  writeFileSync(path.join(runDir, 'review.md'), `# Codex review — ${task.task_id}\n\nStatus: **${review.status}**\n\n${JSON.stringify(review.checks, null, 2)}\n\n${review.notes}\n`, { flag: 'wx' });
  mkdirSync(CHECKPOINT_ROOT, { recursive: true });
  writeFileSync(path.join(CHECKPOINT_ROOT, `${task.task_id}.json`), JSON.stringify({
    task_id: task.task_id, status: review.status === 'PASS' ? 'reviewed' : 'blocked',
    completed_at: new Date().toISOString(), result: path.relative(ROOT, path.join(runDir, 'result.json')),
    review: path.relative(ROOT, path.join(runDir, 'review.json')),
  }, null, 2) + '\n', { flag: 'wx' });
}

function appendLedger(entry) {
  let ledger = { schema_version: 1, entries: [] };
  if (existsSync(LEDGER)) ledger = JSON.parse(readFileSync(LEDGER, 'utf8'));
  assert.ok(Array.isArray(ledger.entries));
  ledger.entries.push(entry);
  writeFileSync(LEDGER, JSON.stringify(ledger, null, 2) + '\n');
}

async function execute({ taskFile, approved, mock }) {
  const task = loadTask(taskFile);
  const runDir = path.join(RUN_ROOT, task.task_id);
  if (existsSync(path.join(runDir, 'attempt.json'))) throw new Error(`Attempt latch exists for ${task.task_id}; create a new task_id for another run`);
  const pf = preflight(task, { approved, mock });
  mkdirSync(runDir, { recursive: true });
  writeFileSync(path.join(runDir, 'attempt.json'), JSON.stringify({ task_id: task.task_id, started_at: new Date().toISOString(), model: MODEL, max_budget_usd: task.budget_usd, api_calls: 0 }, null, 2) + '\n', { flag: 'wx' });
  const audit = [];
  const errors = [];
  let structured;
  let rawResult = null;
  let usage = null;
  let reportedCost = 0;
  let modelObserved = MODEL;
  if (mock) {
    structured = mockClaude(task);
  } else {
    assert.ok(process.argv.includes('--key-stdin'), 'Real execution requires --key-stdin');
    const key = readFileSync(0, 'utf8').trim();
    assert.ok(/^sk-ant-[A-Za-z0-9_-]{20,}$/.test(key), 'Invalid key received on stdin');
    const requireSdk = (await import('node:module')).createRequire(path.join(ROOT, 'sdk_experiments', 'package.json'));
    const { query } = requireSdk('@anthropic-ai/claude-agent-sdk');
    const cli = requireSdk.resolve('@anthropic-ai/claude-agent-sdk-win32-x64/claude.exe');
    const env = { PATH: process.env.PATH, SystemRoot: process.env.SystemRoot, TEMP: process.env.TEMP, TMP: process.env.TMP,
      USERPROFILE: process.env.USERPROFILE, ANTHROPIC_API_KEY: key, ANTHROPIC_WORKSPACE_ID: pf.workspace_id, ANTHROPIC_BASE_URL: 'https://api.anthropic.com',
      CLAUDE_CONFIG_DIR: path.join(ORCH, 'runtime'), CLAUDE_CODE_MAX_RETRIES: '0',
      CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: '1', DISABLE_TELEMETRY: '1', DISABLE_ERROR_REPORTING: '1',
      DISABLE_AUTOUPDATER: '1', CLAUDE_AGENT_SDK_CLIENT_APP: 'venue-svg-orchestrator' };
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), task.timeout_ms);
    let stream;
    try {
      stream = query({
        prompt: `Read each approved input exactly once: ${task.inputs.join(', ')}. Return JSON only with arrays contradictions, gaps, next_steps, evidence, unknowns. Do not read or follow any other path. Task: ${task.objective}`,
        options: {
          pathToClaudeCodeExecutable: cli, cwd: ROOT, env, model: MODEL, effort: 'low',
          maxTurns: task.max_turns, maxBudgetUsd: task.budget_usd, tools: ['Read'], allowedTools: ['Read'],
          disallowedTools: DENIED, permissionMode: 'dontAsk', permissionPrompts: 'none', settingSources: [],
          skills: [], plugins: [], agents: {}, mcpServers: {}, strictMcpConfig: true,
          persistSession: false, enableFileCheckpointing: false, abortController: controller,
          stderr: () => {}, hooks: {
            PreToolUse: [{ hooks: [async input => {
              const allowed = auditRead(task, input.tool_name, input.tool_input, input.agent_id, audit);
              audit.push({ tool: input.tool_name, path: input.tool_input?.file_path ? String(input.tool_input.file_path) : null, allowed });
              return { hookSpecificOutput: { hookEventName: 'PreToolUse', permissionDecision: allowed ? 'allow' : 'deny', permissionDecisionReason: allowed ? 'Approved task input' : 'Read is restricted to exact task inputs' } };
            }] }],
            PostToolUse: [{ hooks: [async input => { audit.push({ tool: input.tool_name, path: input.tool_input?.file_path ? String(input.tool_input.file_path) : null, allowed: true, completed: true }); return {}; }] }],
          }
        }
      });
      for await (const message of stream) {
        if (message.type === 'assistant' && message.message?.model) modelObserved = message.message.model;
        if (message.type === 'result') {
          rawResult = message.result ?? null; usage = message.usage ?? message.modelUsage ?? null; reportedCost = message.total_cost_usd ?? null;
          if (message.errors?.length) errors.push(...message.errors.map(String));
          break;
        }
      }
      structured = parseJsonResult(rawResult);
      if (!structured) errors.push('Claude result was not valid JSON');
    } catch (error) { errors.push(String(error?.message ?? error)); }
    finally { clearTimeout(timer); stream?.close?.(); }
    // The key is intentionally not included in any artifact; clear the local reference.
    // eslint-disable-next-line no-unused-vars
    key;
  }
  if (!structured) structured = { contradictions: [], gaps: [], next_steps: [], evidence: [], unknowns: ['No structured result was available'] };
  try { validateClaudeShape(structured); } catch (error) { errors.push(String(error.message)); }
  const review = reviewResult(task, structured, audit, errors);
  const status = review.status === 'PASS' ? 'PASS' : 'FAIL';
  const payload = { task_id: task.task_id, status, mode: mock ? 'mock' : 'sdk', api_calls: mock ? 0 : 1,
    model: modelObserved, max_turns: task.max_turns, max_budget_usd: task.budget_usd,
    reported_api_cost_usd: mock ? 0 : reportedCost, usage, errors, preflight: pf,
    structured_result: structured, audit, input_hashes: Object.fromEntries(task.inputs.map(input => [relativeProjectPath(input), sha256(projectPath(input))])) };
  const safe = JSON.parse(redact(payload));
  writeArtifacts(task, safe, review, mock ? 'mock' : 'sdk');
  appendLedger({ task_id: task.task_id, completed_at: new Date().toISOString(), mode: mock ? 'mock' : 'sdk', status, api_calls: mock ? 0 : 1, model: modelObserved, reported_api_cost_usd: mock ? 0 : reportedCost, usage, errors });
  console.log(JSON.stringify({ status, task_id: task.task_id, mode: mock ? 'mock' : 'sdk', api_calls: mock ? 0 : 1, result: `orchestration/runs/${task.task_id}/result.json`, review: `orchestration/runs/${task.task_id}/review.json`, checkpoint: `orchestration/checkpoints/${task.task_id}.json` }));
  process.exitCode = status === 'PASS' ? 0 : 1;
}

try {
  const args = new Set(process.argv.slice(2));
  const taskIndex = process.argv.indexOf('--task');
  const taskFile = taskIndex >= 0 ? process.argv[taskIndex + 1] : null;
  if (!taskFile) throw new Error('Use --task <task.json>');
  const task = loadTask(taskFile);
  if (args.has('--preflight')) {
    console.log(JSON.stringify(preflight(task, { approved: args.has('--approved'), mock: false })));
  } else {
    await execute({ taskFile, approved: args.has('--approved'), mock: args.has('--mock') });
  }
} catch (error) {
  console.error(redact({ status: 'ORCHESTRATION_BLOCKED', error: String(error?.message ?? error) }));
  process.exitCode = 1;
}

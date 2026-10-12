import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';
import { spawnSync } from 'node:child_process';
import { createM1ToolPolicy, M1_ALLOWED_TOOLS, M1_FORBIDDEN_TOOL_NAMES, redactM1, validateM1Task } from './m1-write-policy.mjs';
import { appendSafeDiagnostic, billingRecord, captureAssistantDiagnostics, captureTerminalDiagnostic, classifyTerminal } from './sdk-telemetry.mjs';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const ORCH = path.join(ROOT, 'orchestration');
const RUN_ROOT = path.join(ORCH, 'runs');
const LEDGER = path.join(ORCH, 'ledger.json');
const MODEL = 'claude-sonnet-5-5';
const MAX_BUDGET_USD = 10;
const MAX_TURNS = 8;
const MAX_TIMEOUT_MS = 300000;

function projectPath(file) {
  const resolved = path.resolve(ROOT, file);
  const rel = path.relative(ROOT, resolved);
  if (rel.startsWith('..') || path.isAbsolute(rel)) throw new Error(`path outside project: ${file}`);
  return resolved;
}
function sha256(file) { return createHash('sha256').update(readFileSync(file)).digest('hex'); }
function loadTask(file) { return JSON.parse(readFileSync(projectPath(file), 'utf8')); }
function parseJson(text) {
  try { return JSON.parse(String(text ?? '').trim().replace(/^```json\s*/i, '').replace(/\s*```$/i, '')); } catch { return null; }
}
function validateConsole() {
  const consoleData = JSON.parse(readFileSync(path.join(ROOT, 'sdk_experiments/console-confirmation.json'), 'utf8'));
  const workspace = JSON.parse(readFileSync(path.join(ROOT, 'sdk_experiments/workspace-confirmation.json'), 'utf8'));
  for (const key of ['key_workspace_confirmed', 'promotional_credit_eligible', 'auto_reload_disabled', 'spending_limits_reviewed']) assert.equal(consoleData[key], true, `${key} is not confirmed`);
  assert.ok(Number(consoleData.promotional_balance_usd) >= MAX_BUDGET_USD, 'promotional balance is below M1 budget');
  assert.ok(new Date().toISOString().slice(0, 10) < consoleData.promotional_expiry, 'promotional grant is expired');
  assert.ok(Date.now() - Date.parse(consoleData.checked_at) < 24 * 3600000, 'Console confirmation is older than 24 hours');
  assert.equal(workspace.confirmed, true);
  assert.match(workspace.workspace_id, /^wrkspc_[A-Za-z0-9]+$/);
  return { workspace_id: workspace.workspace_id };
}
function preflight(task, worktree, approved, { mock = false } = {}) {
  if (!existsSync(worktree)) throw new Error('dedicated worktree does not exist');
  if (!existsSync(path.join(worktree, '.git'))) throw new Error('worktree must be a dedicated Git worktree');
  if (!mock && path.resolve(worktree).toLowerCase() === ROOT.toLowerCase()) throw new Error('main checkout cannot be used as the M1 worktree');
  validateM1Task(task, worktree);
  for (const file of task.inputs) if (!existsSync(path.resolve(worktree, file))) throw new Error(`worktree input missing: ${file}`);
  if (approved) return validateConsole();
  return { workspace_id: null };
}
function writeArtifact(runDir, name, value) { writeFileSync(path.join(runDir, name), JSON.stringify(value, null, 2) + '\n', { flag: 'wx' }); }
function appendLedger(entry) {
  const ledger = existsSync(LEDGER) ? JSON.parse(readFileSync(LEDGER, 'utf8')) : { schema_version: 1, entries: [] };
  assert.ok(Array.isArray(ledger.entries));
  ledger.entries.push(entry);
  writeFileSync(LEDGER, JSON.stringify(ledger, null, 2) + '\n');
}
function collectGitDiff(worktree) {
  const status = spawnSync('git', ['-C', worktree, 'status', '--short'], { encoding: 'utf8', windowsHide: true });
  const diff = spawnSync('git', ['-C', worktree, 'diff', '--binary', '--no-ext-diff'], { encoding: 'utf8', windowsHide: true });
  return { status: redactM1((status.stdout ?? '').slice(0, 12000)), patch: redactM1((diff.stdout ?? '').slice(0, 100000)), status_exit_code: status.status ?? 1, diff_exit_code: diff.status ?? 1 };
}

async function execute({ taskFile, worktree, approved, keyStdin }) {
  const task = loadTask(taskFile);
  const runDir = path.join(RUN_ROOT, task.task_id);
  if (existsSync(path.join(runDir, 'attempt.json'))) throw new Error(`attempt latch exists for ${task.task_id}`);
  const identity = preflight(task, worktree, approved);
  if (!approved || !keyStdin) throw new Error('real M1 execution requires --approved and --key-stdin');
  const key = readFileSync(0, 'utf8').trim();
  assert.match(key, /^sk-ant-[A-Za-z0-9_-]{20,}$/);
  mkdirSync(runDir, { recursive: true });
  writeArtifact(runDir, 'attempt.json', { task_id: task.task_id, started_at: new Date().toISOString(), api_calls: 0, max_budget_usd: task.budget_usd });
  const policy = createM1ToolPolicy({ worktreeRoot: worktree, readPaths: task.read_paths, writePaths: task.write_paths, forbiddenPaths: task.forbidden_paths });
  const audit = [], errors = [], diagnosticStderr = [], assistantErrors = [];
  let terminal = null, rawResult = null, usage = null, reportedCost = null, timedOut = false;
  const workspaceId = identity.workspace_id;
  const requireSdk = createRequire(path.join(ROOT, 'sdk_experiments/package.json'));
  const { query } = requireSdk('@anthropic-ai/claude-agent-sdk');
  const cli = requireSdk.resolve('@anthropic-ai/claude-agent-sdk-win32-x64/claude.exe');
  const env = {
    PATH: process.env.PATH, SystemRoot: process.env.SystemRoot, TEMP: process.env.TEMP, TMP: process.env.TMP,
    USERPROFILE: process.env.USERPROFILE, ANTHROPIC_API_KEY: key, ANTHROPIC_WORKSPACE_ID: workspaceId,
    ANTHROPIC_CUSTOM_HEADERS: `anthropic-workspace-id: ${workspaceId}`, ANTHROPIC_BASE_URL: 'https://api.anthropic.com',
    CLAUDE_CONFIG_DIR: path.join(ORCH, 'runtime', 'm1', task.task_id), CLAUDE_CODE_MAX_RETRIES: '0',
    CLAUDE_CODE_STARTUP_FAILURE_RESULTS: '1', CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: '1', DISABLE_TELEMETRY: '1',
    DISABLE_ERROR_REPORTING: '1', DISABLE_AUTOUPDATER: '1', CLAUDE_AGENT_SDK_CLIENT_APP: 'venue-svg-orchestrator-m1',
  };
  const controller = new AbortController();
  const timer = setTimeout(() => { timedOut = true; controller.abort(); }, task.timeout_ms);
  let stream;
  try {
    stream = query({
      prompt: `${task.objective}\nRead only the approved input files. You may write/edit only paths under write_paths. Return JSON with summary, changed_files, tests, risks. Do not use or mention reference SVG files.`,
      options: {
        pathToClaudeCodeExecutable: cli, cwd: worktree, env, model: MODEL, effort: 'low', maxTurns: task.max_turns,
        maxBudgetUsd: task.budget_usd, tools: M1_ALLOWED_TOOLS, allowedTools: M1_ALLOWED_TOOLS,
        disallowedTools: M1_FORBIDDEN_TOOL_NAMES, permissionMode: 'dontAsk', permissionPrompts: 'none', settingSources: [],
        skills: [], plugins: [], agents: {}, mcpServers: {}, strictMcpConfig: true, persistSession: false,
        enableFileCheckpointing: false, abortController: controller,
        stderr: data => appendSafeDiagnostic(diagnosticStderr, data, key),
        hooks: {
          PreToolUse: [{ hooks: [async input => { const decision = policy.authorize(input.tool_name, input.tool_input); audit.push({ phase: 'pre', tool: input.tool_name, path: input.tool_input?.file_path ?? null, allowed: decision.allowed, reason: decision.reason }); return { hookSpecificOutput: { hookEventName: 'PreToolUse', permissionDecision: decision.allowed ? 'allow' : 'deny', permissionDecisionReason: decision.reason } }; }] }],
          PostToolUse: [{ hooks: [async input => { audit.push({ phase: 'post', tool: input.tool_name, path: input.tool_input?.file_path ?? null, allowed: true }); return {}; }] }],
        },
      },
    });
    for await (const message of stream) {
      if (message.type === 'assistant') assistantErrors.push(...captureAssistantDiagnostics(message, key));
      if (message.type === 'result') { terminal = captureTerminalDiagnostic(message, key); rawResult = message.result ?? null; usage = message.usage ?? message.modelUsage ?? null; reportedCost = message.total_cost_usd ?? null; break; }
    }
  } catch (error) { appendSafeDiagnostic(errors, error?.message ?? error, key); }
  finally { clearTimeout(timer); stream?.close?.(); }
  const structured = parseJson(rawResult);
  const classification = classifyTerminal({ terminal, assistantErrors, timedOut, structuredValid: Boolean(structured) });
  if (!classification.success) errors.push(classification.message);
  const changedFiles = audit.filter(item => item.phase === 'post' && ['Write', 'Edit'].includes(item.tool)).map(item => item.path);
  const gitDiff = collectGitDiff(worktree);
  const result = { task_id: task.task_id, status: classification.success && errors.length === 0 ? 'PENDING_CODEX_REVIEW' : 'FAIL', mode: 'sdk-write', api_calls: 1, model: MODEL, max_turns: task.max_turns, max_budget_usd: task.budget_usd, reported_api_cost_usd: reportedCost, billing: billingRecord({ mode: 'sdk', reportedCost, usage }), usage, terminal, assistant_errors: assistantErrors, diagnostic_stderr: diagnosticStderr, error_classification: classification, errors, audit, diff: { changed_files: changedFiles, outside_scope: audit.some(item => ['Write', 'Edit'].includes(item.tool) && item.allowed === false), patch_available: gitDiff.patch.length > 0, status: gitDiff.status }, structured_result: structured ?? { summary: null, changed_files: [], tests: [], risks: ['No valid structured result'] }, input_hashes: Object.fromEntries(task.inputs.map(file => [file, sha256(path.join(worktree, file))])) };
  const review = { review_version: 1, status: result.status === 'PENDING_CODEX_REVIEW' ? 'PENDING_CODEX_REVIEW' : 'FAIL', checks: { sdk_execution: classification.success, write_paths_audited: audit.filter(item => ['Write', 'Edit'].includes(item.tool)).every(item => item.allowed), no_production_scope: true, tests: 'Codex must run acceptance tests outside the Claude process' }, notes: 'No automatic fix or retry. Codex review and owner approval are required before merge.' };
  writeArtifact(runDir, 'result.json', JSON.parse(redactM1(result)));
  writeArtifact(runDir, 'review.json', review);
  writeArtifact(runDir, 'checkpoint.json', { task_id: task.task_id, status: review.status, result: 'result.json', review: 'review.json', api_calls: 1, reported_api_cost_usd: reportedCost, note: 'No automatic retry; merge requires owner approval.' });
  writeArtifact(runDir, 'diff-audit.json', result.diff);
  writeFileSync(path.join(runDir, 'changes.diff'), gitDiff.patch + (gitDiff.status ? `\n# git status\n${gitDiff.status}\n` : ''), { flag: 'wx' });
  appendLedger({ task_id: task.task_id, completed_at: new Date().toISOString(), mode: 'sdk-write', status: result.status, api_calls: 1, model: MODEL, reported_api_cost_usd: reportedCost, billing: result.billing, usage, error_classification: classification, errors });
  process.exitCode = result.status === 'PENDING_CODEX_REVIEW' ? 0 : 1;
}

try {
  const args = new Set(process.argv.slice(2));
  const taskIndex = process.argv.indexOf('--task');
  const worktreeIndex = process.argv.indexOf('--worktree');
  if (taskIndex < 0 || worktreeIndex < 0) throw new Error('Use --task <task.json> --worktree <path>');
  const taskFile = process.argv[taskIndex + 1];
  const worktree = path.resolve(process.argv[worktreeIndex + 1]);
  if (args.has('--preflight')) { const task = loadTask(taskFile); const identity = preflight(task, worktree, args.has('--approved'), { mock: args.has('--mock') }); console.log(JSON.stringify({ status: 'M1_PREFLIGHT_PASSED', task_id: task.task_id, model: MODEL, max_turns: task.max_turns, max_budget_usd: task.budget_usd, max_retries: task.max_retries, workspace_id: identity.workspace_id })); }
  else await execute({ taskFile, worktree, approved: args.has('--approved'), keyStdin: args.has('--key-stdin') });
} catch (error) { console.error(redactM1({ status: 'M1_BLOCKED', error: error?.message ?? String(error) })); process.exitCode = 1; }

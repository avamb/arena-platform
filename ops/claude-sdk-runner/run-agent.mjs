// run-agent.mjs — запускает одну задачу через Claude Agent SDK по API-ключу организации
// (promo-кредиты Claude Platform), а не по подписке claude.ai.
//
// Ключ читается ТОЛЬКО из stdin (его подаёт run-agent.ps1 из Windows Credential Manager).
// Ни в аргументы, ни в файлы, ни в глобальное окружение ключ не попадает; дочернему claude.exe
// он передаётся через env вместе с отдельным CLAUDE_CONFIG_DIR, чтобы подписочный логин
// десктопного Claude Code не читался и не затирался.
//
// Использование (обычно через run-agent.ps1):
//   node run-agent.mjs --prompt-file <путь> --cwd <папка> [--model claude-sonnet-5-5]
//        [--max-turns 30] [--max-budget-usd 3] [--effort low|medium|high]
//        [--mode read|edit] [--out <папка для логов>] [--timeout-min 30] < key
//
// --mode read : только чтение (Read/Glob/Grep/Bash без записи — Bash запрещён целиком).
// --mode edit : Read/Glob/Grep/Write/Edit/Bash, без сети и без подагентов.
// Итог: <out>/result.json (статус, стоимость, usage), <out>/result.md (ответ модели),
// <out>/events.jsonl (поток сообщений без секретов).

import { createRequire } from 'node:module';
import { existsSync, mkdirSync, readFileSync, writeFileSync, appendFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const DEFAULT_SDK_ROOT = 'C:/Projects/Venue_SVG_Generator/sdk_experiments';

function arg(name, fallback) {
  const i = process.argv.indexOf(name);
  return i >= 0 && process.argv[i + 1] !== undefined ? process.argv[i + 1] : fallback;
}
const promptFile = arg('--prompt-file');
const cwd = path.resolve(arg('--cwd', process.cwd()));
const model = arg('--model', 'claude-sonnet-5-5');
const maxTurns = Number(arg('--max-turns', '30'));
const maxBudgetUsd = Number(arg('--max-budget-usd', '3'));
const effort = arg('--effort', 'medium');
const mode = arg('--mode', 'read');
const timeoutMs = Number(arg('--timeout-min', '30')) * 60_000;
const sdkRoot = arg('--sdk-root', process.env.CLAUDE_SDK_ROOT || DEFAULT_SDK_ROOT);
// Ключ организации не привязан к workspace, поэтому API требует заголовок anthropic-workspace-id.
// Берём id из подтверждения, которое уже ведёт проект генератора (wrkspc_..., не секрет).
function defaultWorkspaceId() {
  const f = path.join(sdkRoot, 'workspace-confirmation.json');
  if (!existsSync(f)) return '';
  try { const j = JSON.parse(readFileSync(f, 'utf8')); return j.confirmed ? String(j.workspace_id || '') : ''; } catch { return ''; }
}
const workspaceId = arg('--workspace-id', process.env.ANTHROPIC_WORKSPACE_ID || defaultWorkspaceId());
if (!/^wrkspc_[A-Za-z0-9]+$/.test(workspaceId)) { console.error('workspace id missing: pass --workspace-id wrkspc_...'); process.exit(2); }
const stamp = new Date().toISOString().replace(/[:.]/g, '-');
const out = path.resolve(arg('--out', path.join(HERE, 'runs', stamp)));

if (!promptFile) { console.error('--prompt-file is required'); process.exit(2); }
if (!existsSync(cwd)) { console.error(`cwd does not exist: ${cwd}`); process.exit(2); }
if (!['read', 'edit'].includes(mode)) { console.error('--mode must be read or edit'); process.exit(2); }

const key = readFileSync(0, 'utf8').trim();
if (!/^sk-ant-[A-Za-z0-9_-]{20,}$/.test(key)) { console.error('stdin did not carry an Anthropic API key'); process.exit(2); }
const redact = v => String(v).split(key).join('[REDACTED]').replace(/sk-ant-[A-Za-z0-9_-]+/g, '[REDACTED]');

const requireSdk = createRequire(path.join(sdkRoot, 'package.json'));
const { query } = requireSdk('@anthropic-ai/claude-agent-sdk');
const cli = requireSdk.resolve('@anthropic-ai/claude-agent-sdk-win32-x64/claude.exe');

mkdirSync(out, { recursive: true });
const eventsPath = path.join(out, 'events.jsonl');
const logEvent = obj => appendFileSync(eventsPath, redact(JSON.stringify(obj)) + '\n');

const READ_TOOLS = ['Read', 'Glob', 'Grep'];
const EDIT_TOOLS = ['Read', 'Glob', 'Grep', 'Write', 'Edit', 'Bash'];
const allowedTools = mode === 'edit' ? EDIT_TOOLS : READ_TOOLS;
const disallowedTools = ['WebSearch', 'WebFetch', 'Agent', 'Task', 'Skill', 'ToolSearch', 'AskUserQuestion',
  'EnterPlanMode', 'ExitPlanMode', 'NotebookEdit', 'mcp__*', ...(mode === 'edit' ? [] : ['Bash', 'Write', 'Edit'])];

const env = {
  PATH: process.env.PATH, SystemRoot: process.env.SystemRoot, TEMP: process.env.TEMP, TMP: process.env.TMP,
  USERPROFILE: process.env.USERPROFILE, HOME: process.env.USERPROFILE,
  ANTHROPIC_API_KEY: key, ANTHROPIC_BASE_URL: 'https://api.anthropic.com',
  ANTHROPIC_WORKSPACE_ID: workspaceId, ANTHROPIC_CUSTOM_HEADERS: `anthropic-workspace-id: ${workspaceId}`,
  CLAUDE_CONFIG_DIR: path.join(HERE, '.config'),
  CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: '1', DISABLE_TELEMETRY: '1', DISABLE_ERROR_REPORTING: '1',
  DISABLE_AUTOUPDATER: '1', CLAUDE_AGENT_SDK_CLIENT_APP: 'arena-sdk-runner',
};

const prompt = readFileSync(promptFile, 'utf8');
const controller = new AbortController();
const timer = setTimeout(() => controller.abort(), timeoutMs);
let result = null, text = '', error = null;
const started = Date.now();

try {
  const stream = query({
    prompt,
    options: {
      pathToClaudeCodeExecutable: cli, cwd, env, model, effort, maxTurns, maxBudgetUsd,
      allowedTools, disallowedTools,
      permissionMode: mode === 'edit' ? 'bypassPermissions' : 'dontAsk',
      allowDangerouslySkipPermissions: mode === 'edit',
      permissionPrompts: 'none', settingSources: [], skills: [], plugins: [], agents: {}, mcpServers: {},
      strictMcpConfig: true, persistSession: false, enableFileCheckpointing: false,
      abortController: controller,
      stderr: data => logEvent({ type: 'stderr', data: String(data).slice(0, 2000) }),
    },
  });
  for await (const m of stream) {
    if (m.type === 'assistant') {
      for (const block of m.message?.content ?? []) {
        if (block.type === 'text') logEvent({ type: 'text', text: block.text.slice(0, 4000) });
        if (block.type === 'tool_use') logEvent({ type: 'tool_use', name: block.name, input: JSON.stringify(block.input).slice(0, 1000) });
      }
    } else if (m.type === 'result') {
      result = m; text = m.result ?? '';
      break;
    } else {
      logEvent({ type: m.type });
    }
  }
} catch (e) {
  error = redact(e?.message ?? String(e));
} finally {
  clearTimeout(timer);
}

// Ошибка API приходит как result с subtype success и текстом "API Error: ..." — считаем её ошибкой.
const apiErrorText = /^API Error:/i.test(text.trim());
const summary = {
  status: error ? 'error' : apiErrorText ? 'api_error' : (result?.subtype ?? 'unknown'),
  is_error: Boolean(error) || Boolean(result?.is_error) || apiErrorText,
  error,
  model, mode, cwd, prompt_file: path.resolve(promptFile),
  num_turns: result?.num_turns ?? null,
  total_cost_usd: result?.total_cost_usd ?? null,
  duration_ms: Date.now() - started,
  usage: result?.usage ?? null,
  model_usage: result?.modelUsage ?? null,
  out,
};
writeFileSync(path.join(out, 'result.json'), redact(JSON.stringify(summary, null, 2)) + '\n');
writeFileSync(path.join(out, 'result.md'), redact(text) + '\n');
console.log(redact(JSON.stringify({ status: summary.status, cost_usd: summary.total_cost_usd, turns: summary.num_turns, out }, null, 2)));
process.exit(summary.is_error ? 1 : 0);

import { existsSync, lstatSync, realpathSync } from 'node:fs';
import path from 'node:path';

export const M1_ALLOWED_TOOLS = Object.freeze(['Read', 'Write', 'Edit']);
export const M1_FORBIDDEN_TOOL_NAMES = Object.freeze([
  'Bash', 'NotebookEdit', 'Glob', 'Grep', 'WebSearch', 'WebFetch', 'Agent', 'Task',
  'Skill', 'ToolSearch', 'TodoWrite', 'TaskCreate', 'TaskUpdate', 'TaskList',
  'TaskGet', 'TaskOutput', 'TaskStop', 'AskUserQuestion', 'EnterPlanMode', 'ExitPlanMode',
]);

const SECRET_PATTERNS = [
  /sk-ant-[A-Za-z0-9_-]+/i,
  /ANTHROPIC_API_KEY\s*=/i,
  /CLAUDE_CODE_OAUTH_TOKEN\s*=/i,
];

function normalized(value) {
  return path.resolve(String(value)).toLowerCase();
}

function relativeTo(root, value) {
  const rel = path.relative(root, value);
  if (rel === '..' || rel.startsWith(`..${path.sep}`) || path.isAbsolute(rel)) return null;
  return rel;
}

function isWithin(root, candidate) {
  const base = normalized(root).replace(/[\\/]+$/, '');
  const value = normalized(candidate).replace(/[\\/]+$/, '');
  return value === base || value.startsWith(`${base}${path.sep}`);
}

function nearestExistingAncestor(filePath) {
  let current = filePath;
  while (!existsSync(current)) {
    const parent = path.dirname(current);
    if (parent === current) return null;
    current = parent;
  }
  return current;
}

/** Resolve a tool path while rejecting traversal and symlink escapes. */
export function resolveWorktreePath(worktreeRoot, value) {
  if (typeof value !== 'string' || value.trim() === '') throw new Error('tool path is required');
  const root = realpathSync(worktreeRoot);
  const candidate = path.resolve(root, value);
  if (!isWithin(root, candidate)) throw new Error('path outside worktree');
  const existing = nearestExistingAncestor(candidate);
  if (!existing) throw new Error('path has no existing parent');
  const canonicalExisting = realpathSync(existing);
  if (!isWithin(root, canonicalExisting)) throw new Error('symlink escapes worktree');
  return candidate;
}

function pathMatches(root, candidate, configured) {
  const base = path.resolve(root, configured);
  return isWithin(base, candidate);
}

function forbiddenPath(root, candidate, forbiddenPaths) {
  return forbiddenPaths.some(item => {
    try { return pathMatches(root, candidate, item); } catch { return true; }
  });
}

/**
 * Hook policy for the write-capable M1 task. It is a tool policy, not an OS
 * sandbox: the caller must still run in a dedicated worktree and review diff.
 */
export function createM1ToolPolicy({ worktreeRoot, readPaths, writePaths, forbiddenPaths }) {
  const root = realpathSync(worktreeRoot);
  const forbidden = [...forbiddenPaths, 'arena_new', '.git', '.env', 'sdk_experiments/console-confirmation.json', 'sdk_experiments/workspace-confirmation.json'];
  function authorize(toolName, toolInput = {}) {
    if (!M1_ALLOWED_TOOLS.includes(toolName)) {
      return { allowed: false, reason: `tool ${toolName} is denied; only Read/Write/Edit are allowed` };
    }
    if (toolName === 'Read') {
      try {
        const candidate = resolveWorktreePath(root, toolInput.file_path);
        const allowed = readPaths.some(item => pathMatches(root, candidate, item)) && !forbiddenPath(root, candidate, forbidden);
        return { allowed, reason: allowed ? 'approved read path' : 'read path is outside the task allowlist' };
      } catch (error) { return { allowed: false, reason: error.message }; }
    }
    try {
      const candidate = resolveWorktreePath(root, toolInput.file_path);
      const allowed = writePaths.some(item => pathMatches(root, candidate, item)) && !forbiddenPath(root, candidate, forbidden);
      return { allowed, reason: allowed ? 'approved write path' : 'write path is outside the task allowlist' };
    } catch (error) { return { allowed: false, reason: error.message }; }
  }
  return Object.freeze({ root, authorize });
}

export function validateM1Task(task, worktreeRoot) {
  if (!task || typeof task !== 'object') throw new Error('task must be an object');
  for (const key of ['task_id', 'objective', 'inputs', 'read_paths', 'write_paths', 'forbidden_paths', 'acceptance']) {
    if (!Object.hasOwn(task, key)) throw new Error(`missing task field: ${key}`);
  }
  if (task.api_allowed !== true) throw new Error('M1 write task must be explicitly API-enabled only at approved launch');
  if (task.max_retries !== 0) throw new Error('automatic retries are forbidden');
  if (!Number.isFinite(task.budget_usd) || task.budget_usd <= 0 || task.budget_usd > 10) throw new Error('M1 budget must be in (0, 10]');
  if (!Number.isInteger(task.max_turns) || task.max_turns < 1 || task.max_turns > 8) throw new Error('max_turns must be 1..8');
  if (!Array.isArray(task.commands) || task.commands.length !== 0) throw new Error('shell commands are not allowed');
  const serialized = JSON.stringify(task);
  if (SECRET_PATTERNS.some(pattern => pattern.test(serialized))) throw new Error('secret-like value in task packet');
  const root = realpathSync(worktreeRoot);
  for (const item of [...task.inputs, ...task.read_paths, ...task.write_paths]) {
    const candidate = resolveWorktreePath(root, item);
    if (lstatSync(candidate, { throwIfNoEntry: false })?.isSymbolicLink()) throw new Error(`symlink path is forbidden: ${item}`);
  }
  if (!task.forbidden_paths.some(item => item.toLowerCase() === 'arena_new')) throw new Error('arena_new must be explicitly forbidden');
  return true;
}

export function buildM1Prompt(task) {
  const inputs = task.inputs.map(file => `- ${file}`).join('\n');
  const reads = task.read_paths.map(file => `- ${file}`).join('\n');
  const writes = task.write_paths.map(file => `- ${file}`).join('\n');
  return `${task.objective}\n\nApproved input files (read only):\n${inputs}\n\nApproved read paths:\n${reads}\n\nApproved write paths (the only paths you may create or edit):\n${writes}\n\nExpected outputs:\n${(task.expected_outputs ?? []).map(item => `- ${item}`).join('\n')}\n\nAcceptance criteria:\n${task.acceptance.map(item => `- ${item}`).join('\n')}\n\nReturn JSON with summary, changed_files, tests, risks, incomplete_components. Report incomplete work explicitly; never claim PASS without evidence. Do not read or use reference SVG files.`;
}

export function redactM1(value) {
  let text = typeof value === 'string' ? value : JSON.stringify(value, null, 2);
  for (const pattern of SECRET_PATTERNS) text = text.replace(pattern, '[REDACTED]');
  return text;
}

import { spawnSync } from 'node:child_process';

const SECRET_PATTERNS = [
  /sk-ant-[A-Za-z0-9_-]+/gi,
  /ANTHROPIC_API_KEY\s*[=:]\s*[^\s]+/gi,
  /Bearer\s+[^\s]+/gi,
];

export function redactReviewText(value) {
  let text = typeof value === 'string' ? value : JSON.stringify(value);
  for (const pattern of SECRET_PATTERNS) text = text.replace(pattern, '[REDACTED]');
  return text.slice(0, 12000);
}

export function validateCodexReview(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return { verdict: 'BLOCKED', reason: 'review is not an object' };
  if (!['PASS', 'REJECT', 'BLOCKED'].includes(value.verdict)) return { verdict: 'BLOCKED', reason: 'invalid verdict' };
  if (!Array.isArray(value.findings) || !value.findings.every(item => typeof item === 'string')) return { verdict: 'BLOCKED', reason: 'invalid findings' };
  if (typeof value.tests_passed !== 'boolean' || typeof value.scope_ok !== 'boolean') return { verdict: 'BLOCKED', reason: 'missing boolean gates' };
  if (value.verdict === 'PASS' && (!value.tests_passed || !value.scope_ok)) return { verdict: 'REJECT', reason: 'PASS requires tests_passed and scope_ok' };
  return { verdict: value.verdict, findings: value.findings.slice(0, 50), tests_passed: value.tests_passed, scope_ok: value.scope_ok, summary: typeof value.summary === 'string' ? value.summary.slice(0, 4000) : null };
}

function findReviewObject(value) {
  if (!value || typeof value !== 'object') return null;
  if (Object.hasOwn(value, 'verdict')) return value;
  for (const child of Object.values(value)) {
    const found = findReviewObject(child);
    if (found) return found;
  }
  return null;
}

export function parseCodexJsonl(stdout) {
  const candidates = [];
  for (const line of String(stdout ?? '').split(/\r?\n/)) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    try {
      const parsed = JSON.parse(trimmed);
      const found = findReviewObject(parsed);
      if (found) candidates.push(found);
    } catch { /* non-JSON progress line */ }
  }
  const candidate = candidates.at(-1);
  return candidate ? validateCodexReview(candidate) : { verdict: 'BLOCKED', reason: 'no structured review object' };
}

export function runCodexReview({ worktree, prompt, schemaPath, timeoutMs = 300000, command = 'codex.cmd' }) {
  const child = spawnSync(command, [
    'exec', 'review', '--json', '--ephemeral', '--uncommitted', '--output-schema', schemaPath, '-',
  ], { cwd: worktree, input: prompt, encoding: 'utf8', windowsHide: true, timeout: timeoutMs });
  const timedOut = child.error?.code === 'ETIMEDOUT';
  const parsed = timedOut ? { verdict: 'BLOCKED', reason: 'codex review timed out' } : parseCodexJsonl(child.stdout);
  return {
    ...parsed,
    process_exit_code: child.status,
    timed_out: timedOut,
    stdout: redactReviewText(child.stdout),
    stderr: redactReviewText(child.stderr),
  };
}

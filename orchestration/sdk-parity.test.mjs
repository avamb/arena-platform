import assert from 'node:assert/strict';
import test from 'node:test';
import { readFileSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const oldRunner = readFileSync(path.join(ROOT, 'sdk_experiments', 'runner.mjs'), 'utf8');
const newRunner = readFileSync(path.join(ROOT, 'orchestration', 'sdk-task-runner.mjs'), 'utf8');
const sdkPackage = JSON.parse(readFileSync(path.join(ROOT, 'sdk_experiments', 'node_modules', '@anthropic-ai', 'claude-agent-sdk', 'package.json'), 'utf8'));

test('documents SDK process parity and intentional differences offline', () => {
  assert.equal(sdkPackage.version, '0.3.296');
  assert.match(oldRunner, /model: MODEL/);
  assert.match(newRunner, /model: MODEL/);
  assert.match(oldRunner, /maxTurns: 2/);
  assert.match(newRunner, /maxTurns: task\.max_turns/);
  assert.match(oldRunner, /maxBudgetUsd: 0\.25/);
  assert.match(newRunner, /maxBudgetUsd: task\.budget_usd/);
  assert.match(oldRunner, /tools: \['Read'\], allowedTools: \['Read'\]/);
  assert.match(newRunner, /tools: \['Read'\], allowedTools: \['Read'\]/);
  assert.match(oldRunner, /cwd: WORKSPACE/);
  assert.match(newRunner, /cwd: ROOT/);
  assert.match(oldRunner, /CLAUDE_CONFIG_DIR: path\.join\(ROOT, '\.runtime-home'\)/);
  assert.match(newRunner, /CLAUDE_CONFIG_DIR: path\.join\(ORCH, 'runtime'\)/);
  assert.doesNotMatch(oldRunner, /ANTHROPIC_WORKSPACE_ID/);
  assert.match(newRunner, /ANTHROPIC_WORKSPACE_ID: pf\.workspace_id/);
  assert.match(newRunner, /ANTHROPIC_CUSTOM_HEADERS: `anthropic-workspace-id: \$\{pf\.workspace_id\}`/);
  assert.match(oldRunner, /APPDATA/);
  assert.doesNotMatch(newRunner, /APPDATA/);
  assert.match(oldRunner, /systemPrompt:/);
  assert.doesNotMatch(newRunner, /systemPrompt:/);
  assert.match(oldRunner, /stderr: \(\) => \{\}/);
  assert.match(newRunner, /stderr: data =>/);
});

test('workspace identifier is configured only in the child SDK environment', () => {
  assert.match(newRunner, /ANTHROPIC_WORKSPACE_ID: pf\.workspace_id/);
  assert.doesNotMatch(newRunner, /process\.env\.ANTHROPIC_WORKSPACE_ID\s*=/);
  assert.match(newRunner, /workspace_id: identity\?\.workspace_id/);
});

test('existing latch blocks a repeated local run without starting the SDK', () => {
  const result = spawnSync(process.execPath, [
    path.join(ROOT, 'orchestration', 'sdk-task-runner.mjs'),
    '--task', 'orchestration/tasks/orchestration-telemetry-mock-v2.json', '--mock',
  ], { cwd: ROOT, encoding: 'utf8' });
  assert.equal(result.status, 1);
  assert.match(result.stderr, /Attempt latch exists/);
});

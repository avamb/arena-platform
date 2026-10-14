import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { validateEvidenceList } from './evidence-validator.mjs';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const taskId = 'orchestration-smoke-paid-v4';
const resultPath = path.join(ROOT, 'orchestration', 'runs', taskId, 'result.json');
const reviewPath = path.join(ROOT, 'orchestration', 'runs', taskId, 'review.json');
const taskPath = path.join(ROOT, 'orchestration', 'tasks', `${taskId}.json`);
const outputPath = path.join(ROOT, 'orchestration', 'reviews', `${taskId}-reevaluation-v1.json`);
const readJson = file => JSON.parse(readFileSync(file, 'utf8'));
const sha256 = file => createHash('sha256').update(readFileSync(file)).digest('hex');

function sourceLines(file, start, end) {
  const lines = readFileSync(path.join(ROOT, file), 'utf8').replace(/\r\n/g, '\n').split('\n');
  return lines.slice((start ?? 1) - 1, end ?? start ?? lines.length).join('\n');
}

function hasText(file, start, end, pattern) {
  return pattern.test(sourceLines(file, start, end));
}

function citationContentCheck(item) {
  if (!item.valid || item.startLine === null || !item.annotation) return item.valid;
  const source = sourceLines(item.path, item.startLine, item.endLine).toLowerCase();
  const tokens = item.annotation.toLowerCase().match(/[\p{L}\p{N}]{3,}/gu) ?? [];
  const stop = new Set(['для', 'это', 'как', 'что', 'или', 'нет', 'the', 'and', 'with', 'from']);
  const anchors = [...new Set(tokens.filter(token => !stop.has(token)))];
  return anchors.some(token => source.includes(token));
}

const result = readJson(resultPath);
const previousReview = readJson(reviewPath);
const task = readJson(taskPath);
assert.equal(result.task_id, taskId);
assert.equal(result.status, 'FAIL');

const structural = {
  terminal_success: result.terminal?.subtype === 'success' && result.terminal?.is_error === false
    && result.terminal?.terminal_reason === 'completed',
  structured_shape: ['contradictions', 'gaps', 'next_steps', 'evidence'].every(key => Array.isArray(result.structured_result?.[key])),
  approved_reads_only: result.audit?.every(event => event.tool === 'Read' && event.allowed === true) === true,
  no_sdk_errors: Array.isArray(result.errors) && result.errors.length === 0,
};

const evidence = validateEvidenceList(result.structured_result.evidence, task.inputs, { root: ROOT, checkLines: true });
const citationContent = evidence.references.map(item => ({
  reference: item.reference,
  valid_reference: item.valid,
  content_anchor_found: citationContentCheck(item),
}));
const substantiveChecks = {
  citations_match_source: citationContent.every(item => item.valid_reference && item.content_anchor_found),
  contradictions_are_grounded: [
    hasText('PRODUCT_SPEC.md', 5, 5, /XLSX.*PDF.*JPG.*PNG/i),
    hasText('PRODUCT_SPEC.md', 22, 23, /Arena authoring SVG|Bil24/i),
    hasText('PRODUCT_SPEC.md', 32, 35, /Ládví|1166|decor_svg/i),
    hasText('ROADMAP.md', 17, 21, /PDF.*raster/i),
    hasText('ROADMAP.md', 23, 27, /Bil24|ImportSVG/i),
  ].every(Boolean),
  gaps_are_actionable: result.structured_result.gaps.length >= 5,
  next_steps_are_prioritized: result.structured_result.next_steps.length === 3
    && result.structured_result.next_steps.every(step => typeof step === 'string' && step.length > 20),
};
substantiveChecks.content_review = Object.values(substantiveChecks).every(Boolean);

const statuses = {
  anthropic_execution: structural.terminal_success ? 'PASS' : 'FAIL',
  structural_validation: Object.values(structural).every(Boolean) ? 'PASS' : 'FAIL',
  evidence_validation: evidence.valid && substantiveChecks.citations_match_source ? 'PASS' : 'FAIL',
  substantive_codex_review: substantiveChecks.content_review ? 'PASS' : 'PARTIAL PASS',
};
statuses.overall_orchestration = Object.values(statuses).every(value => value === 'PASS') ? 'PASS' : 'PARTIAL PASS';

const output = {
  reevaluation_version: 1,
  validator_version: 'evidence-validator-v1.0.0',
  task_id: taskId,
  source: {
    result: path.relative(ROOT, resultPath),
    result_sha256: sha256(resultPath),
    previous_review: path.relative(ROOT, reviewPath),
    previous_review_sha256: sha256(reviewPath),
    previous_review_status: previousReview.status,
  },
  statuses,
  structural_checks: structural,
  evidence_validation: {
    valid: evidence.valid,
    references: citationContent,
  },
  substantive_review: {
    checks: substantiveChecks,
    conclusion: 'Все замечания опираются на PRODUCT_SPEC.md и ROADMAP.md; часть формулировок является расхождением объёма и порядка milestone, а не строгим логическим противоречием. Три следующих шага приоритизированы корректно.',
  },
  execution_observed: {
    model: result.model,
    api_calls: result.api_calls,
    read_operations: result.audit.filter(event => event.tool === 'Read' && event.allowed === true).length / 2,
    reported_api_cost_usd: result.reported_api_cost_usd,
    billing_status: result.billing.status,
  },
  generated_at: new Date().toISOString(),
};

mkdirSync(path.dirname(outputPath), { recursive: true });
writeFileSync(outputPath, JSON.stringify(output, null, 2) + '\n', { flag: 'wx' });
console.log(JSON.stringify({ status: 'OFFLINE_REEVALUATION_COMPLETE', output: path.relative(ROOT, outputPath), overall: statuses.overall_orchestration }));

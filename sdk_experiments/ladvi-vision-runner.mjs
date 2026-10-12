import assert from 'node:assert/strict';
import { existsSync, readFileSync, writeFileSync, mkdirSync, lstatSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import Anthropic from '@anthropic-ai/sdk';
import { estimateApiCostUsd, parseAssistantText } from './ladvi-response-utils.mjs';
import { API_OUTPUT_SCHEMA, OUTPUT_FORMAT, OUTPUT_SCHEMA, SEMANTIC_LIMITS, validateSemanticDelta } from './ladvi-schema.mjs';

const SDK_ROOT = path.dirname(fileURLToPath(import.meta.url));
const PROJECT = path.resolve(SDK_ROOT, '..');
const INPUT = path.join(PROJECT, 'benchmark_v1', 'phase5a_ladvi', 'claude-input');
const IMAGE = path.join(INPUT, 'photo_2024-02-13_09-18-52.jpg');
const INPUT_MANIFEST = path.join(INPUT, 'input-manifest.json');
const PROMPT = path.join(INPUT, 'prompt-ru.txt');
const MODEL_PATH = path.join(PROJECT, 'benchmark_v1', 'phase5a_ladvi', 'seating-model.json');
const CONFIRM = path.join(SDK_ROOT, 'console-confirmation.json');
const WORKSPACE_CONFIRM = path.join(SDK_ROOT, 'workspace-confirmation.json');
const RESULTS = path.join(SDK_ROOT, 'results');
const MODEL = 'claude-sonnet-5-5';
const MAX_BUDGET_USD = 2;
const MAX_TURNS = 1;
const MAX_TOKENS = 1800;
const MODEL_PROFILE = Object.freeze({
  model: MODEL,
  thinking: Object.freeze({ type: 'between_tools' }),
  structured_outputs: true,
  max_tokens: MAX_TOKENS,
});
const IMAGE_MIME = 'image/jpeg';
const PYTHON = 'C:\\Users\\andre\\.cache\\codex-runtimes\\codex-primary-runtime\\dependencies\\python\\python.exe';

const digest = data => createHash('sha256').update(data).digest('hex');
const readJson = file => JSON.parse(readFileSync(file, 'utf8'));
const safeText = value => String(value ?? '').replace(/sk-ant-[A-Za-z0-9_-]+/g, '[REDACTED]').replace(/Bearer\s+\S+/gi, 'Bearer [REDACTED]');

function checkConfirmation() {
  const c = readJson(CONFIRM);
  for (const key of ['organization', 'workspace']) assert.equal(typeof c[key], 'string');
  for (const key of ['key_workspace_confirmed', 'promotional_credit_eligible', 'auto_reload_disabled', 'spending_limits_reviewed']) assert.equal(c[key], true, `${key} must be true`);
  assert.ok(Number.isFinite(c.promotional_balance_usd) && c.promotional_balance_usd >= MAX_BUDGET_USD);
  const checked = Date.parse(c.checked_at);
  assert.ok(Number.isFinite(checked) && Date.now() >= checked && Date.now() - checked < 24 * 3600000, 'Console confirmation is older than 24 hours');
  assert.ok(/^\d{4}-\d{2}-\d{2}$/.test(c.promotional_expiry));
  assert.ok(new Date().toISOString().slice(0, 10) < c.promotional_expiry, 'Promotional credit expired or expires today');
  return c;
}

function readWorkspaceConfirmation() {
  assert.ok(existsSync(WORKSPACE_CONFIRM),
    'Workspace ID is not configured. Create workspace-confirmation.json with the real Anthropic Console ID (wrkspc_...).');
  const value = readJson(WORKSPACE_CONFIRM);
  assert.equal(value.workspace_name, 'Default', 'Workspace confirmation must target Default');
  assert.match(value.workspace_id, /^wrkspc_[A-Za-z0-9_-]+$/, 'workspace_id must be the real wrkspc_... ID');
  assert.equal(value.confirmed, true, 'Workspace ID must be explicitly confirmed');
  return value;
}

function parseAttemptId() {
  const prefix = '--attempt-id=';
  const arg = process.argv.find(value => value.startsWith(prefix));
  const attemptId = arg ? arg.slice(prefix.length) : '';
  assert.match(attemptId, /^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$/, 'A new explicit attempt ID is required');
  return attemptId;
}

function resultPaths(attemptId) {
  return {
    latch: path.join(RESULTS, `ladvi-attempt-${attemptId}.json`),
    result: path.join(RESULTS, `ladvi-vision-${attemptId}.json`),
    evaluation: path.join(RESULTS, `ladvi-benchmark-evaluation-${attemptId}.json`),
  };
}

function loadPreflight() {
  assert.equal(MODEL_PROFILE.model, 'claude-sonnet-5-5');
  assert.equal(MODEL_PROFILE.thinking.type, 'between_tools', 'claude-sonnet-5-5 requires between_tools thinking');
  assert.equal(MODEL_PROFILE.structured_outputs, true);
  assert.equal(MODEL_PROFILE.max_tokens, 1800);
  assert.ok(existsSync(IMAGE) && !lstatSync(IMAGE).isSymbolicLink());
  assert.ok(existsSync(PROMPT) && existsSync(INPUT_MANIFEST) && existsSync(MODEL_PATH));
  const manifest = readJson(INPUT_MANIFEST);
  const model = readJson(MODEL_PATH);
  assert.equal(manifest.reference_access, 'forbidden');
  assert.equal(manifest.sha256, '6f2a535189c9dc7cafa124eaff7124fc37148bf9e8893ce97d7d5ea2421bec52');
  assert.equal(model.schema_version, 'phase5a-raster-1');
  assert.equal(model.seats.length, 432); assert.equal(model.geometric_rows.length, 18);
  assert.ok(model.seats.every(s => s.original_seat_number === null && s.official_row_label === null));
  const image = readFileSync(IMAGE);
  assert.equal(image[0], 0xff); assert.equal(image[1], 0xd8);
  const prompt = readFileSync(PROMPT, 'utf8');
  assert.match(prompt, /не выдумывать|не заполнять номера/i);
  assert.doesNotMatch(prompt, /Palac_Akropolis|сultural_centre|reference-evaluation/i);
  assert.equal(typeof OUTPUT_SCHEMA.properties.semantic_delta, 'object');
  assert.equal(SEMANTIC_LIMITS.seats, 64);
  assert.equal(API_OUTPUT_SCHEMA.properties.semantic_delta.additionalProperties, false);
  assert.equal(OUTPUT_SCHEMA.type, 'object');
  const coordinateContext = model.geometric_rows.map(row => ({ geometric_row_id: row.geometric_row_id,
    center_y_px: row.center_y_px, seats: row.seat_keys_left_to_right.map(key => {
      const s = model.seats.find(x => x.seat_key === key); return { seat_key: key, x: s.position_px.x, y: s.position_px.y };
    }) }));
  const coordinateText = JSON.stringify(coordinateContext);
  const estimatedInputTokens = Math.ceil((prompt.length + coordinateText.length) / 4) + 1600;
  const estimatedCost = estimatedInputTokens * 2 / 1e6 + MAX_TOKENS * 10 / 1e6;
  assert.ok(estimatedCost < MAX_BUDGET_USD);
  return { manifest, model, modelDigest: digest(readFileSync(MODEL_PATH)), image, prompt, coordinateText, estimatedInputTokens, estimatedCost };
}

function makeUserText(preflight) {
  return `${preflight.prompt}\n\nCoordinate index for already detected seats (technical IDs only; do not infer labels from order):\n${preflight.coordinateText}\n\nReturn only a compact semantic_delta JSON object. Include only labels or seat numbers that are visibly readable and linked to a technical row/seat key; do not enumerate all 432 seats, repeat coordinates, or invent sequential numbering. Use null for an unreadable candidate and list unavailable information in unknowns. Keep evidence short. Local limits: at most ${SEMANTIC_LIMITS.rows} row labels, ${SEMANTIC_LIMITS.seats} seat-number observations, ${SEMANTIC_LIMITS.sectors} sector labels, and ${SEMANTIC_LIMITS.unknowns} unknown entries.`;
}

async function runOnce() {
  assert.ok(process.argv.includes('--approved')); assert.ok(process.argv.includes('--key-stdin'));
  assert.ok(!process.stdin.isTTY, 'Use secure PowerShell launcher');
  const confirmation = checkConfirmation();
  const workspace = readWorkspaceConfirmation();
  const attemptId = parseAttemptId();
  const paths = resultPaths(attemptId);
  const preflight = loadPreflight();
  if (existsSync(paths.latch) || existsSync(paths.result) || existsSync(paths.evaluation)) {
    throw new Error(`Attempt ID ${attemptId} already has result files; choose a new ID.`);
  }
  let key = readFileSync(0, 'utf8').trim();
  assert.match(key, /^sk-ant-[A-Za-z0-9_-]{20,}$/);
  mkdirSync(RESULTS, { recursive: true });
  writeFileSync(paths.latch, JSON.stringify({ attempt_id: attemptId, started_at: new Date().toISOString(), model: MODEL,
    max_tokens: MAX_TOKENS, max_budget_usd: MAX_BUDGET_USD, max_requests: 1,
    workspace_id: workspace.workspace_id, input_sha256: preflight.manifest.sha256 }, null, 2) + '\n', { flag: 'wx' });
  const started = Date.now();
  const report = { schema_version: 'ladvi-vision-result-2', response_contract: 'semantic-delta-v2', case_id: 'ladvi-raster-baseline', model: MODEL,
    attempt_id: attemptId,
    max_tokens: MAX_TOKENS, max_budget_usd: MAX_BUDGET_USD, max_requests: 1,
    console_organization: confirmation.organization, console_workspace: confirmation.workspace,
    workspace_id: workspace.workspace_id, workspace_header: 'anthropic-workspace-id',
    input_image: path.relative(PROJECT, IMAGE), input_image_sha256: preflight.manifest.sha256,
    source_model_sha256: preflight.modelDigest, usage: null,
    reported_api_cost_usd: null, estimated_api_cost_usd: null, duration_ms: null,
    assistant_text: null, assistant_result: null, parse_error_classification: null, errors: [], budget_protection: { max_tokens: MAX_TOKENS, retries: 0,
      note: 'Messages API has no exact dollar ceiling; Console and usage are authoritative.' } };
  try {
    const client = new Anthropic({ apiKey: key, maxRetries: 0, timeout: 90000 });
    const response = await client.messages.create({ model: MODEL, max_tokens: MAX_TOKENS,
      workspace_id: workspace.workspace_id,
      thinking: MODEL_PROFILE.thinking,
      system: 'You are a cautious visual annotation extractor. Never infer a sequence from geometry. Unknown is correct when text is absent or unreadable.',
      messages: [{ role: 'user', content: [
        { type: 'image', source: { type: 'base64', media_type: IMAGE_MIME, data: preflight.image.toString('base64') },
          transformations: { oversized_image: 'error' } },
        { type: 'text', text: makeUserText(preflight) },
      ] }], output_config: { format: OUTPUT_FORMAT },
    });
    report.usage = response.usage ?? null; report.stop_reason = response.stop_reason ?? null;
    report.response_id = response.id ?? null; report.response_model = response.model ?? null;
    const block = response.content?.find(x => x.type === 'text');
    report.assistant_text = block?.text ?? null;
    report.estimated_api_cost_usd = estimateApiCostUsd(report.usage);
    if (!block?.text) throw new Error('Messages response did not contain structured text');
    const parsed = parseAssistantText(block.text, report.stop_reason);
    if (!parsed.ok) {
      report.parse_error_classification = parsed.classification;
      throw new Error(parsed.error);
    }
    report.assistant_result = validateSemanticDelta(parsed.value);
    if (report.response_model !== MODEL) report.errors.push('Returned model differs from requested model');
  } catch (error) {
    report.errors.push(safeText(error?.message ?? error));
  } finally {
    report.duration_ms = Date.now() - started;
    report.source_model_unchanged = digest(readFileSync(MODEL_PATH)) === preflight.modelDigest;
    if (!report.source_model_unchanged) report.errors.push('Source seating-model.json changed during test');
    writeFileSync(paths.result, JSON.stringify(report, null, 2) + '\n', { flag: 'wx' });
    key = '';
  }
  if (report.assistant_result) {
    const evaluation = spawnSync(process.execPath, [path.join(SDK_ROOT, 'ladvi-vision-evaluator.mjs'),
      `--result=${paths.result}`, `--output=${paths.evaluation}`], { encoding: 'utf8', timeout: 30000 });
    report.evaluator = { status: evaluation.status === 0 ? 'completed' : 'failed', stdout: safeText(evaluation.stdout), stderr: safeText(evaluation.stderr) };
    writeFileSync(paths.result, JSON.stringify(report, null, 2) + '\n');
  }
  console.log(JSON.stringify({ status: report.errors.length ? 'failed' : 'success', model: report.model,
    usage: report.usage, reported_api_cost_usd: report.reported_api_cost_usd,
    estimated_api_cost_usd: report.estimated_api_cost_usd, duration_ms: report.duration_ms,
    evaluator: report.evaluator?.status ?? 'not_run', errors: report.errors }));
  process.exitCode = report.errors.length ? 1 : 0;
}

if (process.argv.includes('--preflight')) {
  const confirmation = checkConfirmation();
  const workspace = readWorkspaceConfirmation();
  const preflight = loadPreflight();
  console.log(JSON.stringify({ status: 'LOCAL_LADVI_PREFLIGHT_PASSED', api_calls: 0,
    model: MODEL, thinking: MODEL_PROFILE.thinking.type, max_tokens: MAX_TOKENS, max_budget_usd: MAX_BUDGET_USD,
    workspace_name: workspace.workspace_name, workspace_id: workspace.workspace_id,
    console_workspace: confirmation.workspace, input_sha256: preflight.manifest.sha256,
    reference_in_input_package: false }));
} else if (process.argv.includes('--self-test')) {
  const preflight = loadPreflight();
  console.log(JSON.stringify({ status: 'LOCAL_VISION_RUNNER_CHECK_PASSED', model: MODEL, thinking: MODEL_PROFILE.thinking.type, max_tokens: MAX_TOKENS,
    max_budget_usd: MAX_BUDGET_USD, max_requests: 1, estimated_input_tokens: preflight.estimatedInputTokens,
    estimated_cost_usd: preflight.estimatedCost, api_calls: 0, key_source: 'secure stdin only',
    output_schema: 'json_schema', reference_in_input_package: false }));
} else if (process.argv.includes('--run-once')) {
  await runOnce();
} else throw new Error('Use --self-test or the approved secure PowerShell launcher');

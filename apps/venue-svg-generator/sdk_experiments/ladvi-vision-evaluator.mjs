import { readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = path.dirname(fileURLToPath(import.meta.url));
const PROJECT = path.resolve(ROOT, '..');
const MODEL_PATH = path.join(PROJECT, 'benchmark_v1', 'phase5a_ladvi', 'seating-model.json');
const BASELINE_PATH = path.join(PROJECT, 'benchmark_v1', 'phase5a_ladvi', 'reference-evaluation.json');
const REFERENCE_PATH = path.join(PROJECT, 'ChatExport_2026-10-11', 'files', 'сultural_centre.svg');
const DEFAULT_RESULT_PATH = path.join(ROOT, 'results', 'ladvi-vision.json');
const DEFAULT_OUTPUT_PATH = path.join(ROOT, 'results', 'ladvi-benchmark-evaluation.json');
const SVG = '{http://www.w3.org/2000/svg}';

function argValue(prefix, fallback) {
  const value = process.argv.find(item => item.startsWith(prefix));
  return value ? value.slice(prefix.length) : fallback;
}

function readJson(file) { return JSON.parse(readFileSync(file, 'utf8')); }
function esc(value) { return String(value).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&apos;'}[c])); }

function referenceNumbers() {
  const svg = readFileSync(REFERENCE_PATH, 'utf8');
  const groups = [];
  // This evaluator parses only direct Parter groups. It never runs in the
  // source-only request and its output is not supplied back to Claude.
  const groupPattern = /<g\b([^>]*inkscape:label="#Parter"[^>]*)>([\s\S]*?)<\/g>/g;
  for (const match of svg.matchAll(groupPattern)) {
    const block = match[2];
    const title = block.match(/<title[^>]*>([\s\S]*?)<\/title>/)?.[1]?.trim() ?? null;
    const seats = [];
    for (const circle of block.matchAll(/<circle\b([^>]*)>([\s\S]*?)<\/circle>/g)) {
      const attrs = circle[1];
      const cx = Number(attrs.match(/\bcx="([^\"]+)"/)?.[1]);
      const number = circle[2].match(/<title[^>]*>([\s\S]*?)<\/title>/)?.[1]?.trim() ?? null;
      if (Number.isFinite(cx)) seats.push({ cx, number });
    }
    if (title && seats.length) groups.push({ title, seats: seats.sort((a, b) => a.cx - b.cx) });
  }
  if (groups.length !== 18 || groups.some(g => g.seats.length !== 24)) throw new Error('Reference SVG Parter fingerprint is not 18x24');
  return new Map(groups.map(g => [g.title, g.seats.map(s => s.number)]));
}

function evaluate(resultPath, outputPath) {
  const ai = readJson(resultPath);
  const model = readJson(MODEL_PATH);
  const baseline = readJson(BASELINE_PATH);
  const semantic = ai.assistant_result ?? {};
  const delta = semantic.semantic_delta ?? semantic;
  const rows = Array.isArray(semantic.row_observations) ? semantic.row_observations
    : (Array.isArray(delta.row_labels) ? delta.row_labels.map(item => ({
      geometric_row_id: item.geometric_row_id, observed_text: item.observed_label,
      confidence: item.confidence, evidence: item.evidence })) : []);
  const rowTruth = new Map(baseline.grouping.rows.map(r => [r.geometric_row_id, String(r.reference_row_label)]));
  const rowResults = rows.map(row => ({
    geometric_row_id: row.geometric_row_id ?? null,
    observed_text: row.observed_text ?? null,
    expected_text: rowTruth.get(row.geometric_row_id) ?? null,
    status: rowTruth.get(row.geometric_row_id) == null || row.observed_text == null ? 'unknown'
      : String(row.observed_text).trim() === rowTruth.get(row.geometric_row_id) ? 'match' : 'mismatch',
  }));
  const seatTruth = referenceNumbers();
  const seatByKey = new Map(model.seats.map(s => [s.seat_key, s]));
  const seatResults = [];
  const seatObservations = Array.isArray(semantic.seat_observations) ? semantic.seat_observations
    : (Array.isArray(delta.seat_numbers) ? delta.seat_numbers.map(item => ({
      seat_key: item.seat_key, observed_text: item.observed_number,
      confidence: item.confidence, evidence: item.evidence })) : []);
  for (const item of seatObservations) {
    const source = seatByKey.get(item.seat_key);
    const rowLabel = source ? rowTruth.get(source.geometric_row_id) : null;
    const col = source ? Number(source.spatial_order_left_to_right) - 1 : -1;
    const expected = rowLabel && seatTruth.has(rowLabel) && col >= 0 ? seatTruth.get(rowLabel)[col] : null;
    seatResults.push({ seat_key: item.seat_key ?? null, observed_text: item.observed_text ?? null,
      expected_text: expected, status: expected == null || item.observed_text == null ? 'unknown'
        : String(item.observed_text).trim() === expected ? 'match' : 'mismatch' });
  }
  const count = (items, status) => items.filter(x => x.status === status).length;
  const output = {
    schema_version: 'ladvi-vision-evaluation-1', case_id: ai.case_id ?? 'ladvi-raster-baseline',
    source_model_sha256: ai.source_model_sha256 ?? null, reference_sha256: ai.reference_sha256 ?? null,
    baseline: { detected_seats: model.seats.length, geometric_rows: model.geometric_rows.length,
      row_counts: model.geometric_rows.map(r => r.seat_count), local_number_accuracy: 'unknown' },
    semantic: {
      row_labels: { submitted: rowResults.length, match: count(rowResults, 'match'), mismatch: count(rowResults, 'mismatch'),
        unknown: 18 - count(rowResults, 'match') - count(rowResults, 'mismatch'), results: rowResults },
      seat_numbers: { submitted: seatResults.length, match: count(seatResults, 'match'), mismatch: count(seatResults, 'mismatch'),
        unknown: 432 - count(seatResults, 'match') - count(seatResults, 'mismatch'), results: seatResults,
        note: 'Only labels explicitly returned by Claude are scored; absent labels remain unknown.' },
      sector_labels: semantic.observations?.sector_labels_visible
        ?? (Array.isArray(delta.sector_labels) && delta.sector_labels.length ? 'visible' : 'unknown'),
    },
    api: { model: ai.model ?? null, usage: ai.usage ?? null, reported_api_cost_usd: ai.reported_api_cost_usd ?? null,
      estimated_api_cost_usd: ai.estimated_api_cost_usd ?? null, duration_ms: ai.duration_ms ?? null },
    status: ai.assistant_result ? 'evaluated_with_unknowns' : 'no_structured_result',
  };
  writeFileSync(outputPath, JSON.stringify(output, null, 2) + '\n', 'utf8');
  return output;
}

if (process.argv.includes('--self-test')) {
  const baseline = readJson(BASELINE_PATH);
  if (baseline.grouping?.source_geometric_rows !== 18) throw new Error('Unexpected baseline rows');
  if (referenceNumbers().size !== 18) throw new Error('Unexpected reference rows');
  console.log(JSON.stringify({ status: 'LOCAL_EVALUATOR_CHECK_PASSED', reference_groups: 18, reference_seats: 432 }));
} else {
  const result = evaluate(argValue('--result=', DEFAULT_RESULT_PATH), argValue('--output=', DEFAULT_OUTPUT_PATH));
  console.log(JSON.stringify({ status: result.status, row_labels: result.semantic.row_labels, seat_numbers: result.semantic.seat_numbers }));
}

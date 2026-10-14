import assert from 'node:assert/strict';
import test from 'node:test';
import { estimateApiCostUsd, parseAssistantText } from './ladvi-response-utils.mjs';

test('classifies max_tokens unterminated JSON without retrying', () => {
  const truncated = '{"semantic_delta":{"row_labels":[{"geometric_row_id":"LADVI-G01","observed_label":"Parterre';
  const parsed = parseAssistantText(truncated, 'max_tokens');
  assert.equal(parsed.ok, false);
  assert.equal(parsed.classification, 'truncated_or_incomplete_json');
  assert.match(parsed.error, /Unterminated|Unexpected end/i);
});

test('accepts the compact semantic delta and preserves unknowns', () => {
  const parsed = parseAssistantText(JSON.stringify({
    semantic_delta: { row_labels: [], seat_numbers: [], sector_labels: [] },
    unknowns: ['seat numbering unreadable'],
  }), 'end_turn');
  assert.equal(parsed.ok, true);
  assert.deepEqual(parsed.value.unknowns, ['seat numbering unreadable']);
});

test('estimates the observed first-run cost from real usage', () => {
  assert.equal(estimateApiCostUsd({ input_tokens: 17322, output_tokens: 1800 }), 0.052644);
  assert.equal(estimateApiCostUsd(null), null);
});

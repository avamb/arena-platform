import assert from 'node:assert/strict';
import test from 'node:test';
import { API_OUTPUT_SCHEMA, OUTPUT_SCHEMA, SEMANTIC_LIMITS, validateSemanticDelta } from './ladvi-schema.mjs';

const UNSUPPORTED = new Set(['minimum', 'maximum', 'minLength', 'maxLength', 'maxItems', 'minItems', 'pattern', 'uniqueItems']);

function walkSchema(node) {
  if (!node || typeof node !== 'object' || Array.isArray(node)) return;
  for (const key of Object.keys(node)) assert.equal(UNSUPPORTED.has(key), false, `unsupported schema key: ${key}`);
  if (node.type === 'object') assert.equal(node.additionalProperties, false);
  for (const child of Object.values(node.properties ?? {})) walkSchema(child);
  walkSchema(node.items);
}

test('API-facing transformed schema contains only compatible constraints', () => {
  walkSchema(API_OUTPUT_SCHEMA);
  walkSchema(OUTPUT_SCHEMA);
  assert.equal(API_OUTPUT_SCHEMA.properties.semantic_delta.properties.seat_numbers.items.additionalProperties, false);
});

test('local post-validation retains semantic array limits', () => {
  const base = { semantic_delta: { row_labels: [], seat_numbers: [], sector_labels: [] }, unknowns: [] };
  validateSemanticDelta(base);
  const tooManySeats = { ...base, semantic_delta: { ...base.semantic_delta, seat_numbers: Array(SEMANTIC_LIMITS.seats + 1).fill({}) } };
  assert.throws(() => validateSemanticDelta(tooManySeats), /seat_numbers exceeds local limit 64/);
  const tooManyRows = { ...base, semantic_delta: { ...base.semantic_delta, row_labels: Array(SEMANTIC_LIMITS.rows + 1).fill({}) } };
  assert.throws(() => validateSemanticDelta(tooManyRows), /row_labels exceeds local limit 18/);
});

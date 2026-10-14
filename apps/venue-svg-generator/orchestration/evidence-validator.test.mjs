import assert from 'node:assert/strict';
import test from 'node:test';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { validateEvidenceReference, validateEvidenceList } from './evidence-validator.mjs';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const approved = ['PRODUCT_SPEC.md', 'ROADMAP.md'];

test('accepts exact approved paths and valid line references', () => {
  for (const value of ['PRODUCT_SPEC.md', 'PRODUCT_SPEC.md:5', 'PRODUCT_SPEC.md:5-10', 'ROADMAP.md:14 — regression evidence']) {
    assert.equal(validateEvidenceReference(value, approved, { root: ROOT }).valid, true, value);
  }
});

test('rejects traversal, unknown files, invalid and reverse ranges', () => {
  for (const value of ['../secret.md', 'unknown.md:5', 'PRODUCT_SPEC.md:0', 'PRODUCT_SPEC.md:5-0', 'PRODUCT_SPEC.md:10-5', 'PRODUCT_SPEC.md:5-9999', 'PRODUCT_SPEC.md.evil:5']) {
    assert.equal(validateEvidenceReference(value, approved, { root: ROOT }).valid, false, value);
  }
});

test('does not accept a prefix collision and validates every item', () => {
  assert.equal(validateEvidenceReference('PRODUCT_SPEC.md.bak:5', approved, { root: ROOT }).valid, false);
  const result = validateEvidenceList(['PRODUCT_SPEC.md:5', 'ROADMAP.md:14-21'], approved, { root: ROOT });
  assert.equal(result.valid, true);
  assert.equal(result.references.length, 2);
});

import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { classifySDKResult, correctRecordedReport } from './reporting.mjs';

test('reclassifies the actual first paid result without changing evidence or latch', () => {
  const reportPath = new URL('./results/verification.json', import.meta.url);
  const latchPath = new URL('./results/attempt.json', import.meta.url);
  const originalBytes = readFileSync(reportPath);
  const latchHash = createHash('sha256').update(readFileSync(latchPath)).digest('hex');
  const original = JSON.parse(originalBytes);
  assert.equal(original.result_subtype, 'success');
  assert.equal(original.terminal_reason, 'completed');
  const corrected = correctRecordedReport(original);
  assert.equal(corrected.sdk_authentication_succeeded, true);
  assert.deepEqual(corrected.sdk_errors, []);
  for (const field of ['result_subtype', 'terminal_reason', 'models_used', 'file_read',
    'summary', 'token_usage', 'model_usage', 'reported_api_cost_usd', 'audit',
    'api_key_source', 'num_turns', 'approved_file_unchanged']) {
    assert.deepEqual(corrected[field], original[field], field);
  }
  assert.deepEqual(readFileSync(reportPath), originalBytes);
  assert.equal(createHash('sha256').update(readFileSync(latchPath)).digest('hex'), latchHash);
});

test('completed and legacy success need observed model evidence', () => {
  for (const reason of ['completed', 'end_turn', undefined, null]) {
    const result = { subtype: 'success', terminal_reason: reason };
    assert.equal(classifySDKResult(result, ['observed-model']).authenticationSucceeded, true);
    assert.equal(classifySDKResult(result, []).authenticationSucceeded, null);
  }
});

test('API errors, budget/turn limits, explicit errors and unknown reasons remain failures', () => {
  for (const result of [
    { subtype: 'success', terminal_reason: 'api_error' },
    { subtype: 'success', terminal_reason: 'completed', is_error: true },
    { subtype: 'success', terminal_reason: 'completed', errors: ['real error'] },
    { subtype: 'success', terminal_reason: 'future_unknown_reason' },
    { subtype: 'error_max_budget_usd', terminal_reason: 'budget_exhausted' },
    { subtype: 'error_max_turns', terminal_reason: 'max_turns' },
    { subtype: 'error_during_execution', terminal_reason: 'completed' },
  ]) {
    const c = classifySDKResult(result, ['observed-model']);
    assert.equal(c.completedSuccessfully, false);
    assert.equal(c.authenticationSucceeded, false);
    assert.ok(c.diagnostic);
  }
});

test('offline correction preserves real errors and is idempotent', () => {
  const original = JSON.parse(readFileSync(new URL('./results/verification.json', import.meta.url)));
  const withRealError = { ...original, sdk_errors: [...original.sdk_errors, 'A real policy failure'] };
  const corrected = correctRecordedReport(withRealError);
  assert.deepEqual(corrected.sdk_errors, ['A real policy failure']);
  assert.deepEqual(correctRecordedReport(corrected), corrected);
  const failed = correctRecordedReport({ ...original, terminal_reason: 'api_error', sdk_errors: ['Actual API failure'] });
  assert.equal(failed.sdk_authentication_succeeded, false);
  assert.ok(failed.sdk_errors.includes('Actual API failure'));
});

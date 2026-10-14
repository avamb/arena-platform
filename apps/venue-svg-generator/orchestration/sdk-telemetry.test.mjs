import assert from 'node:assert/strict';
import test from 'node:test';
import {
  billingRecord,
  appendSafeDiagnostic,
  captureAssistantDiagnostic,
  captureAssistantDiagnostics,
  captureTerminalDiagnostic,
  classifyTerminal,
} from './sdk-telemetry.mjs';

test('captures synthetic terminal failures without secrets', () => {
  const terminal = captureTerminalDiagnostic({
    type: 'result', subtype: 'error_during_execution', is_error: true,
    api_error_status: 401, stop_reason: null, terminal_reason: 'error',
    startup_failure_reason: 'Bearer TEST_SECRET_VALUE', errors: ['TEST_SECRET_VALUE'],
    total_cost_usd: 0, usage: { input_tokens: 0 }, result: '',
  }, 'TEST_SECRET_VALUE');
  assert.equal(terminal.api_error_status, 401);
  assert.equal(terminal.result_present, true);
  assert.equal(terminal.errors[0].includes('sk-ant-'), false);
  assert.equal(terminal.terminal_error_text, '');
  const classified = classifyTerminal({ terminal, assistantErrors: [] });
  assert.equal(classified.kind, 'sdk_startup_failure');
  assert.equal(classified.success, false);
});
test('persists bounded HTTP 400 result text without secrets', () => {
  const terminal = captureTerminalDiagnostic({
    type: 'result', subtype: 'success', is_error: true, api_error_status: 400,
    terminal_reason: 'api_error', result: '400 invalid_request: workspace header missing; Authorization: Bearer TEST_SECRET_VALUE',
    usage: { input_tokens: 0 }, total_cost_usd: 0,
  }, 'TEST_SECRET_VALUE');
  assert.equal(terminal.terminal_error_text, '400 invalid_request: workspace header missing; Authorization: Bearer [REDACTED]');
  assert.equal(classifyTerminal({ terminal }).kind, 'api_failure');
  const long = [];
  appendSafeDiagnostic(long, 'x'.repeat(10000));
  assert.ok(long.join('').length <= 4001);
  const stderr = [];
  appendSafeDiagnostic(stderr, 'Authorization: Bearer TEST_SECRET_VALUE; detail', 'TEST_SECRET_VALUE');
  assert.equal(stderr[0], 'Authorization: Bearer [REDACTED]; detail');
});

test('is_error makes subtype success a failure', () => {
  const terminal = captureTerminalDiagnostic({ type: 'result', subtype: 'success', is_error: true, result: 'request rejected' });
  const classification = classifyTerminal({ terminal, structuredValid: true });
  assert.equal(classification.success, false);
  assert.equal(classification.kind, 'sdk_execution_failure');
});

test('redacts assistant diagnostics before persistence', () => {
  const error = captureAssistantDiagnostic({ type: 'assistant', error: 'Bearer TEST_SECRET_VALUE' });
  assert.equal(error, 'Bearer [REDACTED]');
  assert.deepEqual(captureAssistantDiagnostics({ type: 'assistant', message: { errors: ['TEST_SECRET_VALUE', 'invalid_request'] } }, 'TEST_SECRET_VALUE'), ['[REDACTED]', 'invalid_request']);
});

test('classifies assistant authentication errors before JSON parsing', () => {
  const error = captureAssistantDiagnostic({ type: 'assistant', error: 'authentication_failed' });
  const classified = classifyTerminal({ terminal: null, assistantErrors: [error] });
  assert.equal(classified.kind, 'authentication_failure');
});

test('classifies model and generic SDK failures without usage', () => {
  const model = classifyTerminal({
    terminal: captureTerminalDiagnostic({ type: 'result', subtype: 'error_during_execution', is_error: true, errors: [], result: null }),
    assistantErrors: ['model_not_found'],
  });
  assert.equal(model.kind, 'model_access_failure');
  const sdk = classifyTerminal({
    terminal: captureTerminalDiagnostic({ type: 'result', subtype: 'error_max_turns', is_error: true, errors: [], result: null }),
  });
  assert.equal(sdk.kind, 'sdk_execution_limit');
  assert.equal(sdk.success, false);
});

test('classifies API status separately from authentication', () => {
  const terminal = captureTerminalDiagnostic({ type: 'result', subtype: 'error_during_execution', is_error: true, api_error_status: 503, result: null });
  assert.equal(classifyTerminal({ terminal }).kind, 'api_failure');
});

test('keeps billing unknown when SDK reports no usage/cost', () => {
  const billing = billingRecord({ mode: 'sdk', reportedCost: null, usage: null });
  assert.deepEqual(billing, {
    status: 'UNKNOWN', sdk_reported_cost_usd: null,
    usage_present: false, actual_billing: 'UNKNOWN',
  });
});

test('classifies invalid JSON only after a successful terminal result', () => {
  const terminal = captureTerminalDiagnostic({
    type: 'result', subtype: 'success', is_error: false,
    total_cost_usd: 0.01, usage: { input_tokens: 1 }, result: '{broken',
  });
  const classified = classifyTerminal({ terminal, structuredValid: false });
  assert.equal(classified.kind, 'invalid_structured_response');
  assert.equal(classified.success, false);
});

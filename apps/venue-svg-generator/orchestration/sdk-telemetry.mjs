const AUTH_ERRORS = new Set([
  'authentication_failed', 'oauth_org_not_allowed', 'account_on_hold',
  'verification_required', 'billing_error', 'cloud_credential_error',
]);
const API_ERRORS = new Set([
  'invalid_request', 'rate_limit', 'overloaded', 'server_error', 'unknown',
]);
const MODEL_ERRORS = new Set(['model_not_found']);
const LIMIT_SUBTYPES = new Set([
  'error_max_turns', 'error_max_budget_usd', 'error_max_structured_output_retries',
]);

export function safeDiagnosticText(value, max = 600, secrets = []) {
  if (value === null || value === undefined) return null;
  let text;
  try { text = typeof value === 'string' ? value : JSON.stringify(value); } catch { text = '[UNSERIALIZABLE_DIAGNOSTIC]'; }
  text = String(text)
    .replace(/Authorization\s*[:=]\s*Bearer\s+[^\s,;]+/gi, 'Authorization: Bearer [REDACTED]')
    .replace(/(x-api-key|anthropic-api-key|api[_-]?key|token|secret|credential)\s*[:=]\s*["']?[^\s,"'};]+/gi, '$1=[REDACTED]')
    .replace(/sk-ant-[A-Za-z0-9_-]+/gi, '[REDACTED]')
    .replace(/Bearer\s+[^\s,;]+/gi, 'Bearer [REDACTED]')
    .replace(/ANTHROPIC_API_KEY\s*[=:]\s*\S+/gi, 'ANTHROPIC_API_KEY=[REDACTED]');
  for (const secret of secrets) {
    if (typeof secret === 'string' && secret.length > 0) text = text.split(secret).join('[REDACTED]');
  }
  return text.length > max ? `${text.slice(0, max)}…` : text;
}
function safeNumber(value) {
  return Number.isFinite(value) ? value : null;
}

export function captureAssistantDiagnostics(message, secret = null) {
  const values = [];
  if (typeof message?.error === 'string') values.push(message.error);
  if (typeof message?.message?.error === 'string') values.push(message.message.error);
  if (Array.isArray(message?.message?.errors)) values.push(...message.message.errors);
  return values.map(value => safeDiagnosticText(value, 600, [secret])).filter(Boolean);
}

export function captureAssistantDiagnostic(message, secret = null) {
  return captureAssistantDiagnostics(message, secret)[0] ?? null;
}

export function captureTerminalDiagnostic(message, secret = null) {
  if (!message || message.type !== 'result') return null;
  const terminalErrorText = message.is_error === true
    ? safeDiagnosticText(message.result, 1200, [secret])
    : null;
  return {
    subtype: safeDiagnosticText(message.subtype, 120),
    is_error: typeof message.is_error === 'boolean' ? message.is_error : null,
    api_error_status: Number.isInteger(message.api_error_status) ? message.api_error_status : null,
    stop_reason: safeDiagnosticText(message.stop_reason, 120),
    terminal_reason: safeDiagnosticText(message.terminal_reason, 120),
    startup_failure_reason: safeDiagnosticText(message.startup_failure_reason, 600),
    errors: Array.isArray(message.errors) ? message.errors.map(error => safeDiagnosticText(error, 600, [secret])).filter(Boolean) : [],
    terminal_error_text: terminalErrorText,
    num_turns: Number.isInteger(message.num_turns) ? message.num_turns : null,
    duration_ms: safeNumber(message.duration_ms),
    duration_api_ms: safeNumber(message.duration_api_ms),
    result_present: typeof message.result === 'string',
    usage_present: message.usage !== undefined && message.usage !== null,
    model_usage_present: message.modelUsage !== undefined && message.modelUsage !== null,
    sdk_reported_cost_usd: safeNumber(message.total_cost_usd),
  };
}

export function classifyTerminal({ terminal, assistantErrors = [], timedOut = false, structuredValid = null }) {
  const assistant = assistantErrors.filter(Boolean);
  if (timedOut) return { kind: 'timeout', success: false, message: 'SDK query timeout' };
  if (assistant.some(error => AUTH_ERRORS.has(error))) {
    return { kind: 'authentication_failure', success: false, message: 'SDK authentication failure' };
  }
  if (assistant.some(error => MODEL_ERRORS.has(error))) {
    return { kind: 'model_access_failure', success: false, message: 'Requested model is unavailable or inaccessible' };
  }
  if (assistant.some(error => API_ERRORS.has(error))) {
    return { kind: 'api_failure', success: false, message: 'SDK/API request failed' };
  }
  if (!terminal) return { kind: 'sdk_terminal_missing', success: false, message: 'SDK terminal result was not received' };
  if (terminal.startup_failure_reason) {
    return { kind: 'sdk_startup_failure', success: false, message: 'SDK startup failure' };
  }
  if (terminal.api_error_status !== null) {
    return { kind: 'api_failure', success: false, message: 'SDK/API request failed' };
  }
  if (terminal.subtype && LIMIT_SUBTYPES.has(terminal.subtype)) {
    return { kind: 'sdk_execution_limit', success: false, message: `SDK stopped with ${terminal.subtype}` };
  }
  if (terminal.is_error === true) {
    return { kind: 'sdk_execution_failure', success: false, message: 'SDK execution failure' };
  }
  if (terminal.subtype && terminal.subtype !== 'success') {
    return { kind: 'sdk_execution_failure', success: false, message: `SDK stopped with ${terminal.subtype}` };
  }
  if (structuredValid === false) {
    return { kind: 'invalid_structured_response', success: false, message: 'Terminal success did not contain valid JSON' };
  }
  if (structuredValid === true) return { kind: 'success', success: true, message: 'SDK completed successfully' };
  return { kind: 'terminal_success_unparsed', success: false, message: 'Terminal success requires structured-result validation' };
}

export function billingRecord({ mode, reportedCost, usage }) {
  if (mode === 'mock') {
    return { status: 'NOT_APPLICABLE', sdk_reported_cost_usd: 0, actual_billing: 'NOT_APPLICABLE' };
  }
  return {
    status: 'UNKNOWN',
    sdk_reported_cost_usd: Number.isFinite(reportedCost) ? reportedCost : null,
    usage_present: usage !== null && usage !== undefined,
    actual_billing: 'UNKNOWN',
  };
}

export function appendSafeDiagnostic(target, value, secret = null, maxTotal = 4000) {
  if (!Array.isArray(target) || target.join('\n').length >= maxTotal) return;
  const remaining = maxTotal - target.join('\n').length;
  const safe = safeDiagnosticText(value, remaining, [secret]);
  if (safe) target.push(safe);
}

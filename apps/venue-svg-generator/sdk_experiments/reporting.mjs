// Pure reporting helpers: no SDK import, process launch, filesystem access or API calls.
// SDK 0.3.296 TerminalReason uses 'completed'. 'end_turn' is a legacy/API
// stop reason accepted for compatibility; absent reasons occur in older producers.
export function classifySDKResult(result, observedModels = []) {
  const reason = result.terminal_reason;
  const completedSuccessfully = result.subtype === 'success'
    && result.is_error !== true
    && (reason == null || reason === 'completed' || reason === 'end_turn')
    && !(result.errors?.length);
  const modelObserved = observedModels.some(model => typeof model === 'string' && model.length > 0);
  return {
    completedSuccessfully,
    authenticationSucceeded: completedSuccessfully ? (modelObserved ? true : null) : false,
    diagnostic: completedSuccessfully ? null
      : `SDK result: ${result.subtype}; ${reason ?? 'no terminal reason'}`,
  };
}

// The first runner stored selected result fields, not the full raw SDK message.
// Reclassify only that recorded evidence; never invent missing raw is_error/errors.
export function correctRecordedReport(original) {
  const result = {
    subtype: original.result_subtype,
    terminal_reason: original.terminal_reason,
  };
  if (Object.hasOwn(original, 'is_error')) result.is_error = original.is_error;
  const models = [...new Set([
    ...(original.models_used ?? []), ...Object.keys(original.model_usage ?? {}),
  ])];
  const classification = classifySDKResult(result, models);
  const falseDiagnostic = `SDK result: ${result.subtype}; ${result.terminal_reason ?? 'no terminal reason'}`;
  const errors = (original.sdk_errors ?? []).filter(error =>
    !(classification.completedSuccessfully && error === falseDiagnostic));
  if (classification.diagnostic && !errors.includes(classification.diagnostic)) errors.push(classification.diagnostic);
  return {
    ...original,
    sdk_authentication_succeeded: classification.authenticationSucceeded,
    sdk_errors: errors,
    reporting_correction: {
      source: 'results/verification.json',
      method: 'Offline reclassification of recorded result fields and observed models',
      limitation: 'The original full SDK result message was not retained',
      new_api_calls: 0,
    },
  };
}

export function parseAssistantText(text, stopReason = null) {
  try {
    return { ok: true, value: JSON.parse(text), error: null, classification: null };
  } catch (error) {
    const message = String(error?.message ?? error);
    const truncated = stopReason === 'max_tokens' || /Unexpected end|Unterminated string|end of JSON/i.test(message);
    return {
      ok: false,
      value: null,
      error: message,
      classification: truncated ? 'truncated_or_incomplete_json' : 'invalid_json',
    };
  }
}

export function estimateApiCostUsd(usage) {
  if (!usage) return null;
  const inputTokens = Number(usage?.input_tokens ?? 0);
  const outputTokens = Number(usage?.output_tokens ?? 0);
  return Number((inputTokens * 2 / 1e6 + outputTokens * 10 / 1e6).toFixed(6));
}

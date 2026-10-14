// API-facing schema intentionally uses only the conservative Structured Outputs subset.
// Count limits are enforced locally after parsing and repeated in the prompt.
export const OUTPUT_SCHEMA = {
  type: 'object',
  additionalProperties: false,
  properties: {
    semantic_delta: {
      type: 'object',
      additionalProperties: false,
      properties: {
        row_labels: {
          type: 'array',
          items: {
            type: 'object',
            additionalProperties: false,
            properties: {
              geometric_row_id: { type: 'string' },
              observed_label: { type: ['string', 'null'] },
              confidence: { type: ['number', 'null'] },
              evidence: { type: 'string' },
            },
            required: ['geometric_row_id', 'observed_label', 'confidence', 'evidence'],
          },
        },
        seat_numbers: {
          type: 'array',
          items: {
            type: 'object',
            additionalProperties: false,
            properties: {
              seat_key: { type: 'string' },
              observed_number: { type: ['string', 'null'] },
              confidence: { type: ['number', 'null'] },
              evidence: { type: 'string' },
            },
            required: ['seat_key', 'observed_number', 'confidence', 'evidence'],
          },
        },
        sector_labels: {
          type: 'array',
          items: {
            type: 'object',
            additionalProperties: false,
            properties: {
              observed_label: { type: ['string', 'null'] },
              confidence: { type: ['number', 'null'] },
              evidence: { type: 'string' },
            },
            required: ['observed_label', 'confidence', 'evidence'],
          },
        },
      },
      required: ['row_labels', 'seat_numbers', 'sector_labels'],
    },
    unknowns: { type: 'array', items: { type: 'string' } },
  },
  required: ['semantic_delta', 'unknowns'],
};

// The installed SDK contains a transformer internally, but does not export
// `@anthropic-ai/sdk/helpers` as a public package subpath. Use the already
// conservative schema directly rather than importing an unsupported internal path.
export const API_OUTPUT_SCHEMA = structuredClone(OUTPUT_SCHEMA);
export const OUTPUT_FORMAT = { type: 'json_schema', schema: API_OUTPUT_SCHEMA };
export const SEMANTIC_LIMITS = { rows: 18, seats: 64, sectors: 12, unknowns: 12 };

export function validateSemanticDelta(value) {
  const delta = value?.semantic_delta;
  if (!delta || typeof delta !== 'object' || Array.isArray(delta)) throw new Error('semantic_delta must be an object');
  const arrays = [
    ['row_labels', SEMANTIC_LIMITS.rows],
    ['seat_numbers', SEMANTIC_LIMITS.seats],
    ['sector_labels', SEMANTIC_LIMITS.sectors],
  ];
  for (const [name, limit] of arrays) {
    if (!Array.isArray(delta[name])) throw new Error(`semantic_delta.${name} must be an array`);
    if (delta[name].length > limit) throw new Error(`semantic_delta.${name} exceeds local limit ${limit}`);
  }
  if (!Array.isArray(value.unknowns)) throw new Error('unknowns must be an array');
  if (value.unknowns.length > SEMANTIC_LIMITS.unknowns) throw new Error(`unknowns exceeds local limit ${SEMANTIC_LIMITS.unknowns}`);
  return value;
}

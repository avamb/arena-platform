import assert from 'node:assert/strict';
import http from 'node:http';
import { once } from 'node:events';
import Anthropic from '@anthropic-ai/sdk';
import { API_OUTPUT_SCHEMA, OUTPUT_FORMAT } from './ladvi-schema.mjs';

const MOCK_WORKSPACE_ID = 'wrkspc_mock_local_test';
let receivedBody = null;

const server = http.createServer((req, res) => {
  const chunks = [];
  req.on('data', chunk => chunks.push(chunk));
  req.on('end', () => {
    receivedBody = JSON.parse(Buffer.concat(chunks).toString('utf8'));
    const seen = req.headers['anthropic-workspace-id'];
    if (seen !== MOCK_WORKSPACE_ID) {
      res.writeHead(400, { 'content-type': 'application/json' });
      res.end(JSON.stringify({ error: 'workspace header missing or incorrect' }));
      return;
    }
    res.writeHead(200, { 'content-type': 'application/json' });
    res.end(JSON.stringify({
      id: 'msg_local_mock', type: 'message', role: 'assistant', model: 'claude-sonnet-5-5',
      content: [{ type: 'text', text: '{}' }], stop_reason: 'end_turn', stop_sequence: null,
      usage: { input_tokens: 1, output_tokens: 1 },
    }));
  });
});

server.listen(0, '127.0.0.1');
await once(server, 'listening');
const address = server.address();
const client = new Anthropic({ apiKey: 'local-mock-key', baseURL: `http://127.0.0.1:${address.port}`, maxRetries: 0 });
try {
  const response = await client.messages.create({
    model: 'claude-sonnet-5-5', max_tokens: 1800, workspace_id: MOCK_WORKSPACE_ID,
    thinking: { type: 'between_tools' },
    output_config: { format: OUTPUT_FORMAT },
    messages: [{ role: 'user', content: 'local mock only' }],
  });
  assert.equal(response.id, 'msg_local_mock');
  assert.equal(receivedBody.output_config.format.type, 'json_schema');
  assert.equal(receivedBody.output_config.format.schema.properties.semantic_delta.additionalProperties, false);
  assert.equal(Object.hasOwn(receivedBody.output_config.format.schema.semantic_delta?.seat_numbers ?? {}, 'maxItems'), false);
  assert.equal(API_OUTPUT_SCHEMA.properties.semantic_delta.additionalProperties, false);
  assert.equal(receivedBody.thinking.type, 'between_tools');
  assert.equal(receivedBody.max_tokens, 1800);
  console.log(JSON.stringify({ status: 'LOCAL_WORKSPACE_AND_STRUCTURED_OUTPUT_CHECK_PASSED', api_calls: 0, header: 'anthropic-workspace-id', structured_outputs: true, thinking: 'between_tools', max_tokens: 1800, mock_only: true }));
} finally {
  server.close();
}

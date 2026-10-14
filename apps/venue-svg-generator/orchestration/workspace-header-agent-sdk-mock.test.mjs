import assert from 'node:assert/strict';
import http from 'node:http';
import { once } from 'node:events';
import path from 'node:path';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import { safeDiagnosticText } from './sdk-telemetry.mjs';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const RUNTIME_ROOT = path.resolve(process.env.VENUE_SVG_RUNTIME_ROOT || path.join(path.dirname(ROOT), 'Venue_SVG_Generator'));
const requireSdk = createRequire(path.join(RUNTIME_ROOT, 'sdk_experiments', 'package.json'));
const { query } = requireSdk('@anthropic-ai/claude-agent-sdk');
const cli = requireSdk.resolve('@anthropic-ai/claude-agent-sdk-win32-x64/claude.exe');
const workspaceId = 'wrkspc_01XadYi6gMSqYzdMR5KqiHhr';
const fakeKey = 'LOCAL_TEST_CREDENTIAL_NEVER_SENT_TO_ANTHROPIC';
const requests = [];

const server = http.createServer((req, res) => {
  const chunks = [];
  req.on('data', chunk => chunks.push(chunk));
  req.on('end', () => {
    requests.push({
      host: req.headers.host,
      path: req.url,
      workspace: req.headers['anthropic-workspace-id'],
      authorization: req.headers.authorization,
      body: Buffer.concat(chunks).toString('utf8'),
    });
    res.writeHead(200, { 'content-type': 'text/event-stream', connection: 'close', 'cache-control': 'no-cache' });
    const events = [
      ['message_start', { type: 'message_start', message: {
        id: 'msg_local_agent_sdk_mock', type: 'message', role: 'assistant', model: 'claude-sonnet-5-5',
        content: [], stop_reason: null, stop_sequence: null, usage: { input_tokens: 1, output_tokens: 0 },
      } }],
      ['content_block_start', { type: 'content_block_start', index: 0, content_block: { type: 'text', text: '' } }],
      ['content_block_delta', { type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text: 'local mock response' } }],
      ['content_block_stop', { type: 'content_block_stop', index: 0 }],
      ['message_delta', { type: 'message_delta', delta: { stop_reason: 'end_turn', stop_sequence: null }, usage: { output_tokens: 1 } }],
      ['message_stop', { type: 'message_stop' }],
    ];
    for (const [event, data] of events) res.write(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`);
    res.end();
  });
});

server.listen(0, '127.0.0.1');
await once(server, 'listening');
const address = server.address();
const env = {
  PATH: process.env.PATH, SystemRoot: process.env.SystemRoot, TEMP: process.env.TEMP, TMP: process.env.TMP,
  USERPROFILE: process.env.USERPROFILE, ANTHROPIC_API_KEY: fakeKey,
  ANTHROPIC_WORKSPACE_ID: workspaceId,
  ANTHROPIC_CUSTOM_HEADERS: `anthropic-workspace-id: ${workspaceId}`,
  ANTHROPIC_BASE_URL: `http://127.0.0.1:${address.port}`,
  CLAUDE_CONFIG_DIR: path.join(ROOT, 'orchestration', 'runtime'), CLAUDE_CODE_MAX_RETRIES: '0',
  CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: '1', DISABLE_TELEMETRY: '1', DISABLE_ERROR_REPORTING: '1',
  DISABLE_AUTOUPDATER: '1', CLAUDE_AGENT_SDK_CLIENT_APP: 'venue-svg-a5-local-mock',
};
const stderr = [];
const controller = new AbortController();
try {
  const stream = query({
    prompt: 'Return one short acknowledgement.',
    options: {
      pathToClaudeCodeExecutable: cli,
      cwd: path.join(RUNTIME_ROOT, 'sdk_experiments', 'workspace'),
      env,
      model: 'claude-sonnet-5-5',
      maxTurns: 1,
      maxBudgetUsd: 0.01,
      tools: [],
      allowedTools: [],
      disallowedTools: ['Bash', 'Write', 'Edit', 'Agent', 'Task', 'mcp__*'],
      permissionMode: 'dontAsk', permissionPrompts: 'none', settingSources: [],
      skills: [], plugins: [], agents: {}, mcpServers: {}, strictMcpConfig: true,
      persistSession: false, enableFileCheckpointing: false,
      abortController: controller,
      stderr: data => stderr.push(safeDiagnosticText(data, 600, [fakeKey])),
    },
  });
  for await (const message of stream) {
    if (message.type === 'result') break;
  }
  stream.close?.();
} finally {
  controller.abort();
  server.close();
}

assert.equal(requests.length, 1, 'Agent SDK sent an unexpected retry/request count to the local mock');
assert.ok(requests.every(request => request.host?.startsWith('127.0.0.1:')), 'Request escaped local mock');
assert.ok(requests.every(request => request.workspace === workspaceId), 'Workspace header was not forwarded');
const diagnosticText = safeDiagnosticText({ requests, stderr }, 4000, [fakeKey]);
assert.equal(diagnosticText.includes(fakeKey), false, 'API key leaked into diagnostics');
assert.equal(diagnosticText.includes('Bearer ' + fakeKey), false, 'Authorization credential leaked into diagnostics');
console.log(JSON.stringify({
  status: 'LOCAL_AGENT_SDK_WORKSPACE_HEADER_CHECK_PASSED',
  sdk_requests: requests.length,
  paths: [...new Set(requests.map(request => request.path))],
  workspace_header: workspaceId,
  external_anthropic_requests: false,
  credential_in_diagnostics: false,
}));

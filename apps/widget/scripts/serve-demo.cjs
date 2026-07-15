/**
 * Static file server for the Arena Tickets widget demo page.
 *
 * Used by Playwright E2E tests to serve:
 *   /demo/index.html        → main attribute-matrix demo
 *   /demo/a11y-keyboard.html → accessibility / keyboard test fixture
 *   /dist/arena-tickets.js  → the built widget bundle
 *
 * Run: node scripts/serve-demo.cjs
 * The dist/ folder must be built first: npm run build
 *
 * Lifecycle:
 *   Playwright starts this process as the webServer child and kills it (via
 *   SIGTERM on POSIX or TerminateProcess on Windows) when all tests complete.
 *   The shutdown() handler below destroys any keep-alive connections so
 *   server.close() resolves promptly and the process exits without delay.
 *   A 3-second forced-exit timer (unref'd so it does not itself block exit)
 *   acts as a backstop if any connection refuses to close.
 */

// @ts-check
'use strict';

const http = require('http');
const fs = require('fs');
const path = require('path');

const ROOT = path.join(__dirname, '..');
const PORT = parseInt(process.env['PORT'] ?? '4173', 10);

const MIME_TYPES = /** @type {Record<string,string>} */ ({
  '.html': 'text/html; charset=utf-8',
  '.js': 'application/javascript; charset=utf-8',
  '.mjs': 'application/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.json': 'application/json; charset=utf-8',
  '.png': 'image/png',
  '.ico': 'image/x-icon',
  '.woff2': 'font/woff2',
  '.woff': 'font/woff',
});

const server = http.createServer((req, res) => {
  let urlPath = req.url ?? '/';

  // Strip query string and fragment.
  urlPath = urlPath.split('?')[0].split('#')[0];

  // Default route → demo page.
  if (urlPath === '/') {
    urlPath = '/demo/index.html';
  }

  // Prevent path traversal.
  const absPath = path.join(ROOT, urlPath);
  if (!absPath.startsWith(ROOT + path.sep) && absPath !== ROOT) {
    res.writeHead(403, { 'Content-Type': 'text/plain' });
    res.end('403 Forbidden');
    return;
  }

  const ext = path.extname(absPath).toLowerCase();
  const contentType = MIME_TYPES[ext] ?? 'application/octet-stream';

  fs.readFile(absPath, (err, data) => {
    if (err) {
      res.writeHead(404, { 'Content-Type': 'text/plain' });
      res.end('404 Not Found: ' + urlPath);
      return;
    }
    res.writeHead(200, {
      'Content-Type': contentType,
      'Cache-Control': 'no-cache',
      'Access-Control-Allow-Origin': '*',
    });
    res.end(data);
  });
});

// ── Connection tracking for clean shutdown ────────────────────────────────────
// HTTP keep-alive connections prevent server.close() from resolving until the
// browser side closes them.  We track every socket so shutdown() can forcibly
// destroy them, allowing the process to exit promptly after tests complete.

/** @type {Set<import('net').Socket>} */
const connections = new Set();

server.on('connection', (conn) => {
  connections.add(conn);
  conn.on('close', () => connections.delete(conn));
});

/**
 * Gracefully stop the server and exit.
 *
 * Called by SIGTERM (Playwright's preferred kill on POSIX) and SIGINT
 * (Ctrl-C during local development).  Destroys open keep-alive connections
 * so server.close() resolves immediately, then calls process.exit(0).
 * An unref'd 3-second timer is the final backstop.
 */
function shutdown() {
  process.stdout.write('Arena demo server shutting down…\n');
  // Destroy keep-alive sockets so server.close() does not wait for them.
  for (const conn of connections) {
    conn.destroy();
  }
  connections.clear();
  server.close(() => {
    process.exit(0);
  });
  // Backstop: force exit after 3 s if something still holds the loop open.
  // .unref() ensures this timer does not itself prevent the natural exit.
  setTimeout(() => process.exit(0), 3000).unref();
}

process.on('SIGTERM', shutdown);
process.on('SIGINT', shutdown);

// ── Start ─────────────────────────────────────────────────────────────────────

server.listen(PORT, '127.0.0.1', () => {
  process.stdout.write(`Arena demo server listening on http://localhost:${PORT}\n`);
});

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

server.listen(PORT, '127.0.0.1', () => {
  process.stdout.write(`Arena demo server listening on http://localhost:${PORT}\n`);
});

/**
 * Real-backend proxy server for Arena Tickets widget E2E tests — WID-R3.
 *
 * Listens on port 4174.
 *
 * Routing:
 *   /v1/*  → proxied to ARENA_API_URL (default http://localhost:8080)
 *   *      → static files served from apps/widget directory
 *
 * Environment variables:
 *   PORT           Override listen port (default 4174)
 *   ARENA_API_URL  Backend base URL      (default http://localhost:8080)
 *
 * Run: node scripts/serve-demo-real.cjs
 * The dist/ folder must be built first: npm run build
 */

// @ts-check
'use strict';

const http = require('http');
const fs   = require('fs');
const path = require('path');
const url  = require('url');

const ROOT        = path.join(__dirname, '..');
const PORT        = parseInt(process.env['PORT'] ?? '4174', 10);
const BACKEND_URL = process.env['ARENA_API_URL'] ?? 'http://localhost:8080';

/** @type {Record<string, string>} */
const MIME_TYPES = {
  '.html': 'text/html; charset=utf-8',
  '.js':   'application/javascript; charset=utf-8',
  '.mjs':  'application/javascript; charset=utf-8',
  '.css':  'text/css; charset=utf-8',
  '.svg':  'image/svg+xml',
  '.json': 'application/json; charset=utf-8',
};

/**
 * Proxy an incoming request to the Arena backend.
 *
 * @param {http.IncomingMessage} req
 * @param {http.ServerResponse}  res
 * @param {string}               reqPath   Path + query string, e.g. '/v1/event-sessions/…'
 */
function proxyToBackend(req, res, reqPath) {
  const parsed   = url.parse(BACKEND_URL);
  const isHttps  = parsed.protocol === 'https:';
  const hostname = parsed.hostname ?? 'localhost';
  const port     = parsed.port
    ? parseInt(parsed.port, 10)
    : (isHttps ? 443 : 80);

  /** @type {http.RequestOptions} */
  const options = {
    hostname,
    port,
    path: reqPath,
    method: req.method ?? 'GET',
    headers: Object.assign({}, req.headers, {
      // Rewrite the Host header so the backend sees itself, not the proxy.
      host: parsed.host ?? hostname,
    }),
  };

  // Remove connection-hop headers that must not be forwarded.
  delete options.headers['connection'];
  delete options.headers['transfer-encoding'];

  const transport = isHttps ? require('https') : http;

  const proxyReq = transport.request(options, (proxyRes) => {
    // Forward all response headers (including CORS headers from the backend).
    const outHeaders = Object.assign({}, proxyRes.headers);
    // Remove hop-by-hop headers.
    delete outHeaders['connection'];
    delete outHeaders['transfer-encoding'];

    res.writeHead(proxyRes.statusCode ?? 502, outHeaders);
    proxyRes.pipe(res, { end: true });
  });

  proxyReq.on('error', (err) => {
    // Backend unreachable or connection refused.
    if (!res.headersSent) {
      res.writeHead(502, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        error:   'bad_gateway',
        message: 'Arena backend unreachable: ' + err.message,
      }));
    }
  });

  // Pipe the request body to the backend (important for POST/PUT).
  req.pipe(proxyReq, { end: true });
}

/**
 * Serve a static file from the widget root.
 *
 * @param {http.ServerResponse} res
 * @param {string}              filePath  Absolute filesystem path.
 * @param {string}              urlPath   Original URL path (for error messages).
 */
function serveStatic(res, filePath, urlPath) {
  const ext         = path.extname(filePath).toLowerCase();
  const contentType = MIME_TYPES[ext] ?? 'application/octet-stream';

  fs.readFile(filePath, (err, data) => {
    if (err) {
      res.writeHead(404, { 'Content-Type': 'text/plain' });
      res.end('404 Not Found: ' + urlPath);
      return;
    }
    res.writeHead(200, {
      'Content-Type':                contentType,
      'Cache-Control':               'no-cache',
      'Access-Control-Allow-Origin': '*',
    });
    res.end(data);
  });
}

const server = http.createServer((req, res) => {
  // Extract path + query; strip fragment.
  const raw     = (req.url ?? '/').split('#')[0];
  const urlPath = raw || '/';

  // ── Proxy /v1/* to the real Arena backend ──────────────────────────────────
  if (urlPath.startsWith('/v1/') || urlPath === '/v1') {
    proxyToBackend(req, res, urlPath);
    return;
  }

  // ── Static file serving ────────────────────────────────────────────────────

  // Strip query string for file lookup.
  const filePart = urlPath.split('?')[0];

  // Default route.
  const normalized = filePart === '/' ? '/demo/index.html' : filePart;

  // Path traversal guard: resolved path must stay inside ROOT.
  const absPath = path.resolve(ROOT, '.' + normalized);
  if (!absPath.startsWith(ROOT + path.sep) && absPath !== ROOT) {
    res.writeHead(403, { 'Content-Type': 'text/plain' });
    res.end('403 Forbidden');
    return;
  }

  serveStatic(res, absPath, normalized);
});

server.listen(PORT, '127.0.0.1', () => {
  process.stdout.write(
    `[serve-demo-real] Listening on http://127.0.0.1:${PORT} — /v1/* → ${BACKEND_URL}\n`,
  );
});

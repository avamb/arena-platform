/**
 * postbuild.mjs — run after `vite build`.
 * Copies demo/iframe.html → dist/v1/iframe.html so the iframe fallback
 * page ships in the same versioned CDN layout as arena-tickets.js.
 */
import { copyFileSync, mkdirSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const root = join(__dirname, '..');
const src = join(root, 'demo', 'iframe.html');
const dest = join(root, 'dist', 'v1', 'iframe.html');

mkdirSync(join(root, 'dist', 'v1'), { recursive: true });
copyFileSync(src, dest);
console.log('✓  Copied demo/iframe.html → dist/v1/iframe.html');

// Renders the cover PNGs: out/<film>/<lang>/<film>_<lang>_<format>_cover.png
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { chromium } from 'playwright';
import { parse } from 'yaml';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const cfg = parse(fs.readFileSync(path.join(root, 'covers', 'covers.yaml'), 'utf8')) as {
  formats: Record<string, { width: number; height: number }>;
  films: Record<string, {
    item?: Record<string, { kind: 'phone' | 'browser'; status?: string; crop?: { x: number; y: number; w: number; h: number }; src: Record<string, string> }>;
    texts: Record<string, { line1: string; line2: string; sub: string }>;
  }>;
};

// Formats of a film: the scenario's `formats` list (default 16x9, 9x16, 4x5), plus 16x9 always (YouTube).
// A film without scenarios/<film>/scenario.yaml gets the default list.
function formatsOf(film: string): string[] {
  const file = path.join(root, 'scenarios', film, 'scenario.yaml');
  let list = ['16x9', '9x16', '4x5'];
  if (fs.existsSync(file)) {
    const sc = parse(fs.readFileSync(file, 'utf8')) as { formats?: string[] } | null;
    if (sc?.formats) list = sc.formats;
  }
  const all = list.includes('16x9') ? list : [...list, '16x9'];
  for (const f of all) {
    if (!cfg.formats[f]) throw new Error(`${film}: unknown format ${f}`);
  }
  return all;
}

const coverUrl = pathToFileURL(path.join(root, 'covers', 'cover.html')).href;
const browser = await chromium.launch();
const written: string[] = [];
try {
  for (const [film, def] of Object.entries(cfg.films)) {
    for (const lang of Object.keys(def.texts)) {
      for (const fmt of formatsOf(film)) {
        const size = cfg.formats[fmt];
        const t = def.texts[lang];
        const spec = def.item?.[fmt] ?? def.item?.default;
        let item: unknown = null;
        const srcPath = spec?.src[lang];
        if (spec && srcPath) {
          const abs = path.isAbsolute(srcPath) ? srcPath : path.join(root, srcPath);
          if (fs.existsSync(abs)) {
            item = { kind: spec.kind, status: spec.status, crop: spec.crop, src: pathToFileURL(abs).href };
          } else {
            console.warn(`skip subject, file missing: ${abs}`);
          }
        }
        const cover = { w: size.width, h: size.height, format: fmt, lang, lines: [t.line1, t.line2], sub: t.sub.replace(/ —/g, ' —'), item };
        const page = await browser.newPage({ viewport: { width: size.width, height: size.height } });
        await page.addInitScript((c) => { (window as unknown as { COVER: unknown }).COVER = c; }, cover);
        await page.goto(coverUrl);
        await page.waitForSelector('body[data-ready="1"]');
        await page.evaluate(() => document.fonts.ready);
        const out = path.join(root, 'out', film, lang, `${film}_${lang}_${fmt}_cover.png`);
        fs.mkdirSync(path.dirname(out), { recursive: true });
        await page.screenshot({ path: out });
        await page.close();
        written.push(out);
        console.log(out);
      }
    }
  }
} finally {
  await browser.close();
}
console.log(`${written.length} covers`);

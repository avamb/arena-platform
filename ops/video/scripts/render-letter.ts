/**
 * Draws a letter of the platform as a phone mail screen for a film.
 *
 *   npx tsx scripts/render-letter.ts <letter.json> <out.png>
 *
 * letter.json: { "lang": "ru", "subject": "...", "html": "<the HTML body of the real letter>" }
 * The body is the letter that the stand sent. The script removes tracking pictures, makes the links
 * dead, shortens the one-time token and replaces the strings given in "replace" ([from, to] pairs).
 * The bar above the body (sender, subject, recipient) is drawn by this script, it is not a mail program.
 */
import fs from "node:fs";
import path from "node:path";
import { chromium } from "playwright";

interface Letter {
  lang: string;
  subject: string;
  html: string;
  to?: string;
  replace?: [string, string][];
}

const TO_LABEL: Record<string, string> = { ru: "Кому", en: "To", es: "Para" };

function clean(l: Letter): string {
  let body = l.html;
  const m = /<body[^>]*>([\s\S]*)<\/body>/i.exec(body);
  if (m) body = m[1];
  body = body
    .replace(/<!--[\s\S]*?-->/g, "")
    .replace(/<img[^>]*>/gi, "")
    .replace(/href="[^"]*"/gi, 'href="#"')
    .replace(/(token=[0-9a-f]{10})[0-9A-Za-z_-]+/g, "$1…");
  for (const [from, to] of l.replace ?? []) body = body.split(from).join(to);
  return body;
}

function esc(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

async function main(): Promise<void> {
  const [src, out] = process.argv.slice(2);
  if (!src || !out) throw new Error("usage: tsx scripts/render-letter.ts <letter.json> <out.png>");
  const l = JSON.parse(fs.readFileSync(src, "utf8")) as Letter;
  let subject = l.subject;
  for (const [from, to] of l.replace ?? []) subject = subject.split(from).join(to);
  const page = `<!doctype html><html lang="${l.lang}"><head><meta charset="utf-8"><style>
    body{margin:0;background:#fff;font-family:system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;color:#1a1a2e}
    .bar{padding:18px 20px 14px;border-bottom:1px solid #e5e7eb}
    .subj{font-size:19px;font-weight:600;line-height:1.3;margin:0 0 14px}
    .from{display:flex;gap:12px;align-items:center}
    .av{width:40px;height:40px;border-radius:50%;background:#000f8a;color:#fff;display:flex;align-items:center;justify-content:center;font-weight:700}
    .who{font-size:14px;font-weight:600}.to{font-size:12.5px;color:#6b7280;margin-top:2px}
    .body{padding:4px 20px 28px;font-family:sans-serif;font-size:15px}
    .body h1{font-size:20px}
  </style></head><body>
  <div class="bar"><p class="subj">${esc(subject)}</p>
    <div class="from"><div class="av">A</div><div><div class="who">ArenaSoldOut</div>
    <div class="to">${TO_LABEL[l.lang] ?? "To"}: ${esc(l.to ?? "carmen@example.com")}</div></div></div></div>
  <div class="body">${clean(l)}</div></body></html>`;
  const browser = await chromium.launch();
  try {
    const p = await browser.newPage({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2 });
    await p.setContent(page, { waitUntil: "load" });
    fs.mkdirSync(path.dirname(out), { recursive: true });
    await p.screenshot({ path: out, fullPage: true });
  } finally {
    await browser.close();
  }
  console.log(`letter ${path.basename(out)}`);
}

main().catch((e) => {
  console.error(e instanceof Error ? e.message : e);
  process.exit(1);
});

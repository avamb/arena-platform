/**
 * Draws the poster of the invented event of the how-to films (a jazz evening of "Aurora Events" in Berlin).
 *
 *   npx tsx scripts/make-web-posters.ts
 *
 * Writes scenarios/web-create-event/assets/poster_4x5.png (Russian) and assets/<en|es>/poster_4x5.png, 1080 x 1350.
 * The date, the hall and the price on the poster are the ones scripts/capture-web.ts types into the wizard.
 */
import fs from "node:fs";
import path from "node:path";
import { chromium } from "playwright";
import { ROOT } from "../src/env.ts";

const TEXT = {
  ru: { l1: "ВЕЧЕР", l2: "ДЖАЗА", band: "Квартет Марты Лем", date: "13 февраля 2027 · 20:00", hall: "Aurora Club · Берлин", price: "Билеты: 25 €" },
  en: { l1: "JAZZ", l2: "NIGHT", band: "Marta Lem Quartet", date: "13 February 2027 · 20:00", hall: "Aurora Club · Berlin", price: "Tickets: 25 €" },
  es: { l1: "NOCHE", l2: "DE JAZZ", band: "Cuarteto de Marta Lem", date: "13 de febrero de 2027 · 20:00", hall: "Aurora Club · Berlín", price: "Entradas: 25 €" },
} as const;

function html(t: (typeof TEXT)[keyof typeof TEXT]): string {
  return `<!doctype html><html><head><meta charset="utf-8"><style>
    body{margin:0;width:1080px;height:1350px;background:radial-gradient(circle at 70% 25%,#1d2a6b 0,#0b1230 60%);color:#fff;font-family:"Segoe UI",Arial,sans-serif;position:relative;overflow:hidden}
    .org{position:absolute;left:80px;top:70px;font-size:30px;letter-spacing:8px;color:#f6c343;font-weight:700}
    .sun{position:absolute;right:90px;top:150px;width:440px;height:440px;border-radius:50%;background:#f6c343}
    .note{position:absolute;right:205px;top:200px;font-size:330px;line-height:1;color:#0b1230;font-family:"Segoe UI Symbol","Arial Unicode MS",serif}
    .t{position:absolute;left:80px;top:690px;font-weight:900;font-size:170px;line-height:.98;letter-spacing:2px}
    .t span{display:block}.t .y{color:#f6c343}
    .band{position:absolute;left:84px;top:1050px;font-size:46px;font-weight:600}
    .rule{position:absolute;left:84px;top:1128px;width:912px;height:3px;background:#f6c343}
    .d{position:absolute;left:84px;top:1158px;font-size:38px;line-height:1.45}
    .p{position:absolute;right:80px;bottom:70px;background:#f6c343;color:#0b1230;font-weight:800;font-size:38px;padding:14px 26px;border-radius:12px}
  </style></head><body>
    <div class="org">AURORA EVENTS</div><div class="sun"></div><div class="note">&#9835;</div>
    <div class="t"><span>${t.l1}</span><span class="y">${t.l2}</span></div>
    <div class="band">${t.band}</div><div class="rule"></div>
    <div class="d">${t.date}<br>${t.hall}</div><div class="p">${t.price}</div>
  </body></html>`;
}

async function main(): Promise<void> {
  const browser = await chromium.launch();
  try {
    const page = await browser.newPage({ viewport: { width: 1080, height: 1350 } });
    for (const lang of ["ru", "en", "es"] as const) {
      const out = path.join(ROOT, "scenarios", "web-create-event", "assets", lang === "ru" ? "" : lang, "poster_4x5.png");
      fs.mkdirSync(path.dirname(out), { recursive: true });
      await page.setContent(html(TEXT[lang]), { waitUntil: "load" });
      await page.screenshot({ path: out });
      console.log(`poster ${lang}`);
    }
  } finally {
    await browser.close();
  }
}

main().catch((e) => {
  console.error(e instanceof Error ? e.message : e);
  process.exit(1);
});

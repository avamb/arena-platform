// Draws the three demo pictures (2880x1800) from local HTML mockups of an imaginary event center.
// All data is made up. Run: npm run assets:demo. The PNG files are committed.
import fs from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { chromium } from "playwright";
import { captureScene } from "../src/capture.ts";
import { ROOT } from "../src/env.ts";

const CSS = `
*{box-sizing:border-box}
body{margin:0;width:1440px;height:900px;position:relative;overflow:hidden;background:#f3f5fb;font-family:"Segoe UI",Arial,sans-serif;color:#1b2140}
.side{position:absolute;left:0;top:0;width:240px;height:900px;background:#1b2559;color:#cfd6ff}
.logo{position:absolute;left:28px;top:24px;font-size:26px;font-weight:700;color:#fff}
.org{position:absolute;left:28px;top:76px;width:184px;padding:10px 12px;border-radius:10px;background:#2b3a85;font-size:15px;color:#fff}
.org small{display:block;font-size:12px;color:#aab4f5}
.nav{position:absolute;left:16px;width:208px;height:42px;line-height:42px;padding-left:16px;border-radius:8px;font-size:16px}
.nav.on{background:#3a4bb0;color:#fff}
.top{position:absolute;left:240px;top:0;right:0;height:64px;background:#fff;border-bottom:1px solid #dde1f0;line-height:64px;padding-left:40px;font-size:14px;color:#6b7399}
.user{position:absolute;right:40px;top:0;font-size:14px}
h1{position:absolute;left:280px;top:88px;margin:0;font-size:30px;line-height:44px}
.btn{position:absolute;height:44px;line-height:44px;text-align:center;border-radius:10px;background:#3a4bb0;color:#fff;font-size:16px;font-weight:600}
.card{position:absolute;background:#fff;border:1px solid #dde1f0;border-radius:12px}
.row{position:absolute;left:280px;width:1120px;height:64px;background:#fff;border-bottom:1px solid #e6e9f5;line-height:64px;font-size:16px}
.row span{position:absolute}
.head{background:#eaedf8;height:44px;line-height:44px;font-size:13px;color:#6b7399;text-transform:uppercase;letter-spacing:.04em}
.pill{padding:4px 12px;border-radius:999px;font-size:13px;line-height:20px;top:19px}
.ok{background:#dcf5e5;color:#177a3c}.dr{background:#eceef6;color:#5a6188}
label{position:absolute;font-size:14px;color:#6b7399}
.in{position:absolute;height:48px;line-height:46px;padding-left:14px;background:#fff;border:1px solid #b9c0e0;border-radius:10px;font-size:17px}
.big{font-size:34px;font-weight:700;position:absolute;left:20px;top:44px}
.cap{position:absolute;left:20px;top:16px;font-size:14px;color:#6b7399}
.sub{position:absolute;left:20px;top:88px;font-size:13px;color:#177a3c}
.bar{position:absolute;bottom:40px;width:56px;background:#5d6fe0;border-radius:6px 6px 0 0}
.bl{position:absolute;bottom:14px;width:56px;text-align:center;font-size:12px;color:#6b7399}
`;

function shell(body: string, active: number): string {
  const items = ["Мероприятия", "Заказы", "Продажи", "Промокоды", "Команда"];
  const nav = items
    .map((t, i) => `<div class="nav${i === active ? " on" : ""}" style="top:${150 + i * 50}px">${t}</div>`)
    .join("");
  return `<!doctype html><html lang="ru"><head><meta charset="utf-8"><style>${CSS}</style></head><body>
<div class="side"><div class="logo">Event Center</div><div class="org">ABH TEAM<small>тестовая организация</small></div>${nav}</div>
<div class="top">ABH TEAM / демонстрационные данные<span class="user">Тестовый пользователь</span></div>${body}</body></html>`;
}

const EVENTS: [string, string, string, string, boolean][] = [
  ["Весенний джаз-вечер", "12.04.2027, 19:00", "Тестовый зал", "87 / 120", true],
  ["Стендап: демо-вечер", "18.04.2027, 20:00", "Клуб «Пример»", "40 / 80", true],
  ["Мастер-класс по керамике", "24.04.2027, 12:00", "Студия «Глина»", "12 / 16", true],
  ["Дегустация: вымышленные вина", "30.04.2027, 18:30", "Погреб «Образец»", "0 / 30", false],
  ["Камерный концерт", "08.05.2027, 19:30", "Тестовый зал", "0 / 150", false],
];

function eventsPage(): string {
  const rows = EVENTS.map(
    (e, i) =>
      `<div class="row" style="top:${214 + i * 64}px"><span style="left:20px">${e[0]}</span><span style="left:380px;color:#6b7399">${e[1]}</span><span style="left:620px;color:#6b7399">${e[2]}</span><span style="left:860px">${e[3]}</span><span class="pill ${e[4] ? "ok" : "dr"}" style="left:980px">${e[4] ? "Опубликовано" : "Черновик"}</span></div>`,
  ).join("");
  const head = `<div class="row head" style="top:170px"><span style="left:20px">Название</span><span style="left:380px">Дата</span><span style="left:620px">Площадка</span><span style="left:860px">Билеты</span><span style="left:980px">Статус</span></div>`;
  return shell(`<h1>Мероприятия</h1><div class="btn" style="left:1180px;top:88px;width:220px">Новое мероприятие</div>${head}${rows}`, 0);
}

function newEventPage(): string {
  return shell(
    `<h1>Новое мероприятие</h1>
<label style="left:280px;top:150px">Название</label><div class="in" style="left:280px;top:176px;width:640px">Тестовый вечер: демо</div>
<label style="left:280px;top:244px">Дата</label><div class="in" style="left:280px;top:270px;width:300px">15.05.2027</div>
<label style="left:620px;top:244px">Время</label><div class="in" style="left:620px;top:270px;width:300px">19:00</div>
<label style="left:280px;top:338px">Площадка</label><div class="in" style="left:280px;top:364px;width:640px">Тестовый зал</div>
<label style="left:280px;top:432px">Цена билета, EUR</label><div class="in" style="left:280px;top:458px;width:300px">25.00</div>
<div class="btn" style="left:280px;top:560px;width:180px;height:48px;line-height:48px">Создать</div>
<div class="card" style="left:980px;top:176px;width:420px;height:360px"><div class="cap">Как увидит покупатель</div>
<div style="position:absolute;left:20px;top:56px;width:380px;height:180px;border-radius:10px;background:linear-gradient(135deg,#5d6fe0,#9a6be0)"></div>
<div style="position:absolute;left:20px;top:252px;font-size:20px;font-weight:700">Тестовый вечер: демо</div>
<div style="position:absolute;left:20px;top:288px;font-size:14px;color:#6b7399">15.05.2027, 19:00 · Тестовый зал</div>
<div style="position:absolute;left:20px;top:312px;font-size:14px;color:#6b7399">от 25.00 EUR</div></div>`,
    0,
  );
}

function salesPage(): string {
  const cards: [string, string, string][] = [
    ["Продано билетов", "87", "из 120 доступных"],
    ["Выручка", "2 175 EUR", "+312 EUR за сутки"],
    ["Заказов", "41", "среднее 2,1 билета"],
    ["Возвраты", "1", "25 EUR"],
  ];
  const cardsHtml = cards
    .map(
      (c, i) =>
        `<div class="card" style="left:${280 + i * 280}px;top:150px;width:240px;height:120px"><div class="cap">${c[0]}</div><div class="big">${c[1]}</div><div class="sub">${c[2]}</div></div>`,
    )
    .join("");
  const vals = [20, 35, 28, 60, 45, 80, 95];
  const days = ["Пн", "Вт", "Ср", "Чт", "Пт", "Сб", "Вс"];
  const bars = vals
    .map(
      (v, i) =>
        `<div class="bar" style="left:${60 + i * 140}px;height:${v * 2.6}px"></div><div class="bl" style="left:${60 + i * 140}px">${days[i]}</div>`,
    )
    .join("");
  return shell(
    `<h1>Продажи: Весенний джаз-вечер</h1>${cardsHtml}
<div class="card" style="left:280px;top:310px;width:1080px;height:400px"><div class="cap">Продажи по дням</div><div style="position:absolute;left:0;top:0;width:100%;height:100%">${bars}</div></div>`,
    2,
  );
}

async function main(): Promise<void> {
  const tmp = path.join(ROOT, ".cache", "mockups");
  const outDir = path.join(ROOT, "scenarios", "demo", "assets");
  fs.mkdirSync(tmp, { recursive: true });
  const pages: [string, string][] = [
    ["01-events", eventsPage()],
    ["02-new-event", newEventPage()],
    ["03-sales", salesPage()],
  ];
  const browser = await chromium.launch();
  try {
    for (const [name, html] of pages) {
      const file = path.join(tmp, `${name}.html`);
      fs.writeFileSync(file, html, "utf8");
      await captureScene(browser, {
        id: name,
        steps: [{ goto: pathToFileURL(file).href }, { wait: 300 }],
        viewport: { width: 1440, height: 900 },
        outDir,
        record: false,
      });
      console.log(`written ${name}.png`);
    }
  } finally {
    await browser.close();
  }
}

main().catch((e: unknown) => {
  console.error(e instanceof Error ? e.message : String(e));
  process.exit(1);
});

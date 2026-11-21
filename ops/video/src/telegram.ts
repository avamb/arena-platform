// Drives ONE bot chat in Telegram Web K in a phone-size window (see README, "Telegram bot capture").
// Only https://web.telegram.org/k/#@<bot> is opened. The module never signs in: it uses a browser
// profile where a person signed in before, and stops if the page asks to sign in.
import fs from "node:fs";
import path from "node:path";
import { chromium, type BrowserContext, type Page } from "playwright";

export interface BotChatOptions {
  /** persistent browser profile folder (.cache/tg-profile, never committed) */
  profileDir: string;
  /** bot user name without the @ */
  bot: string;
  /** the name shown in the chat header of every screenshot */
  publicTitle: string;
  /** strings to replace in the messages before a screenshot, for example a staging host name */
  replacements?: Record<string, string>;
  /** window size in CSS pixels, default 420 x 880 */
  viewport?: { width: number; height: number };
}

export interface BotMessage {
  from: "me" | "bot";
  text: string;
  buttons: string[];
}

export interface BotChat {
  /** types the text and sends it, then waits until the chat is quiet */
  send(text: string): Promise<void>;
  /** presses the LAST button whose text matches (inline or keyboard); exact: the whole text must match */
  click(buttonText: string, opts?: { exact?: boolean }): Promise<void>;
  /** sends a file as a document */
  sendFile(filePath: string): Promise<void>;
  /** waits until the number and the length of the last message stop changing */
  settle(): Promise<void>;
  /** the last n messages, oldest first */
  lastMessages(n: number): Promise<BotMessage[]>;
  /** masks, then takes a double-resolution screenshot of the chat */
  shot(filePath: string): Promise<void>;
  close(): Promise<void>;
}

const SEND_BOX = ".input-message-input[contenteditable=true]";
const BUTTONS = ".bubbles .bubble .reply-markup-button, .reply-keyboard .btn, .reply-keyboard-button";

function norm(s: string): string {
  return s.toLowerCase().replace(/[^a-z0-9Ѐ-ӿ]/g, "");
}

export async function openBotChat(o: BotChatOptions): Promise<BotChat> {
  if (!/^[A-Za-z][A-Za-z0-9_]{2,63}$/.test(o.bot)) throw new Error("telegram: bad bot name");
  if (!o.publicTitle.trim()) throw new Error("telegram: publicTitle is required");
  const url = `https://web.telegram.org/k/#@${o.bot}`;
  const replacements = Object.entries(o.replacements ?? {});

  const ctx: BrowserContext = await chromium.launchPersistentContext(o.profileDir, {
    headless: true,
    viewport: o.viewport ?? { width: 420, height: 880 },
    deviceScaleFactor: 2,
    locale: "ru-RU",
  });
  let page: Page;
  try {
    page = ctx.pages()[0] ?? (await ctx.newPage());
    await page.goto(url, { waitUntil: "domcontentloaded" });
    try {
      await page.waitForSelector(".bubbles .bubble", { timeout: 30000 });
    } catch {
      const signIn = await page.locator("#auth-pages, .input-field-phone").count();
      throw new Error(
        signIn > 0
          ? "telegram: the page asks to sign in; sign in by hand in the profile first, this module never signs in"
          : "telegram: the chat did not open in 30 seconds",
      );
    }
    await page.waitForTimeout(2500);
    const title = (await page.locator(".chat-info .peer-title").first().innerText()).trim();
    // the header shows the display name of the bot; it must contain the user name without the trailing "bot"
    const want = norm(o.bot).replace(/bot$/, "");
    if (!want || !norm(title).includes(want)) throw new Error("telegram: the open chat is not the expected bot, stopping");
  } catch (e) {
    await ctx.close();
    throw e;
  }

  /** The public name, no unread counters, listed replacements, the Russian input placeholder. */
  async function mask(): Promise<void> {
    await page.evaluate(
      ({ publicTitle, replacements }) => {
        document.querySelectorAll(".chat-info .peer-title").forEach((e) => (e.textContent = publicTitle));
        document
          .querySelectorAll(".sidebar-close-button .badge, .chat-info .badge, .topbar .badge, .unread-count")
          .forEach((e) => ((e as HTMLElement).style.display = "none"));
        if (replacements.length > 0) {
          const walker = document.createTreeWalker(document.querySelector(".bubbles") ?? document.body, NodeFilter.SHOW_TEXT);
          for (let n = walker.nextNode(); n; n = walker.nextNode()) {
            let t = n.nodeValue ?? "";
            for (const [from, to] of replacements) t = t.split(from).join(to);
            if (t !== n.nodeValue) n.nodeValue = t;
          }
          document.querySelectorAll<HTMLAnchorElement>(".bubbles a[href]").forEach((a) => {
            let h = a.getAttribute("href") ?? "";
            for (const [from, to] of replacements) h = h.split(from).join(to);
            a.setAttribute("href", h);
          });
        }
        // the placeholder of the input field: attribute form and text form
        document.querySelectorAll<HTMLElement>(".chat-input [data-placeholder], .chat-input [placeholder]").forEach((e) => {
          for (const a of ["data-placeholder", "placeholder"]) {
            if (e.getAttribute(a) === "Message") e.setAttribute(a, "Сообщение");
          }
        });
        document.querySelectorAll<HTMLElement>(".chat-input *").forEach((e) => {
          if (e.children.length === 0 && e.textContent?.trim() === "Message") e.textContent = "Сообщение";
        });
      },
      { publicTitle: o.publicTitle, replacements },
    );
  }

  function assertOnBotPage(): void {
    if (!page.url().startsWith("https://web.telegram.org/k/")) throw new Error("telegram: the page left Telegram Web, stopping");
  }

  const chat: BotChat = {
    async settle() {
      assertOnBotPage();
      let last = "";
      for (let i = 0; i < 40; i++) {
        await page.waitForTimeout(700);
        const n = await page.locator(".bubbles .bubble").count();
        const len = await page.locator(".bubbles .bubble").last().innerText().then((t) => t.length).catch(() => 0);
        const key = `${n}:${len}`;
        if (key === last && i > 2) break;
        last = key;
      }
    },

    async send(text) {
      assertOnBotPage();
      const input = page.locator(SEND_BOX).first();
      await input.click();
      await page.keyboard.type(text, { delay: 15 });
      await page.keyboard.press("Enter");
      await chat.settle();
    },

    async click(buttonText, opts) {
      assertOnBotPage();
      const all = page.locator(BUTTONS);
      const n = await all.count();
      let hit = -1;
      for (let k = n - 1; k >= 0; k--) {
        const t = (await all.nth(k).innerText()).trim();
        if (opts?.exact ? t === buttonText : t.includes(buttonText)) {
          hit = k;
          break;
        }
      }
      if (hit < 0) throw new Error(`telegram: button not found: ${buttonText}`);
      await all.nth(hit).scrollIntoViewIfNeeded();
      await all.nth(hit).click();
      await chat.settle();
    },

    async sendFile(filePath) {
      assertOnBotPage();
      if (!fs.existsSync(filePath)) throw new Error(`telegram: file not found: ${filePath}`);
      await page.locator(".attach-file, .btn-icon.attach-file").first().click();
      const [chooser] = await Promise.all([
        page.waitForEvent("filechooser"),
        page.locator(".btn-menu-item", { hasText: /Document|Файл|Документ/ }).first().click(),
      ]);
      await chooser.setFiles(filePath);
      await page.waitForSelector(".popup-send-photo, .popup-new-media", { timeout: 15000 });
      await page.waitForTimeout(800);
      await page.locator(".popup-send-photo .btn-primary, .popup-new-media .btn-primary").first().click();
      await chat.settle();
    },

    async lastMessages(n) {
      assertOnBotPage();
      return page.evaluate((count) => {
        const bubbles = Array.from(document.querySelectorAll(".bubbles .bubble")).slice(-count);
        return bubbles.map((b) => {
          const msg = b.querySelector(".message, .document-name");
          const clone = msg ? (msg.cloneNode(true) as HTMLElement) : null;
          if (clone) clone.querySelectorAll(".time, .reply-markup").forEach((e) => e.remove());
          return {
            from: b.classList.contains("is-out") ? ("me" as const) : ("bot" as const),
            text: clone ? clone.innerText.trim() : "",
            buttons: Array.from(b.querySelectorAll(".reply-markup-button")).map((x) => (x as HTMLElement).innerText.trim()),
          };
        });
      }, n);
    },

    async shot(filePath) {
      assertOnBotPage();
      // never a picture of the chat list: the header of an open chat must be there
      if ((await page.locator(".chat-info .peer-title").count()) === 0) throw new Error("telegram: no open chat in the frame, not taking a screenshot");
      await mask();
      fs.mkdirSync(path.dirname(filePath), { recursive: true });
      await page.screenshot({ path: filePath });
    },

    async close() {
      await ctx.close();
    },
  };
  return chat;
}

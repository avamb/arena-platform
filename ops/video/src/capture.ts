import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { chromium, type Browser, type BrowserContext, type Page } from "playwright";
import { loadEnv } from "./env.ts";
import { loadScenario, type Step } from "./scenario.ts";

/** Allowed: file:, localhost, loopback, or a host with the word "staging" in a label (staging.x, ec-staging.x). No switch turns this off. */
export function assertHostAllowed(rawUrl: string): void {
  let u: URL;
  try {
    u = new URL(rawUrl);
  } catch {
    throw new Error("capture: not a valid address");
  }
  if (u.protocol === "file:") return;
  if (u.protocol !== "http:" && u.protocol !== "https:") throw new Error(`capture: protocol ${u.protocol} is not allowed`);
  const host = u.hostname.toLowerCase();
  if (host === "localhost" || host === "127.0.0.1" || host === "[::1]" || host === "::1") return;
  if (host.split(".").some((label) => label.split("-").includes("staging"))) return;
  throw new Error(`capture: host "${host}" is neither localhost nor a staging host, refusing`);
}

export interface RunContext {
  baseUrl?: string;
}

function substitute(text: string): string {
  return text.replace(/\{\{\s*env\.([A-Za-z0-9_]+)\s*\}\}/g, (_m, name: string) => {
    const v = process.env[name];
    if (v === undefined || v === "") throw new Error(`capture: environment variable ${name} is not set`);
    return v;
  });
}

function resolveUrl(target: string, ctx: RunContext): string {
  const t = substitute(target);
  let full: string;
  if (/^[a-z][a-z0-9+.-]*:/i.test(t)) full = t;
  else {
    if (!ctx.baseUrl) throw new Error("capture: a relative goto needs a base address");
    full = ctx.baseUrl.replace(/\/+$/, "") + (t.startsWith("/") ? t : `/${t}`);
  }
  assertHostAllowed(full);
  return full;
}

/** Runs steps. Logs the kind of each step and its selector, never a typed value. */
export async function runSteps(page: Page, steps: Step[], ctx: RunContext): Promise<void> {
  for (const step of steps) {
    if ("goto" in step) {
      const url = resolveUrl(step.goto, ctx);
      console.log(`  goto ${new URL(url).pathname}`);
      await page.goto(url, { waitUntil: "load" });
    } else if ("click" in step) {
      console.log(`  click ${step.click}`);
      await page.click(substitute(step.click));
    } else if ("hover" in step) {
      console.log(`  hover ${step.hover}`);
      await page.hover(substitute(step.hover));
    } else if ("fill" in step) {
      const { selector, value, value_env } = step.fill;
      console.log(`  fill ${selector}`);
      const sel = substitute(selector);
      const loc = page.locator(sel).first();
      if (value !== undefined) {
        const type = await loc.evaluate((el) => (el as HTMLInputElement).type ?? "");
        if (type.toLowerCase() === "password") {
          throw new Error("capture: refusing a literal value in a password field, use value_env");
        }
      }
      let text: string;
      if (value_env !== undefined) {
        const v = process.env[value_env];
        if (v === undefined) throw new Error(`capture: environment variable ${value_env} is not set`);
        text = v;
      } else text = substitute(value ?? "");
      await loc.fill(text);
    } else if ("press" in step) {
      const p = step.press;
      const key = typeof p === "string" ? p : p.key;
      console.log(`  press ${key}`);
      if (typeof p !== "string" && p.selector) await page.press(substitute(p.selector), key);
      else await page.keyboard.press(key);
    } else if ("wait_for" in step) {
      console.log(`  wait_for ${step.wait_for}`);
      await page.waitForSelector(substitute(step.wait_for), { timeout: 30000 });
    } else if ("wait" in step) {
      await page.waitForTimeout(step.wait);
    }
  }
}

export interface CaptureSceneOptions {
  id: string;
  steps: Step[];
  viewport: { width: number; height: number };
  outDir: string;
  baseUrl?: string;
  storageState?: Awaited<ReturnType<BrowserContext["storageState"]>>;
  /** record a webm clip next to the screenshot (default true) */
  record?: boolean;
}

/** Does the steps of one scene and saves <id>.png (double resolution) and <id>.webm. */
export async function captureScene(browser: Browser, o: CaptureSceneOptions): Promise<void> {
  fs.mkdirSync(o.outDir, { recursive: true });
  const record = o.record !== false;
  const tmpVideo = path.join(o.outDir, `.rec-${o.id}`);
  const context = await browser.newContext({
    viewport: o.viewport,
    deviceScaleFactor: 2,
    storageState: o.storageState,
    ...(record ? { recordVideo: { dir: tmpVideo, size: o.viewport } } : {}),
  });
  const page = await context.newPage();
  try {
    await runSteps(page, o.steps, { baseUrl: o.baseUrl });
    await page.screenshot({ path: path.join(o.outDir, `${o.id}.png`) });
  } finally {
    const video = page.video();
    await context.close();
    if (record && video) {
      await video.saveAs(path.join(o.outDir, `${o.id}.webm`)).catch(() => undefined);
      fs.rmSync(tmpVideo, { recursive: true, force: true });
    }
  }
}

async function main(): Promise<void> {
  loadEnv();
  const id = process.argv[2];
  if (!id) throw new Error("usage: npm run capture -- <scenario>");
  const sc = loadScenario(id);
  const todo = sc.scenes.filter((s) => s.source.capture);
  if (todo.length === 0) {
    console.log("No scene has capture steps, nothing to do.");
    return;
  }
  if (!sc.capture) throw new Error("scenario has no capture section");
  const baseUrl = process.env[sc.capture.base_url_env];
  if (!baseUrl) throw new Error(`capture: ${sc.capture.base_url_env} is not set`);
  assertHostAllowed(baseUrl);

  const outDir = path.join(sc.dir, "captures");
  const browser = await chromium.launch();
  try {
    let storageState: BrowserContext extends never ? never : Awaited<ReturnType<BrowserContext["storageState"]>> | undefined;
    if (sc.capture.setup?.length) {
      console.log("setup (not recorded)");
      const ctx = await browser.newContext({ viewport: sc.capture.viewport, deviceScaleFactor: 2 });
      const page = await ctx.newPage();
      await runSteps(page, sc.capture.setup, { baseUrl });
      storageState = await ctx.storageState();
      await ctx.close();
    }
    for (const scene of todo) {
      console.log(`scene ${scene.id}`);
      await captureScene(browser, {
        id: scene.id,
        steps: scene.source.capture!,
        viewport: sc.capture.viewport,
        outDir,
        baseUrl,
        storageState,
        record: true,
      });
    }
  } finally {
    await browser.close();
  }
  console.log(`done: ${outDir}`);
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch((e: unknown) => {
    console.error(e instanceof Error ? e.message : String(e));
    process.exit(1);
  });
}

#!/usr/bin/env node
/**
 * Live harness of the web event center's twin tests (task 0-9, spec 40 5.0).
 *
 * Starts a REAL arena-api against the database named by DATABASE_URL (it is
 * migrated outside: `arena-migrate up`), puts one organization with three
 * people in it (owner org_admin, manager organizer, agent agent) and one
 * published event with a session, signs the three in through the API and
 * writes everything the Playwright specs need to
 * tests/smoke/.live-env.json. Then it stays up until it is stopped; the
 * Playwright config (tests/smoke/playwright.live.config.ts) runs it as a
 * webServer and waits for its /ready port.
 *
 * Environment (everything but DATABASE_URL has a default):
 *   DATABASE_URL            required, a migrated database
 *   ARENA_LIVE_API_PORT     arena-api port, default 18086
 *   ARENA_LIVE_READY_PORT   the harness's own readiness port, default 18087
 *   ARENA_LIVE_UI_PORT      the vite dev server port (CORS origin), default 5177
 *   ARENA_LIVE_STRIPE_PORT  the harness's Stripe stub (refunds, task 8-1), default 18088
 *   ARENA_API_BIN           a prebuilt arena-api (CI); else built with go
 *   ARENA_E2E_SEED_BIN      a prebuilt arena-e2e-seed (CI); else built with go
 *   JWT_SIGNING_SECRET      default: a fixed test value
 *   MEDIA_BACKEND, MEDIA_LOCAL_ROOT, MEDIA_SIGNING_SECRET
 *                           poster storage; default: local, under .live/media
 *   E2E_POSTER_SCRIPT, E2E_POSTER_CALLS
 *                           the scripted poster reader of the test-only arena-api
 *                           build (-tags e2eposter): what the "vision model" answers
 *                           on the next read, and a log of every read. Default:
 *                           under .live/. No real model key is ever used here.
 *
 * The web event center must be started with VITE_API_BASE_URL pointing at
 * the arena-api port, or it throws on boot (AGENTS.md).
 */
import { spawn, spawnSync } from "node:child_process";
import { existsSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const appDir = resolve(here, "..");
const repoRoot = resolve(appDir, "..", "..");
const liveDir = join(appDir, ".live");
const envFile = join(appDir, "tests", "smoke", ".live-env.json");
const exe = process.platform === "win32" ? ".exe" : "";

const apiPort = Number(process.env.ARENA_LIVE_API_PORT ?? 18086);
const readyPort = Number(process.env.ARENA_LIVE_READY_PORT ?? 18087);
const uiPort = Number(process.env.ARENA_LIVE_UI_PORT ?? 5177);
const stripePort = Number(process.env.ARENA_LIVE_STRIPE_PORT ?? 18088);
const apiBase = `http://127.0.0.1:${apiPort}`;
const stripeBase = `http://127.0.0.1:${stripePort}`;

function log(msg) {
  console.log(`[live-harness] ${msg}`);
}

function die(msg) {
  console.error(`[live-harness] ERROR: ${msg}`);
  process.exit(1);
}

if (!process.env.DATABASE_URL) die("DATABASE_URL is required (a database migrated by arena-migrate up).");

// The fixture writes an organization, three accounts and an event: only into
// a scratch database on this machine, never a remote one and never the shared
// dev-stand database `arena` (the same refusal as ops/loadtest provision.mjs).
{
  let dsn;
  try {
    dsn = new URL(process.env.DATABASE_URL);
  } catch {
    die("DATABASE_URL is not a valid URL.");
  }
  const localHosts = new Set(["localhost", "127.0.0.1", "[::1]", "::1"]);
  if (!localHosts.has(dsn.hostname)) {
    die(`DATABASE_URL points at ${dsn.hostname}: the live harness runs only against a local scratch database.`);
  }
  const dbName = decodeURIComponent(dsn.pathname.replace(/^\//, ""));
  if (dbName === "" || dbName === "arena") {
    die(`DATABASE_URL names the database "${dbName}": use a scratch database such as arena_ci_w09 (tests/README.md), never the shared stand.`);
  }
}

// A file left by a killed run would hand the specs a stale fixture.
rmSync(envFile, { force: true });

/** A binary of the backend: the prebuilt one from the environment, else built with go. */
function backendBinary(envName, pkg, outName, goArgs = []) {
  const given = process.env[envName];
  if (given) {
    if (!existsSync(given)) die(`${envName}=${given} does not exist.`);
    return given;
  }
  mkdirSync(liveDir, { recursive: true });
  const out = join(liveDir, outName + exe);
  log(`building ${pkg}`);
  const goCmd = process.platform === "win32" ? "go.exe" : "go";
  const r = spawnSync(goCmd, ["build", ...goArgs, "-o", out, pkg], { cwd: repoRoot, stdio: "inherit" });
  if (r.status !== 0) die(`go build ${pkg} failed (go must be on PATH).`);
  return out;
}

async function waitFor(url, what, seconds = 60) {
  for (let i = 0; i < seconds; i += 1) {
    try {
      const res = await fetch(url);
      if (res.ok) return;
    } catch {
      /* not up yet */
    }
    await new Promise((r) => setTimeout(r, 1000));
  }
  die(`${what} did not answer ${url} within ${seconds} s.`);
}

async function signIn(person) {
  const res = await fetch(`${apiBase}/v1/auth/login`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-Client-Channel": "event_center_web" },
    body: JSON.stringify({ email: person.email, password: person.password }),
  });
  if (!res.ok) die(`sign-in of ${person.role} answered ${res.status}: ${await res.text()}`);
  const body = await res.json();
  return { access_token: body.access_token, expires_at: body.expires_at };
}

// 1. The fixture: one organization, three memberships, one published event.
const seedBin = backendBinary("ARENA_E2E_SEED_BIN", "./apps/backend/cmd/arena-e2e-seed", "arena-e2e-seed");
log("seeding the fixture");
const seeded = spawnSync(seedBin, [], { cwd: repoRoot, env: process.env, encoding: "utf8" });
if (seeded.status !== 0) die(`arena-e2e-seed failed:\n${seeded.stderr}`);
const fixture = JSON.parse(seeded.stdout);

// 2. The real arena-api.
// Built with the test-only tag `e2eposter`: the vision model is replaced by a scripted reader
// (apps/backend/cmd/arena-api/poster_reader_e2e.go); everything around it is the real route.
// A prebuilt ARENA_API_BIN must be built the same way for the poster-first specs.
const apiBin = backendBinary("ARENA_API_BIN", "./apps/backend/cmd/arena-api", "arena-api", ["-tags", "e2eposter"]);
const mediaRoot = join(liveDir, "media");
mkdirSync(mediaRoot, { recursive: true });
const posterScript = process.env.E2E_POSTER_SCRIPT ?? join(liveDir, "poster-script.json");
const posterCalls = process.env.E2E_POSTER_CALLS ?? join(liveDir, "poster-calls.log");
// Start without a script (the reader then answers "not configured") and with an empty call log.
rmSync(posterScript, { force: true });
rmSync(posterCalls, { force: true });
// 2b. A Stripe stub for the refund twin (task 8-1). arena-api sends every
//    Stripe call of the fixture's test configuration here
//    (STRIPE_API_BASE_URL, refused in production by config.Validate), so no
//    test ever reaches Stripe. It answers what the refund engine asks:
//      POST /v1/refunds        a refund; the same Idempotency-Key answers the
//                              first refund again, as Stripe does. An amount
//                              of 777 (7.77) is declined with a Stripe error
//                              object, so the spec can show a refusal.
//      GET  /v1/refunds/{id}   a refund read back
//      GET  /v1/balance        what a credential check asks
//      GET  /__calls           every refund call it received (for the spec)
//      POST /__reset           forgets them
const stripeCalls = [];
const stripeRefunds = new Map(); // idempotency key -> answer
let stripeSeq = 0;
function stripeJson(res, status, body) {
  res.writeHead(status, { "Content-Type": "application/json" });
  res.end(JSON.stringify(body));
}
const stripeStub = createServer((req, res) => {
  const path = (req.url ?? "/").split("?")[0];
  let raw = "";
  req.on("data", (c) => {
    raw += c;
  });
  req.on("end", () => {
    if (req.method === "GET" && path === "/__calls") return stripeJson(res, 200, stripeCalls);
    if (req.method === "POST" && path === "/__reset") {
      stripeCalls.length = 0;
      stripeRefunds.clear();
      return stripeJson(res, 200, { ok: true });
    }
    if (req.method === "GET" && path === "/v1/balance") {
      return stripeJson(res, 200, { object: "balance", available: [], pending: [], livemode: false });
    }
    if (req.method === "POST" && path === "/v1/refunds") {
      const form = new URLSearchParams(raw);
      const key = req.headers["idempotency-key"] ?? "";
      const amount = Number(form.get("amount") ?? 0);
      stripeCalls.push({ amount, payment_intent: form.get("payment_intent"), key, order: form.get("metadata[arena_order_id]") });
      const seen = stripeRefunds.get(key);
      if (seen !== undefined) return stripeJson(res, seen.status, seen.body);
      let answer;
      if (amount === 777) {
        answer = {
          status: 400,
          body: { error: { type: "invalid_request_error", code: "charge_disputed", message: "The charge is disputed and cannot be refunded." } },
        };
      } else {
        stripeSeq += 1;
        answer = {
          status: 200,
          body: {
            id: `re_e2e_${stripeSeq}`,
            object: "refund",
            status: "succeeded",
            amount,
            currency: "eur",
            payment_intent: form.get("payment_intent"),
          },
        };
      }
      if (key !== "") stripeRefunds.set(key, answer);
      return stripeJson(res, answer.status, answer.body);
    }
    if (req.method === "GET" && path.startsWith("/v1/refunds/")) {
      const id = path.slice("/v1/refunds/".length);
      for (const a of stripeRefunds.values()) if (a.body.id === id) return stripeJson(res, 200, a.body);
      return stripeJson(res, 404, { error: { type: "invalid_request_error", code: "resource_missing", message: "No such refund" } });
    }
    return stripeJson(res, 404, { error: { type: "invalid_request_error", message: `stub: ${req.method} ${path}` } });
  });
});
stripeStub.listen(stripePort, "127.0.0.1");

// 3. Start the real arena-api (apiBin chosen in step 2).
const botServiceToken = process.env.BOT_SERVICE_TOKEN ?? "event-center-live-harness-bot-service-token";
const apiEnv = {
  ...process.env,
  APP_ENV: "development",
  JWT_SIGNING_SECRET: process.env.JWT_SIGNING_SECRET ?? "event-center-live-harness-jwt-secret-32-bytes",
  HTTP_LISTEN_ADDR: `127.0.0.1:${apiPort}`,
  // The vite dev server is another origin than the API: the browser needs CORS.
  CORS_ALLOWED_ORIGINS: [
    `http://127.0.0.1:${uiPort}`, `http://localhost:${uiPort}`,
    ...(process.env.ARENA_LIVE_PUBLIC_UI_PORT
      ? [`http://127.0.0.1:${Number(process.env.ARENA_LIVE_PUBLIC_UI_PORT)}`]
      : []),
  ].join(","),
  EVENT_CENTER_PUBLIC_URL: `http://127.0.0.1:${uiPort}`,
  // The Telegram mini app twin (spec 44, RT-12b) plays the bot: it asks POST /v1/bot/web-login-codes for a code.
  // The route answers 503 without a token; this one is a test value for the throwaway API of this run.
  BOT_SERVICE_TOKEN: botServiceToken,
  // Credit packs and platform invoices (B3-3/B4) are closed by default in a
  // deployment; the billing-packs twin needs them open.
  BILLING_ORGANIZER_ENABLED: process.env.BILLING_ORGANIZER_ENABLED ?? "true",
  // The hosted sales site: promoter cards show "<this>/<slug>" (task 4-6).
  PUBLIC_TICKETS_BASE_URL: process.env.PUBLIC_TICKETS_BASE_URL ?? "https://tickets.e2e.test",
  // Posters (task 7-1): uploads need a storage. A LOCAL one under the git-ignored
  // .live directory unless the caller names its own; never a production path.
  // API_PUBLIC_URL makes the signed poster address absolute, on the API origin.
  MEDIA_BACKEND: process.env.MEDIA_BACKEND ?? "local",
  MEDIA_LOCAL_ROOT: process.env.MEDIA_LOCAL_ROOT ?? mediaRoot,
  MEDIA_SIGNING_SECRET: process.env.MEDIA_SIGNING_SECRET ?? "event-center-live-harness-media-secret-32-bytes",
  API_PUBLIC_URL: apiBase,
  E2E_POSTER_SCRIPT: posterScript,
  E2E_POSTER_CALLS: posterCalls,
  // Every Stripe call goes to the stub above, never to Stripe (task 8-1).
  STRIPE_API_BASE_URL: `${stripeBase}/v1`,
};
log(`starting arena-api on ${apiBase}`);
const api = spawn(apiBin, [], { cwd: repoRoot, env: apiEnv, stdio: ["ignore", "inherit", "inherit"] });
api.on("exit", (code) => {
  if (!stopping) die(`arena-api exited early with code ${code}.`);
});

let stopping = false;
function stop() {
  if (stopping) return;
  stopping = true;
  log("stopping");
  try {
    rmSync(envFile, { force: true });
  } catch {
    /* best effort */
  }
  api.kill();
  ready.close();
  stripeStub.close();
  setTimeout(() => process.exit(0), 300);
}
process.on("SIGINT", stop);
process.on("SIGTERM", stop);
process.on("exit", () => api.kill());

// The readiness port answers 200 only once the fixture file is written.
let isReady = false;
const ready = createServer((req, res) => {
  res.statusCode = isReady ? 200 : 503;
  res.end(isReady ? "ready" : "starting");
});
ready.listen(readyPort, "127.0.0.1");

await waitFor(`${apiBase}/healthz`, "arena-api");

// 4. Sign the three in once: proves the fixture works against the real login
//    and gives API-level assertions an access token (about 15 minutes).
const people = [];
for (const p of fixture.people) {
  people.push({ ...p, ...(await signIn(p)) });
}

mkdirSync(dirname(envFile), { recursive: true });
writeFileSync(
  envFile,
  JSON.stringify(
    {
      api_base_url: apiBase,
      org_id: fixture.org_id,
      org_name: fixture.org_name,
      event_id: fixture.event_id,
      event_name: fixture.event_name,
      session_id: fixture.session_id,
      foreign_media_id: fixture.foreign_media_id,
      poster_script: posterScript,
      poster_calls: posterCalls,
      stripe_stub_url: stripeBase,
      bot_service_token: botServiceToken,
      people,
    },
    null,
    2,
  ),
  { mode: 0o600 },
);
isReady = true;
log(`ready: ${fixture.people.length} people, event "${fixture.event_name}", ${envFile}`);

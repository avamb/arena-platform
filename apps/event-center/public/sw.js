/*
 * Service worker of the Arena event center (spec 37, PWA, owner decision
 * 2026-10-14). It only lets the app start fast and open without network.
 *
 * What it caches: the app shell of THIS origin and nothing else — the
 * content-hashed files under /assets/, the icons under /icons/, and
 * /index.html as the offline start page. None of them carries personal data.
 *
 * What it never touches: arena-api. The API lives on another origin
 * (VITE_API_BASE_URL), and the worker ignores every request that is not a
 * GET of this origin; it also ignores /api/ (the future same-origin proxy,
 * WEB-AUTH-1), /health and itself. Buyers' e-mails, phones, orders and
 * tokens therefore never enter the cache.
 *
 * Versions: the page registers /sw.js?v=<build id>, so every build is a new
 * worker with its own cache name. On activate the worker deletes the caches
 * of older builds, so a stale /index.html or an old icon never outlives a
 * deploy.
 */
"use strict";

const CACHE_PREFIX = "arena-ec-shell-";

/** The build id the page put into the worker's address (?v=), "dev" without it. */
function arenaBuildVersion(href) {
  const v = new URL(href).searchParams.get("v");
  return v && /^[A-Za-z0-9._-]{1,64}$/.test(v) ? v : "dev";
}
self.arenaBuildVersion = arenaBuildVersion;

const CACHE = CACHE_PREFIX + arenaBuildVersion(self.location.href);
const OFFLINE_PAGE = "/index.html";

/**
 * What to do with a request: "skip" (the network handles it, the worker
 * stays out), "asset" (cache first: the file name carries its hash),
 * "fresh" (network first, the cached copy only offline: a file whose name
 * does not change when its content does, like an icon) or "page" (an app
 * address: network first, the cached shell when offline).
 */
function arenaCachePolicy(url, method, origin, mode) {
  if (method !== "GET") return "skip";
  const u = new URL(url);
  if (u.origin !== origin) return "skip";
  const p = u.pathname;
  if (p.startsWith("/api/") || p === "/health" || p === "/sw.js") return "skip";
  if (p.startsWith("/assets/")) return "asset";
  if (p.startsWith("/icons/")) return "fresh";
  if (mode === "navigate") return "page";
  return "skip";
}
self.arenaCachePolicy = arenaCachePolicy;

/** The caches an activating worker removes: older builds of this app only. */
function arenaStaleCaches(keys, current) {
  return keys.filter((k) => k.startsWith(CACHE_PREFIX) && k !== current);
}
self.arenaStaleCaches = arenaStaleCaches;

self.addEventListener("install", (event) => {
  event.waitUntil(
    caches
      .open(CACHE)
      .then((cache) => cache.add(OFFLINE_PAGE))
      .then(() => self.skipWaiting()),
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) => Promise.all(arenaStaleCaches(keys, CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

function keep(cache, req, res) {
  if (res.ok && res.type === "basic") cache.put(req, res.clone());
  return res;
}

self.addEventListener("fetch", (event) => {
  const req = event.request;
  const policy = arenaCachePolicy(req.url, req.method, self.location.origin, req.mode);
  if (policy === "asset") {
    event.respondWith(
      caches.open(CACHE).then((cache) => cache.match(req).then((hit) => hit || fetch(req).then((res) => keep(cache, req, res)))),
    );
    return;
  }
  if (policy === "fresh") {
    event.respondWith(
      caches.open(CACHE).then((cache) =>
        fetch(req)
          .then((res) => keep(cache, req, res))
          .catch(() => cache.match(req).then((hit) => hit || Response.error())),
      ),
    );
    return;
  }
  if (policy === "page") {
    event.respondWith(
      fetch(req).catch(() => caches.open(CACHE).then((cache) => cache.match(OFFLINE_PAGE))),
    );
  }
});

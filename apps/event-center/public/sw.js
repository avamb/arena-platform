/*
 * Service worker of the Arena event center (spec 37, PWA, owner decision
 * 2026-10-14). It only lets the app start fast and open without network.
 *
 * What it caches: the app shell of THIS origin and nothing else — the
 * content-hashed files under /assets/ and /icons/, and /index.html as the
 * offline start page. None of them carries personal data.
 *
 * What it never touches: arena-api. The API lives on another origin
 * (VITE_API_BASE_URL), and the worker ignores every request that is not a
 * GET of this origin; it also ignores /api/ (the future same-origin proxy,
 * WEB-AUTH-1), /health and itself. Buyers' e-mails, phones, orders and
 * tokens therefore never enter the cache.
 */
"use strict";

const CACHE = "arena-ec-shell-v1";
const OFFLINE_PAGE = "/index.html";

/**
 * What to do with a request: "skip" (the network handles it, the worker
 * stays out), "asset" (cache first: the file name carries its hash) or
 * "page" (an app address: network first, the cached shell when offline).
 */
function arenaCachePolicy(url, method, origin, mode) {
  if (method !== "GET") return "skip";
  const u = new URL(url);
  if (u.origin !== origin) return "skip";
  const p = u.pathname;
  if (p.startsWith("/api/") || p === "/health" || p === "/sw.js") return "skip";
  if (p.startsWith("/assets/") || p.startsWith("/icons/")) return "asset";
  if (mode === "navigate") return "page";
  return "skip";
}
self.arenaCachePolicy = arenaCachePolicy;

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
      .then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

self.addEventListener("fetch", (event) => {
  const req = event.request;
  const policy = arenaCachePolicy(req.url, req.method, self.location.origin, req.mode);
  if (policy === "asset") {
    event.respondWith(
      caches.open(CACHE).then((cache) =>
        cache.match(req).then(
          (hit) =>
            hit ||
            fetch(req).then((res) => {
              if (res.ok && res.type === "basic") cache.put(req, res.clone());
              return res;
            }),
        ),
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

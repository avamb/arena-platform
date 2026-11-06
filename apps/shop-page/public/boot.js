/*
 * boot.js - starts the storefront's first API requests while the main bundle is
 * still downloading (an organizer's own domain only; on the platform host the
 * organization is in the path and the bundle asks itself).
 *
 * It is a plain script, the first in <head>, with no imports, so it runs as soon
 * as it arrives. It asks GET /v1/public/shop/by-host (skipped when the host's
 * organization is in the one-hour cache main.ts writes under
 * "arena_shop_host:<host>") and then GET /v1/public/shop/{slug}, and leaves both
 * promises in window.__arenaShopPrefetch. main.ts takes them (lib/api.ts
 * takePrefetch) and falls back to its own requests when anything here failed.
 * The API base comes from <meta name="arena-api-base">, written by vite.config.ts.
 */
(function () {
  try {
    var host = location.hostname.toLowerCase();
    if (
      host === 'localhost' || host === '127.0.0.1' || host === '[::1]' || /\.localhost$/.test(host) ||
      host === 'arenasoldout.com' || /\.arenasoldout\.com$/.test(host)
    ) {
      return;
    }
    var meta = document.querySelector('meta[name="arena-api-base"]');
    var api = ((meta && meta.getAttribute('content')) || '').replace(/\/+$/, '');
    var get = function (path) {
      return fetch(api + path, { headers: { Accept: 'application/json' } });
    };
    var cached = null;
    try {
      var raw = localStorage.getItem('arena_shop_host:' + host);
      var c = raw ? JSON.parse(raw) : null;
      if (c && c.slug && Date.now() - c.at < 3600000) cached = c.slug;
    } catch (e) {
      /* no cache */
    }
    var slug = cached
      ? Promise.resolve(cached)
      : get('/v1/public/shop/by-host?host=' + encodeURIComponent(host))
          .then(function (r) { return r.ok ? r.json() : null; })
          .then(function (b) { return (b && b.org_slug) || null; });
    slug = slug.catch(function () { return null; });
    var catalog = slug
      .then(function (s) { return s ? get('/v1/public/shop/' + encodeURIComponent(s)) : null; })
      .catch(function () { return null; });
    window.__arenaShopPrefetch = { host: host, slug: slug, catalog: catalog };
  } catch (e) {
    /* the bundle asks the API itself */
  }
})();

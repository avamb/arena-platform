# 301 table: einatwinery.com (Shopify) to the arena storefront

The storefront keeps Shopify's URL shapes, so on the organizer's own domain
**most old addresses need no redirect at all**: the same path is a real route of
the new app. This file lists what is identical, what has to be redirected, and
what has no equivalent yet. Source: the offline export
(`.local/einatwinery_export/theme_live/hrefs.txt` and the page list) and
`collections.json`.

On the platform host the same paths simply sit under `/{org_slug}`
(`shop.arenasoldout.com/einatwinery/products/two-cats`); the table below is for
the custom domain where the base is empty.

## Identical (no redirect)

| Old path | New path | Note |
| --- | --- | --- |
| `/` | `/` | home: banner + featured collection |
| `/collections/all` | `/collections/all` | all 7 active products |
| `/products/{handle}` | `/products/{slug}` | the seven handles below; Cyrillic ones stay percent-encoded |
| `/pages/наши-мероприятия` | same | the page body is delivered by the API |
| `/pages/частные-мероприятия` | same | |
| `/pages/наши-партнеры` | same | |
| `/pages/о-нас` | same | |
| `/pages/contact` | same | the contact form sends mail through the shop's contact address (mailto) |
| `/policies/privacy-policy` | same | |
| `/policies/refund-policy` | same | |
| `/policies/terms-of-service` | same | |
| `/policies/contact-information` | same | |
| `/cart` | `/cart` | |
| `/search?q=...` | same | client-side search over the catalog |

Product handles (slugs must be imported unchanged):
`wineandglassesset`, `lonelycat`, `красное-сухое-4-кота`,
`набор-красных-вин-собери-кота`, `3cats-2018`, `two-cats`, `lonely-cat-cabfr`.

## To redirect (301)

| Old path | New path | Why |
| --- | --- | --- |
| `/collections/{any other handle}` (`new-4-12-23`, ...) | `/collections/all` | the app has one collection; "Наши вина" (`new-4-12-23`) is the same merchandise |
| `/collections/tickets` | `/pages/наши-мероприятия` | tickets are events, sold through arena event pages, not the merch shop |
| `/collections/{handle}/products/{product}` | `/products/{product}` | Shopify's collection-scoped product URL |
| `/collections/all.atom`, `/collections/{handle}.atom` | `/collections/all` | no feeds |
| `/products/{handle of a ticket product}` | `/pages/наши-мероприятия` | ticket products of the old shop are not part of the merch catalog |
| `/cart/{variant_id}:{qty}` | `/cart` | cart permalinks cannot be honored (variant ids change) |
| `/checkouts/...`, `/checkout` (old) | `/cart` | Shopify checkout sessions are dead after the move |
| `/account`, `/account/login`, `/account/register`, `/customer_authentication/...` | `/pages/contact` | no customer accounts in the first version |
| `/blogs/news/...`, `/blogs/галерея-мероприятий-2/...`, `/blogs/...` | `/pages/наши-мероприятия` | the blog is not ported; the events page carries the upcoming-events blocks as static markup and its article links point nowhere new |

## Gone (no redirect, answer 404)

`/cart.js`, `/cart/add.js`, `/products/{handle}.js`, `/products.json`,
`/collections/all/products.json`, `/search/suggest.json`, Shopify app endpoints
(`/apps/...`), `/.well-known/ucp`, `/admin`. Nothing links to them.

## Not under this app

`/sitemap.xml` and `/robots.txt` are served by the platform later (an event for
the SEO work); until then the old files disappear with the old shop.

## nginx form (when the domain is served by this container)

```nginx
# inside the server block of nginx.conf.template, before "location /"
rewrite ^/collections/tickets/?$            /pages/%D0%BD%D0%B0%D1%88%D0%B8-%D0%BC%D0%B5%D1%80%D0%BE%D0%BF%D1%80%D0%B8%D1%8F%D1%82%D0%B8%D1%8F permanent;
rewrite ^/collections/[^/]+/products/(.+)$   /products/$1 permanent;
rewrite ^/collections/(?!all/?$)[^/]+/?$     /collections/all permanent;
rewrite ^/collections/[^/]+\.atom$           /collections/all permanent;
rewrite ^/cart/[0-9]+:[0-9]+.*$              /cart permanent;
rewrite ^/checkouts/.*$                      /cart permanent;
rewrite ^/(account|customer_authentication)(/.*)?$  /pages/contact permanent;
rewrite ^/blogs/.*$                          /pages/%D0%BD%D0%B0%D1%88%D0%B8-%D0%BC%D0%B5%D1%80%D0%BE%D0%BF%D1%80%D0%B8%D1%8F%D1%82%D0%B8%D1%8F permanent;
```

The ticket products' handles are not in the export; add one rewrite per handle
once the list of ticket products is known (the "Билеты" collection has 22).
Cloudflare Bulk Redirects is the alternative if the zone already carries
redirects.

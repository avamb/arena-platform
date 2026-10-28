# Shopify import into the merchandise shop (`arena-shop-import`)

One-shot operator tool (spec `08_architecture/47_merch_shop_module_ru.md`
section 8). It loads a Shopify export into the shop module of ONE
organization. It is idempotent: running it again updates, never duplicates.

It lives in `apps/backend/cmd/arena-shop-import` and, like `arena-seed` and
`arena-bil24-import`, is not part of the Docker image: run it from a machine
that can reach the database.

## What goes where

| Export | Becomes | Key used to recognise it again |
|---|---|---|
| Products CSV, one product per Handle | `shop_products` (title, `body_html`, status, type, vendor, order of the file as `sort_order`) | slug = Shopify handle, lower-cased (Cyrillic handles stay Cyrillic) |
| Variant rows | `shop_variants` (options, SKU, price, compare-at, stock, position, track_stock) | SKU, then the three option values, then the title |
| `Image Src` rows | `media_objects` (`owner_type shop_product`) + `shop_product_images` (position, alt) | SHA-256 of the picture's bytes |
| Customers CSV | `customers` through `customers.Resolve` (e-mail and phone identities, name, locale `ru` on create), `customer_org_links` (source `import`), marketing consent in `customer_consents` when `Accepts Email Marketing` is `yes` | e-mail and phone identity |
| Rendered theme pages + a mapping file | `shop_pages` | slug from the mapping file |
| Settings (see below) | `shop_settings` | one row per organization |
| Orders CSV | parsed and summed only, nothing is written (second wave) | - |

Rules that matter:

* **Money** is converted from major to minor units exactly once, on the decimal
  string (never a float): `650.00` -> `65000`. An amount with more decimals than
  the currency has is refused, not rounded. `--currency` (default `ILS`).
* **Only `active` products** are imported unless `--include-drafts`, which
  also brings `draft`, `archived` and `unlisted` ones (`unlisted` becomes a
  `draft`: the shop has no such state).
* **Stock** follows Shopify only when it counted it (`Variant Inventory
  Tracker` set) and refuses to oversell (`Inventory Policy` not `continue`),
  otherwise the variant is untracked. On a re-run the stock of an existing
  variant is NOT overwritten (once the shop sells, its own number is the
  truth) unless `--sync-stock`.
* A stored variant the CSV does not list (an organizer added it) is kept.
  A picture the CSV does not list stays attached.
* **Settings** are written only when the organization has no `shop_settings`
  row. Creating a product makes a default row, so run this import before
  anyone adds a product by hand. Defaults (flags): currency from `--currency`,
  shipping fee `50.00` (`--shipping-fee`), country `IL`, age gate 18,
  announcement "Special discount 15% on gift sets", contact e-mail
  `sales@einatwinery.com`.
* **Descriptions and pages** are cleaned to a small tag set (p, br, h2-h6,
  lists, strong, em, u, a, img, blockquote, tables): no script, style, class,
  inline style or event handler; `h1` becomes `h2`; `//cdn...` becomes
  `https://cdn...`; empty paragraphs and theme sections that only held a
  dropped product grid disappear. Relative links are left relative unless
  `--site-url` is given.
* Pictures are read from `--images-dir` by file name when a local copy exists,
  otherwise downloaded from the CSV URL. The bytes decide the type (jpeg, png,
  webp, gif, up to 15 MB). A picture that cannot be read is listed under
  "missing images" and the product is still imported.
* Customers: each one is its own transaction; a bad row costs one row. The
  report never prints a name, e-mail or phone. A customer without an e-mail is
  imported by phone; consent to e-mail marketing needs an e-mail. Rows that
  share a phone number with an earlier row resolve to the same customer.

## Flags

```
--org <uuid>          organization that owns the shop (required)
--products <csv>      --customers <csv>      --orders <csv>
--pages <dir>         folder of rendered theme html
--pages-map <json>    file -> slug/title/position (required with --pages);
                      docs/ops/shop_import_pages.einatwinery.json is the one for
                      the Einat Winery theme export
--images-dir <dir>    local copies of the pictures (optional)
--include-drafts      also draft, unlisted and archived products
--dry-run             report only: no writes, no downloads
--only-new            leave products and pages that already exist alone
--sync-stock          overwrite stock of existing variants with the CSV's
--skip-images         --skip-settings
--currency ILS        --default-region IL      --locale ru      --site-url <url>
--shipping-fee --country --age-gate --announcement --contact-email   (new settings)
--db-url <dsn>        else DATABASE_URL
```

A dry run with a database compares with what is stored; without
`DATABASE_URL` it runs offline and counts everything as new. Exit code 1 when
any product, picture, page, customer or the settings failed.

Pictures need the media store of the target environment: `MEDIA_BACKEND` and
its settings (`MEDIA_LOCAL_ROOT`, or the `MEDIA_S3_*` set) plus
`MEDIA_SIGNING_SECRET`, exactly as arena-api has them, and `JWT_SIGNING_SECRET`
(config loading demands it). The pictures must land in the storage the stand's
API reads, so for the stand that means its S3/R2 settings.

## Command lines

Dry run against a local scratch database (this is how the numbers below were
made; recipe for the scratch database is the "CI Integration job" one in
AGENTS.md, DB `arena_shop_l6`, host port 45432 on this machine):

```
DATABASE_URL=<scratch dsn> go run ./apps/backend/cmd/arena-shop-import \
  --org <org uuid> --dry-run \
  --products   C:/Projects/arena_new/.local/einatwinery_export/admin/products_export_1.csv \
  --customers  C:/Projects/arena_new/.local/einatwinery_export/admin/customers_export.csv \
  --orders     "C:/Projects/arena_new/.local/einatwinery_export/admin/orders_export_1 (2).csv" \
  --pages      C:/Projects/arena_new/.local/einatwinery_export/theme_live/pages \
  --pages-map  docs/ops/shop_import_pages.einatwinery.json
```

The stand: same command without `--dry-run`, with `DATABASE_URL` of the stand
database (through an SSH tunnel or from a host on its network) and the stand's
media settings in the environment. Do it in this order and read each report
before the next step:

1. the command above with `--dry-run`;
2. the same without `--dry-run` and without `--customers` (settings, products,
   pictures, pages);
3. open the storefront and compare with the live Shopify site;
4. `--customers <csv>` alone (personal data: only after the owner confirms the
   legal basis for moving the customers and their marketing consent);
5. run step 2 once more: every line must read "unchanged".

## Dry run on the real export (counts only)

Fresh scratch database, organization with no shop yet. Default selection
(active products only):

```
settings:  1 would be created
products:  7 would be created, 32 skipped (archived 3, draft 28, unlisted 1)
variants:  8 would be created
images:    22 would be created
pages:     9 would be created (2 pages carry a picture that still points at the old store's CDN)
customers: 932 would be created
consents:  290 would be created
money (ILS): prices 200.00 - 550.00, stock value 102630.00
orders file (parsed, NOT imported): 872 orders, 911 lines, 1 without e-mail,
           2 cancelled, 0 unreadable; paid 798, refunded 62, partially refunded 12
           ILS: total 469610.40, subtotal 459260.40, shipping 10350.00,
           discounts 3141.60, refunded 33342.80
```

With `--include-drafts`: 39 products, 50 variants, 70 pictures, prices
0.00 - 1750.00, stock value 2407029.00; the rest as above.

A real run of products and pages (all 39 products, pictures downloaded from the
Shopify CDN into a local media store) and of the customers on the scratch
database gave: 39 products, 50 variants, 68 pictures (2 more listed twice
with identical bytes), 9 pages, 932 customers resolving to 924 distinct people
(8 rows share a phone with an earlier row), 289 + 1 consents. The second run of
each reported only "unchanged".

## Known limits

* Orders are not imported yet (`shop_orders` with source `shopify`). The orders
  file is parsed and its totals are printed so they can be compared with
  Shopify's own report.
* Page pictures stay on the old store's CDN (`www.einatwinery.com/cdn/...`).
  They stop working when the Shopify store is closed; side-load
  them before that (the shop has no media owner type for pages yet).
* Links inside pages and descriptions that point to the old store
  (`/pages/contact`, `/products/...`) are kept as they are.
* A product keeps its Shopify handle as slug, so a re-run after the organizer
  renamed a slug creates the product again. Do not re-run after go-live except
  with `--only-new`.
* Stock moves are journaled with reason `manual` (the shop service's variant
  writer), not `import`.
* Discounts export is not read.

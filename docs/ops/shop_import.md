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
| Products CSV, one product per Handle | `shop_products` (title, `body_html`, status, `kind='physical'`, `option1_name..option3_name` from `Option1 Name..Option3 Name`, type, vendor, order of the file as `sort_order`) | slug = Shopify handle, lower-cased (Cyrillic handles stay Cyrillic) |
| Variant rows | `shop_variants` (options, SKU, price, compare-at, stock, position, track_stock) | SKU, then the three option values, then the title |
| `Image Src` rows | `media_objects` (`owner_type shop_product`) + `shop_product_images` (position, alt) | SHA-256 of the picture's bytes |
| Customers CSV | `customers` through `customers.Resolve` (e-mail and phone identities, name, locale `ru` on create), `customer_org_links` (source `import`), marketing consents (see below) | e-mail and phone identity |
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
* **Settings** never open the shop: `enabled` stays false (only a platform
  superadmin switches it on) and `max_line_qty` keeps its default 24.
* **Descriptions and pages** are first cleaned of Shopify markup (no script,
  style, class, inline style or event handler; `h1` becomes `h2`; `//cdn...`
  becomes `https://cdn...`; empty paragraphs and theme sections that only held
  a dropped product grid disappear) and then passed through
  `shop.SanitizeBody` with the shop's product/page profile, the same function
  the API uses. A page keeps pictures and tables (the page sanitizer), a
  product description does not: whatever the profile removes is counted per
  page / product in the report. A
  text the sanitizer refuses (too long, for instance) fails that product or
  page and is listed under problems, never cut silently. Relative links are
  left relative unless `--site-url` is given.
* **Option names**: `Option1 Name..Option3 Name` of a product's first row.
  Shopify's `Title` / `Default Title` placeholder is not an option.
* **Product kind**: new products are `physical` (the export has no digital
  goods). An existing product keeps its kind on a re-run.
* Pictures are read from `--images-dir` by file name when a local copy exists,
  otherwise downloaded from the CSV URL. The bytes decide the type (jpeg, png,
  webp, gif, up to 15 MB). A picture that cannot be read is listed under
  "missing images" and the product is still imported.
* **Picture size**: the width and height in pixels are read from the
  picture's header and stored on its media object (`media_objects.width` /
  `height`); the product answers carry them so a storefront card can keep the
  picture's aspect before it loads. On a re-run, a picture that is already
  attached but has no size yet gets it filled in ("picture size filled in" in
  the report); a recorded size is never overwritten.
* **Customers: all of them, with all their marketing consents** (owner
  decision 2026-10-28). They are on by default: when `--products` is given
  and `customers_export.csv` sits in the same folder (Shopify's own file name),
  it is imported without any flag; `--customers <csv>` names another file and
  `--skip-customers` leaves them out. Each customer is its own transaction; a
  bad row costs one row. The report never prints a name, e-mail or phone. A
  customer without an e-mail is imported by phone. Rows that share a phone
  number with an earlier row resolve to the same customer.
* **Consents** follow how the rest of the platform stores them:
  * `Accepts Email Marketing = yes` -> `customer_consents` (customer, org,
    kind `marketing`, `given_at` = the import time because the export has no
    consent timestamp, source `import:shopify`). First record wins: a re-run,
    or a consent the customer already has (even a withdrawn one), is never
    touched. Needs an e-mail.
  * `Accepts SMS Marketing = yes` and `Accepts WhatsApp Marketing = yes` ->
    organization-scoped `customer_attributes` `marketing_consent_sms` /
    `marketing_consent_whatsapp` (JSON with channel and the import time),
    because `customer_consents` has no channel and a `marketing` row would be
    read as e-mail consent. Needs a phone number. The Einat Winery export has
    no `yes` in either column.
  * `no` records nothing (the absence of a row is "no"): the export does not
    say whether the person never consented or unsubscribed.

## Flags

```
--org <uuid>          organization that owns the shop (required)
--products <csv>      --customers <csv>      --orders <csv>
                      (customers_export.csv next to --products is used when --customers is absent)
--pages <dir>         folder of rendered theme html
--pages-map <json>    file -> slug/title/position (required with --pages);
                      docs/ops/shop_import_pages.einatwinery.json is the one for
                      the Einat Winery theme export
--images-dir <dir>    local copies of the pictures (optional)
--include-drafts      also draft, unlisted and archived products
--dry-run             report only: no writes, no downloads
--only-new            leave products and pages that already exist alone
--sync-stock          overwrite stock of existing variants with the CSV's
--skip-images         --skip-settings        --skip-customers
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
API reads. The staging and production stands keep media with
`MEDIA_BACKEND=local` in a docker volume of the api/worker containers, so
there the tool runs from the api image itself (next section).

The importer is built into the arena image (`/app/arena-shop-import`, next to
arena-api, arena-worker, arena-migrate, arena-healthcheck and arena-bot), but
the image has no shell: it is started with `--entrypoint`.

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

## Running it on a stand (staging or production)

The api image has no shell and the media volume is only reachable from inside
the stand, so the importer runs as a one-off container of the api image on the
stand's host, joined to the stand's compose network and mounting the stand's
media volume. Nothing is installed on the server and nothing needs the web.

1. **Image.** The image must contain `/app/arena-shop-import`, i.e. be built
   from a commit that has this tool in the `Dockerfile` (check:
   `docker run --rm --entrypoint /app/arena-shop-import <image> --help`).
   Take the tag the stand runs now:
   `docker inspect <api container> --format '{{.Config.Image}}'`.
   If the stand still runs an older tag, deploy a newer one first.
2. **Export.** Copy the export folder to the server, outside any web root and
   outside the app's directories, read it back only through the container:
   `scp -r einatwinery_export/admin root@<server>:/root/einat_export` and the
   theme pages next to it (`.../theme_live/pages` to
   `/root/einat_export/pages`; the mapping file is in the repo,
   `docs/ops/shop_import_pages.einatwinery.json`, copy it to
   `/root/einat_export/pages_map.json`). It contains customers' personal data:
   `chmod -R go-rwx /root/einat_export`, and delete it when the import is done
   (`rm -rf /root/einat_export /root/api.env`).
3. **Environment.** Do not retype settings, take the api container's own
   (it passes the same configuration validation the importer runs):
   `docker inspect <api container> --format '{{range .Config.Env}}{{println .}}{{end}}' > /root/api.env; chmod 600 /root/api.env`.
   Never print this file. What the importer reads from it:

   | Variable | Value | Why |
   |---|---|---|
   | `DATABASE_URL` | the api's, which already names the stand's postgres by its compose hostname (`postgres`) and carries the `sslmode` the stand's `APP_ENV` accepts | where the shop lives. A host name that only resolves on the compose network is why the container joins it |
   | `JWT_SIGNING_SECRET` | the api's | config loading demands it, even though the tool signs no token |
   | `APP_ENV` | the api's | production refuses a plain `sslmode` unless `ALLOW_PRIVATE_DB_PLAINTEXT=true`, which the api env then already carries |
   | `MEDIA_BACKEND` | `local` | the stand's backend |
   | `MEDIA_LOCAL_ROOT` | EQUAL to the api's (for example `/data/media`) | the directory inside the container where the volume is mounted; the stored `media_objects.storage_key` is relative to it |
   | `MEDIA_SIGNING_SECRET` | EQUAL to the api's | the api verifies the signed picture URLs with it; another value and every picture answers 403 |

   Variables the importer does not use are harmless. A configuration error
   names the offending variable (`load config: invalid configuration: ...`).
4. **Network and volume.** Find them on the running api container, do not
   guess names:
   `docker inspect <api container> --format '{{json .NetworkSettings.Networks}}'`
   (the compose network, typically `<project>_default`) and
   `docker inspect <api container> --format '{{json .Mounts}}'` (the entry
   whose `Destination` equals `MEDIA_LOCAL_ROOT`; its `Name` is the volume).
   The load-test stand's convention is volume `arena-lt-media` at
   `/data/media`. The container runs as the image's `nonroot` user (uid 65532)
   like the api: if the first picture fails with `mkdir ... permission
   denied`, the volume belongs to another user, and the api could not write it
   either. Fix the ownership (or pass `--user` equal to the api's
   `docker inspect <api> --format '{{.Config.User}}'`), do not run the tool as
   root over an api-owned volume.
5. **Command.** Dry run first, the same command with `--dry-run` appended is
   enough for the first look (a dry run reads the database, writes nothing,
   downloads nothing). Then the same without it:

   ```
   docker run --rm --entrypoint /app/arena-shop-import \
     --network <compose network> \
     --env-file /root/api.env \
     -v <media volume>:<MEDIA_LOCAL_ROOT> \
     -v /root/einat_export:/import:ro \
     ghcr.io/avamb/arena-api:<tag> \
     --org <organization uuid> --dry-run \
     --products /import/products_export_1.csv \
     --customers /import/customers_export.csv \
     --orders "/import/orders_export_1 (2).csv" \
     --pages /import/pages --pages-map /import/pages_map.json
   ```

   Product pictures are downloaded from the Shopify CDN by the container, so
   the compose network needs outbound internet (a normal compose network
   has it). A real run needs no `--images-dir`. `--include-drafts` brings the
   non-active products as drafts.
6. **Afterwards**: `rm -rf /root/einat_export /root/api.env`, and the shop
   stays closed (`shop_settings.enabled=false`) until a platform superadmin
   opens it.

Order of work (on the stand as on a scratch database), reading each report
before the next step:

1. the command above with `--dry-run`;
2. the same without `--dry-run` (add `--skip-customers` to hold the customers
   back for the end): settings, products, pictures, pages;
3. open the storefront and compare with the live Shopify site;
4. customers ride along by default (the owner decided to move all of them with
   their consents). `--skip-customers` holds them back for a separate step;
5. run step 2 once more: every line must read "unchanged".

### Filling in the size of pictures imported earlier

Pictures imported before the importer recorded sizes (the 68 pictures of
Einat Winery on the staging stand, 2026-10-28) have no width and height, and
a migration cannot add them: the bytes live in the media store, not in the
database. Run the same products import once more, with the same CSV (and the
same `--images-dir` if the first run had one), without `--only-new` and
without `--dry-run`, for instance with `--skip-customers --skip-settings` and
no `--pages`. Every picture is read again, recognised by its checksum, and its
size is filled in; the report shows "picture size filled in" with the count,
and nothing is uploaded twice. Check afterwards:

```sql
SELECT count(*) FILTER (WHERE m.width IS NULL) AS without_size, count(*) AS total
FROM shop_product_images i JOIN media_objects m ON m.id = i.media_id
JOIN shop_products p ON p.id = i.product_id WHERE p.org_id = '<org uuid>';
```

Note that this re-run, like any re-run without `--only-new`, also writes back
the title, description, status, type, vendor, options and variants of a
product whose stored values differ from the CSV, so do it before the organizer
starts editing products. `--only-new` leaves existing products, and so their
pictures, untouched. A picture added by hand after this change gets its size
at upload (`POST /v1/media` reads it); one added by hand before it and not in
the CSV stays without a size, and the storefront has to fall back for it.

## Dry run on the real export (counts only)

Fresh scratch database, organization with no shop yet. Default selection
(active products only):

```
settings:  1 would be created
products:  7 would be created, 32 skipped (archived 3, draft 28, unlisted 1)
variants:  8 would be created
images:    22 would be created
pages:     9 would be created (2 pages carry a picture that still points at the Shopify CDN)
customers: 932 would be created
consents:  290 would be created (642 customers say no: nothing recorded)
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
(8 rows share a phone with an earlier row), 289 + 1 consents (e-mail marketing,
no SMS or WhatsApp `yes` in this export). Checked in the database after the run:
all 39 products `physical`, option names set on the 9 that have real options
(the 30 Default Title ones have none), `shop_settings.enabled` false,
`max_line_qty` 24. The second run of each reported only "unchanged".

## Known limits

* Orders are not imported yet (`shop_orders` with source `shopify`). The orders
  file is parsed and its totals are printed so they can be compared with
  Shopify's own report.
* Pictures inside pages (3 on `events`, 1 on `partners` for Einat Winery) are
  kept by the page sanitizer, but they still load from Shopify. Every `<img>`
  of a stored page or description that is still on Shopify (`cdn.shopify.com`, any `*.shopify.com` or
  `shopifycdn.net` host, or the old domain's `/cdn/` path) is reported as
  "page picture still points at the Shopify CDN" (descriptions: "description
  picture ..."). Those pictures stop working when the Shopify store is closed:
  re-host them before the domain switch.
* Links inside pages and descriptions that point to the old store
  (`/pages/contact`, `/products/...`) are kept as they are.
* A product keeps its Shopify handle as slug, so a re-run after the organizer
  renamed a slug creates the product again. Do not re-run after go-live except
  with `--only-new`.
* Stock moves are journaled with reason `manual` (the shop service's variant
  writer), not `import`.
* Discounts export is not read.

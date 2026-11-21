# Vino&Co: import of the mailing base (P-18, spec 62 section 10.1)

The command `arena-marketing-import` loads the Vino&Co mailing base into
`marketing_subscriptions` / `marketing_consents` (the same tables the widget
tick uses). It reads two exports and never connects to WordPress or Brevo: the
operator produces the files, this tool only reads them.

```
DATABASE_URL=... JWT_SIGNING_SECRET=x go run ./apps/backend/cmd/arena-marketing-import \
    --org-id <organization uuid> --brevo brevo.csv --elementor club.csv --dry-run
```

At least one of `--brevo` / `--elementor` is required. `--dry-run` runs the whole
import in a transaction that is rolled back, so the printed counts are exactly
what the real run would write. (`JWT_SIGNING_SECRET` is demanded by the config
loader, any value does.)

## The rules

1. **Suppressions win.** Brevo contacts that are blacklisted or unsubscribed are
   imported as suppressions: a subscription in status `withdrawn`. If the
   address already has an active subscription on the platform it is withdrawn.
   An address that is suppressed is never made a subscriber, in this run or any
   later one. A suppressed address that signs up again through the widget
   becomes `pending` and needs the double opt-in confirmation again: that is
   the "new explicit confirmation".
2. **Subscribers need evidence.** A Brevo contact becomes a confirmed subscriber
   only with both a consent date and a consent source. A form application of
   Elementor always counts: its date is the consent date, its source is
   "club form". The date and source are kept on the subscription
   (`granted_at`, `confirmed_at`, `source`, `text_version` =
   `import:<file>:<consent source>`) and in the consent journal (source `import`).
3. **No consent data = skipped** and counted ("no consent data"). Never
   subscribed.
4. **Deduplication** by normalized (lower-cased, trimmed) e-mail across both
   files and the database. Of several consents the earliest wins. An address
   that already has a subscription of any status is left as it is
   ("already present"), except that a suppression withdraws an active one.
5. **Idempotent.** A second run with the same files writes nothing.

## The files

Both are CSV with a header line, UTF-8 (a BOM is fine), separator comma or
semicolon (the header line decides), column names case-insensitive.

### Brevo contacts export (`--brevo`)

| column | required | meaning |
|---|---|---|
| `email` (also `email_address`) | yes | the address |
| `name`, or `firstname` + `lastname` | no | the name for the organizer's export |
| `blacklisted` (also `emailblacklisted`) | no | `1`/`true`/`yes` = blacklisted, becomes a suppression |
| `unsubscribed` | no | `1`/`true`/`yes` or an unsubscribe date = becomes a suppression |
| `consent_date` (also `consent_at`, `opt_in_date`) | for a subscriber | `2025-03-04`, `2025-03-04 10:15:00`, RFC 3339, `04.03.2025` (day first) |
| `consent_source` (also `opt_in_source`, `source`) | for a subscriber | where the consent was given, any text, e.g. `popup 1185`, `checkout` |

The standard Brevo export has the first five; the two consent columns come from
the contact attributes the operator adds (or a list/segment export with a
"date added" attribute copied into `consent_date`). Rows with a date that cannot
be read are reported in the "problem" lines and treated as having no consent data.

### Elementor club-form export (`--elementor`)

| column | required | meaning |
|---|---|---|
| `date` (also `created_at`, `submitted_at`) | yes | date of the application = date of the consent |
| `name` | no | |
| `email` | yes | |
| `phone` | no | read but not stored: a subscription keeps no phone number |

The form's "consent" field is mandatory on the site, so every row of the table
`x4gd_e_submissions` for popup 1185 is a consent. Contact-form, private-event
and other Elementor submissions are not subscriptions: do not include them.

## The output

```
DRY RUN for organization <uuid>
rows read:            brevo=... elementor=...
invalid e-mail:       n (skipped)
unreadable date:      n (treated as no consent data)
duplicates merged:    n
no consent data:      n (skipped, not subscribed)
suppressions:         new=n withdrawn-existing=n already=n
subscribers:          new=n already-present=n blocked-by-suppression=n
```

Reconcile before the real run: `brevo + elementor rows` against the sums, and the
number of suppressions against the Brevo blacklist/unsubscribe counts.

## Order of work

1. Export the two files (operator, outside this repository).
2. `--dry-run`, reconcile the counts.
3. Real run (the same command without `--dry-run`).
4. **T0 re-run for late sign-ups.** In the night of the switch export both files
   again (everything, not only the new rows) and run the same command: rows
   already imported are "already present" and write nothing, new sign-ups are
   added, and anyone who unsubscribed in Brevo since the first run is withdrawn.
   Do this before WordPress/Brevo forms are switched off.

## Notes

- Every marketing letter of the platform carries the unsubscribe link of the
  subscription; its status changes at once.
- The existing retention rule (`marketing.Purge`) deletes a withdrawn
  subscription three years after it was withdrawn. An imported suppression is a
  withdrawn subscription dated on the day of the import, so the rule would erase
  the suppression after three years. Decide before then whether suppressions
  need to be kept longer (they are the proof that the address must not be mailed).

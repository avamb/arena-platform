# WP contract fixtures (wave W1)

- `bil24_orders_pseudonymized.json` — 68 real Bil24 order exports (118 tickets, 10 `REFUND`)
  from the Vino&Co site, structure and key sets untouched, buyer `fullName` / `phone` /
  `email` / `user.email` / `paymentBankId` / `actionLegalOwnerInn` replaced with deterministic
  synthetic values. Used by the BINDING key-set test of `internal/platform/bil24wire` and by
  the customer-import tests (#468). Never put un-pseudonymized exports here.
- `requests/<COMMAND>/<case>.json`, `golden/<COMMAND>/<case>.json`, `wp_receiver/` — produced
  by feature #450 from `08_architecture/18_bil24_compat_wave1_specification_ru.md` §7, §9, §15.

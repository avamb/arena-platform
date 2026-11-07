#!/usr/bin/env bash
# Routing check for traefik-dynamic-vino-site.yml (spec 62 section 14.2 item 4, runbook step 7).
# Sends GET requests only and reports which backend answered, SITE or WP, for every rule.
#
#   ops/vino-cutover/traefik-routing-check.sh <host> <ip> [port]
#
#   production after T0 : traefik-routing-check.sh vinoandco.events <lead-parser public ip>
#   rehearsal           : traefik-routing-check.sh vino-rehearsal.example.test <lead-parser ip> 443 -k
#   local whoami test   : MODE=whoami traefik-routing-check.sh vino.test 127.0.0.1 18443 -k
#
# <ip> is where --resolve points: the server itself, so the request skips Cloudflare and DNS.
# Extra arguments after the port (the port is then required) go to curl, e.g. -k for a name without a certificate.
# Backend detection: MODE=whoami reads the "Name:" line of traefik/whoami. Otherwise a response
# with an x-powered-by: PHP header is WP and anything else is the static site (nginx).
#
# The two WordPress-side probes (wc-api=allpay and vino_pay_back) are harmless on WP: the IPN
# handler logs a hit for order 0 and changes nothing, the back link of an unknown order redirects
# to the home page. They are still real WordPress requests: run them only once, after the route is
# live, and do not repeat them in a loop.
set -u
HOST="${1:?host}"; IP="${2:?ip}"; PORT="${3:-443}"; shift 3 2>/dev/null || shift $#
EXTRA=("$@")
MODE="${MODE:-real}"
fail=0; n=0

probe() {  # probe <expected SITE|WP> <path-with-query> [extra curl args...]
  local want="$1" path="$2"; shift 2
  local out got
  out=$(curl -s -o /tmp/vc_body.$$ -D - --max-time 15 --resolve "${HOST}:${PORT}:${IP}" "${EXTRA[@]}" "$@" "https://${HOST}:${PORT}${path}" 2>/dev/null) || out=""
  if [ "$MODE" = whoami ]; then
    got=$(grep -m1 '^Name:' /tmp/vc_body.$$ | sed 's/Name: *//; s/\r//')
  elif echo "$out" | grep -qi '^x-powered-by: *PHP'; then got=WP
  elif [ -z "$out" ]; then got=NO-ANSWER
  else got=SITE; fi
  n=$((n+1))
  if [ "$got" = "$want" ]; then printf 'ok    %-5s %s\n' "$got" "$path"
  else printf 'FAIL  want %-5s got %-9s %s\n' "$want" "$got" "$path"; fail=$((fail+1)); fi
}

echo "== always the new site"
probe SITE /order-status/
probe SITE /en/order-status/
probe SITE /he/order-status/
probe SITE '/order-status/?key=wc_order_x&order_id=1'
probe SITE /my-tickets/
probe SITE '/my-tickets/?t=abc'
probe SITE /en/my-tickets/
probe SITE /he/my-tickets/
probe SITE /_site/assets/x.js
echo "== the rest of the host is the new site"
probe SITE /
probe SITE /en/
probe SITE /he/
probe SITE /upcoming-events/
probe SITE /event/izrail-takie-raznye-belye/
probe SITE /robots.txt
probe SITE '/?wc-api=other'
probe SITE '/?vino_pay_back='
echo "== old order pages WITHOUT old-order parameters: the site (301 there)"
probe SITE /payment-successful/
probe SITE /my-account/
probe SITE /en/payment-successful/
probe SITE /en/my-account/
probe SITE /he/hthshlw-hslyx/
probe SITE /he/hxshbw-shly/
probe SITE '/payment-successful/?utm_source=x'
echo "== old order pages WITH old-order parameters: WordPress"
for q in 'order_id=1' 'order_key=wc_order_x' 'key=wc_order_x' 'login=a'; do
  probe WP "/payment-successful/?$q"
  probe WP "/my-account/?$q"
  probe WP "/en/payment-successful/?$q"
  probe WP "/en/my-account/?$q"
  probe WP "/he/hthshlw-hslyx/?$q"
  probe WP "/he/hxshbw-shly/?$q"
done
probe WP '/payment-successful/order-received/1/?key=wc_order_x'
probe WP '/my-account/lost-password/?show-reset-form=true&id=1&key=x'
echo "== webhooks and old payment returns: WordPress"
probe WP '/?wc-api=allpay'
probe WP '/index.php?wc-api=allpay'
probe WP /wc-api/allpay/
probe WP '/wp-json/lops/v1/brevo'
probe WP '/?vino_pay_back=0&key=none'
echo "== operator: WordPress"
probe WP /wp-login.php
probe WP '/wp-login.php?action=lostpassword'
probe WP /wp-admin/
probe WP /wp-admin/admin-ajax.php
probe SITE /wp-json/
probe SITE /wp-content/plugins/woocommerce/readme.txt
probe SITE /wp-includes/js/jquery/jquery.min.js
probe WP /wp-json/ -H 'Cookie: wordpress_logged_in_x=1'
probe WP /wp-includes/js/jquery/jquery.min.js -H 'Cookie: wordpress_logged_in_x=1'
probe WP /wp-content/plugins/woocommerce/readme.txt -H 'Cookie: wordpress_logged_in_x=1'
probe SITE /xmlrpc.php
rm -f /tmp/vc_body.$$
echo; echo "$n probes, $fail failed"; [ "$fail" -eq 0 ]

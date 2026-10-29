#!/bin/sh
# Asserts the headers of the Telegram mini app location of a running
# event-center container (spec 44, RT-12): /tg/edit and a deeper /tg/ path get
# index.html with the tg headers (no X-Frame-Options, frame-ancestors naming
# Telegram), while / and /index.html still send X-Frame-Options: DENY and
# frame-ancestors 'none'. Without the named location @tg_index the request is
# matched again as /index.html and comes back with DENY, which this script
# catches.
#
# Usage: apps/event-center/scripts/check-headers-tg.sh http://127.0.0.1:8091
set -eu
base="${1:?base URL of the container}"
status=0

lower() { tr 'A-Z' 'a-z'; }

main_index="$(curl -fsS "$base/")"

for path in /tg "/tg?next=%2Fo%2Fx" /tg/edit "/tg/edit?x=1" "/tg/invitations?session=x&event=y" /tg/other/deep/link; do
  headers="$(curl -fsSI "$base$path")"
  body="$(curl -fsS "$base$path")"
  for want in \
    "content-security-policy: default-src 'none'" \
    "script-src 'self';" \
    "style-src 'self';" \
    "frame-ancestors https://web.telegram.org" \
    "x-content-type-options: nosniff" \
    "referrer-policy: no-referrer" \
    "cache-control: no-store"; do
    if ! printf '%s\n' "$headers" | lower | grep -qF "$want"; then
      echo "FAIL: $path has no \"$want\""
      status=1
    fi
  done
  for unwanted in "x-frame-options" "frame-ancestors 'none'"; do
    if printf '%s\n' "$headers" | lower | grep -qF "$unwanted"; then
      echo "FAIL: $path has \"$unwanted\""
      status=1
    fi
  done
  if [ "$body" != "$main_index" ]; then
    echo "FAIL: $path is not the same index.html as /"
    status=1
  fi
done

for path in / /index.html /o/any/events /tgx; do
  headers="$(curl -fsSI "$base$path")"
  for want in "x-frame-options: deny" "frame-ancestors 'none'"; do
    if ! printf '%s\n' "$headers" | lower | grep -qF "$want"; then
      echo "FAIL: $path has no \"$want\""
      status=1
    fi
  done
  if printf '%s\n' "$headers" | lower | grep -qF "telegram.org"; then
    echo "FAIL: $path names Telegram in its headers"
    status=1
  fi
done

# The vendored Telegram script (loaded by the page, never by index.html) is
# served from this origin as JavaScript; its path is the one the page asks for.
script="$(grep -o '/vendor/telegram-web-app.[0-9a-f]*.js' "$(dirname "$0")/../src/lib/tgEdit.ts" | head -n 1)"
if [ -z "$script" ]; then
  echo "FAIL: no vendored Telegram script path in src/lib/tgEdit.ts"
  exit 1
fi
if ! curl -fsSI "$base$script" | lower | grep -qF "content-type: application/javascript"; then
  echo "FAIL: $script is not served as JavaScript"
  status=1
fi

if [ "$status" -eq 0 ]; then
  echo "OK: /tg and /tg/edit have the tg headers and index.html, / /index.html /o/... keep X-Frame-Options, $script is served"
fi
exit "$status"

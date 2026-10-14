#!/bin/sh
# Asserts the security headers of a running event-center container on the
# entry page, a deep app link and a hashed asset (review finding B1: a
# location with its own add_header used to drop every server-level header).
#
# Usage: apps/event-center/scripts/check-headers.sh http://127.0.0.1:8091
set -eu
base="${1:?base URL of the container}"
asset="$(curl -fsS "$base/" | grep -o '/assets/index-[A-Za-z0-9_-]*\.js' | head -n 1)"
if [ -z "$asset" ]; then
  echo "FAIL: no hashed index script in $base/"
  exit 1
fi
status=0
for path in / /o/any/events /index.html "/reset-password?token=check" "/accept-invite?token=check" /sw.js /manifest.webmanifest "$asset"; do
  headers="$(curl -fsSI "$base$path")"
  for want in \
    "content-security-policy: default-src 'none'" \
    "frame-ancestors 'none'" \
    "x-frame-options: deny" \
    "x-content-type-options: nosniff" \
    "referrer-policy: no-referrer"; do
    if ! printf '%s\n' "$headers" | tr 'A-Z' 'a-z' | grep -qF "$want"; then
      echo "FAIL: $path has no \"$want\""
      status=1
    fi
  done
done
if [ "$status" -eq 0 ]; then
  echo "OK: security headers on /, a deep link, /index.html, the password pages, /sw.js, the manifest and $asset"
fi
exit "$status"

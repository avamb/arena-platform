#!/usr/bin/env bash
# Route probes for a site cutover (spec 63 section 18).
#
#   ops/site-cutover/routing-check.sh <probes file> <host> <ip> <port> [extra curl args]
#
#   production after T0 : routing-check.sh ops/site-cutover/probes/lampyris.probes lampyrisevents.com <lead-parser public ip> 443
#   rehearsal           : routing-check.sh ops/site-cutover/probes/lampyris.probes <fake host> <ip> 443 -k
#   plan only, no sends : DRY=1 routing-check.sh ops/site-cutover/probes/lampyris.probes example.test 127.0.0.1 443
#
# <ip> is where --resolve points (the server itself), so the request skips Cloudflare and DNS.
# Extra curl arguments after the port go to every request, e.g. -k for a name without a certificate.
#
# Probes file: one probe per line, "<SITE|WP> <path with query> [curl args]". Blank lines and lines
# starting with # are skipped. SITE means the new site answered, WP means WordPress did.
# Backend detection: a response with x-powered-by: PHP or an Apache server header is WordPress,
# anything else that answered is the static site (nginx). No answer at all is NO-ANSWER.
#
# Safety: only GET and HEAD. A probe with a request body or another method is refused before it is
# sent, so a probe can never reach a live webhook with a POST (spec 63 lesson U-19). Use -I for HEAD.
set -u

PROBES="${1:?probes file}"; HOST="${2:?host}"; IP="${3:?ip}"; PORT="${4:?port}"
shift 4
EXTRA=("$@")
DRY="${DRY:-0}"
n=0; fail=0; refused=0
body="$(mktemp)"
trap 'rm -f "$body"' EXIT

probe() {  # probe <SITE|WP> <path> [curl args...]
  local want="$1" path="$2" a
  shift 2
  for a in "$@"; do
    case "$a" in
      -X|--request*|-d|--data*|--json|-F|--form*|-T|--upload-file*)
        printf 'REFUSED %-5s %s: %s would send a body or another method\n' "$want" "$path" "$a"
        refused=$((refused+1)); return ;;
    esac
  done
  n=$((n+1))
  if [ "$DRY" = 1 ]; then
    printf 'plan  want %-5s %s %s\n' "$want" "$path" "$*"
    return
  fi
  local out got
  out=$(curl -s -o "$body" -D - --max-time 15 --resolve "${HOST}:${PORT}:${IP}" ${EXTRA[@]+"${EXTRA[@]}"} "$@" "https://${HOST}:${PORT}${path}" 2>/dev/null) || out=""
  if [ -z "$out" ]; then got=NO-ANSWER
  elif echo "$out" | grep -qiE '^(x-powered-by: *PHP|server: *Apache)'; then got=WP
  else got=SITE; fi
  if [ "$got" = "$want" ]; then
    printf 'ok    %-5s %s\n' "$got" "$path"
  else
    printf 'FAIL  want %-5s got %-9s %s\n' "$want" "$got" "$path"
    fail=$((fail+1))
  fi
}

line_no=0
while IFS= read -r line || [ -n "$line" ]; do
  line_no=$((line_no+1))
  case "$line" in ''|\#*) continue ;; esac
  read -r -a f <<< "$line"
  want="${f[0]}"; path="${f[1]:-}"
  args=("${f[@]:2}")
  case "$want" in SITE|WP) ;; *) echo "bad probe at line $line_no: $line" >&2; exit 2 ;; esac
  if [ -z "$path" ]; then echo "bad probe at line $line_no: $line" >&2; exit 2; fi
  probe "$want" "$path" ${args[@]+"${args[@]}"}
done < "$PROBES"

echo; echo "$n probes, $fail failed, $refused refused"
[ "$fail" -eq 0 ] && [ "$refused" -eq 0 ]

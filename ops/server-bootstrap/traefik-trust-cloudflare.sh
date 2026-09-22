#!/usr/bin/env bash
# Make the Dokploy Traefik on this host keep the visitor address Cloudflare
# puts in X-Forwarded-For. Idempotent; keeps a dated backup of the config.
#
#   ssh arena-prod 'bash -s' < ops/server-bootstrap/traefik-trust-cloudflare.sh
#
# WHY (docs/loadtest/2026-09-22_server_step2_ru.md §3.2, §4.7, §6): Traefik
# drops a client-supplied X-Forwarded-For unless the peer is listed in
# forwardedHeaders.trustedIPs. Behind Cloudflare that leaves arena-api
# with the edge node's address only, so PUBLIC_API_IP_RATE_LIMIT (600/min
# per visitor) throttles every buyer behind one edge as if they were one
# person. After this script, set TRUSTED_PROXY_COUNT: "2" (Cloudflare +
# Traefik) in the backend-prod Raw compose and deploy — not before: with
# count 2 and no trusted ranges every visitor would resolve to Traefik.
set -euo pipefail

CFG=/etc/dokploy/traefik/traefik.yml
[ -f "$CFG" ] || { echo "no $CFG on this host"; exit 1; }
if grep -q forwardedHeaders "$CFG"; then
  echo "already configured: forwardedHeaders present in $CFG"; exit 0
fi

# Current edge ranges, straight from Cloudflare.
RANGES=$(curl -fsS --max-time 20 https://api.cloudflare.com/client/v4/ips \
  | python3 -c 'import sys,json; r=json.load(sys.stdin)["result"]; print("\n".join(r["ipv4_cidrs"]+r["ipv6_cidrs"]))')
COUNT=$(printf '%s\n' "$RANGES" | grep -c .)
[ "$COUNT" -ge 15 ] || { echo "suspicious range list ($COUNT entries), aborting"; exit 1; }

cp -a "$CFG" "$CFG.bak-$(date +%F-%H%M)"

python3 - "$CFG" "$RANGES" <<'EOF'
import sys
cfg, ranges = sys.argv[1], sys.argv[2].split()
s = open(cfg).read()
block = ("    forwardedHeaders:\n"
         "      # Cloudflare edge ranges (api.cloudflare.com/client/v4/ips): keep the\n"
         "      # visitor address CF puts in X-Forwarded-For; arena-api reads it with\n"
         "      # TRUSTED_PROXY_COUNT=2 (Cloudflare + Traefik). Re-run the script to refresh.\n"
         "      trustedIPs:\n" + "".join(f"        - {r}\n" for r in ranges))
for ep, addr in (("web", ":80"), ("websecure", ":443")):
    anchor = f"  {ep}:\n    address: {addr}\n"
    assert anchor in s, f"entryPoint {ep} not found in {cfg}"
    s = s.replace(anchor, anchor + block, 1)
open(cfg, "w").write(s)
print(f"patched {cfg}: {len(ranges)} ranges under web and websecure")
EOF

docker restart dokploy-traefik >/dev/null
sleep 5
docker ps --format '{{.Names}} {{.Status}}' | grep dokploy-traefik
if docker logs --since 30s dokploy-traefik 2>&1 | grep -qi 'error'; then
  echo "traefik logged errors after restart — check: docker logs --since 1m dokploy-traefik"
  echo "rollback: cp $CFG.bak-* $CFG && docker restart dokploy-traefik"
  exit 1
fi
echo "done. Next: TRUSTED_PROXY_COUNT: \"2\" in the backend-prod Raw compose, then Deploy."

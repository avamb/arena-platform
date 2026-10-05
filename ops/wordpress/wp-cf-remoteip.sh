#!/bin/sh
# Makes Apache in every WordPress container log (and expose as REMOTE_ADDR) the real visitor address.
# Traffic comes Cloudflare -> Traefik -> container, so without this every request shows a Cloudflare edge address.
# Cloudflare sends the visitor in CF-Connecting-IP; only the internal docker networks may set it.
# Re-applied on every container start (a Dokploy deploy recreates the container and drops the file).
# It only logs the address: a request that skips Cloudflare could forge the header, which at worst falsifies the log line.
# Per-container extras: every *.conf in /etc/wp-apache-extra/<container name>/ on the host is copied into that container's
# conf-enabled as zz-extra-<file> in the same pass (minimaldeco: Apache worker cap and an xmlrpc.php block, which its
# Dokploy compose cannot carry without a redeploy).
# Fail-safe: a failed configtest removes every file this script wrote. Run once: wp-cf-remoteip.sh --once
CONF='RemoteIPHeader CF-Connecting-IP
RemoteIPInternalProxy 10.0.0.0/8
RemoteIPInternalProxy 172.16.0.0/12
RemoteIPInternalProxy 192.168.0.0/16
RemoteIPInternalProxy 169.254.0.0/16
RemoteIPInternalProxy 127.0.0.0/8'
F=/etc/apache2/conf-enabled/zz-cf-remoteip.conf

apply() {
  n=$1
  i=0
  while [ $i -lt 20 ]; do
    docker exec "$n" test -d /etc/apache2/conf-enabled 2>/dev/null && break
    i=$((i+1)); sleep 3
  done
  docker exec "$n" test -x /usr/sbin/apache2ctl 2>/dev/null || return 0
  printf '%s\n' "$CONF" | docker exec -i "$n" sh -c "cat > $F" || return 1
  if [ -d "/etc/wp-apache-extra/$n" ]; then
    for x in /etc/wp-apache-extra/"$n"/*.conf; do
      [ -f "$x" ] || continue
      docker exec -i "$n" sh -c "cat > /etc/apache2/conf-enabled/zz-extra-$(basename "$x")" < "$x"
    done
  fi
  if docker exec "$n" apache2ctl configtest >/dev/null 2>&1; then
    docker exec "$n" apache2ctl graceful >/dev/null 2>&1   # fails harmlessly while apache is still starting, it reads the file then
    logger -t wp-cf-remoteip "applied to $n"
  else
    docker exec "$n" sh -c "rm -f $F /etc/apache2/conf-enabled/zz-extra-*.conf"
    logger -t wp-cf-remoteip "configtest FAILED in $n, file removed"
  fi
}

all() { for n in $(docker ps --format '{{.Names}}' | grep -E -- '-wordpress-[0-9]+$'); do apply "$n"; done; }

[ "${1:-}" = "--once" ] && { all; exit 0; }
all
docker events --filter type=container --filter event=start --format '{{.Actor.Attributes.name}}' | while read -r n; do
  case "$n" in *-wordpress-[0-9]*) apply "$n" & ;; esac
done

#!/usr/bin/env bash
# Baseline hardening + Docker for a fresh Hetzner Cloud server (Ubuntu 24.04 / Debian 13).
# Idempotent. Run as root ONCE after the first login:  bash bootstrap.sh <new-hostname>
# Does NOT install Dokploy: the server is attached to the existing Dokploy panel as a
# "remote server" (Settings -> Servers -> Add), which installs Traefik and the swarm bits.
set -euo pipefail

NEW_HOSTNAME="${1:?usage: bootstrap.sh <hostname>}"

echo "== hostname"
hostnamectl set-hostname "$NEW_HOSTNAME"
grep -q "$NEW_HOSTNAME" /etc/hosts || echo "127.0.1.1 $NEW_HOSTNAME" >> /etc/hosts

echo "== packages"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq ufw fail2ban unattended-upgrades ca-certificates curl gnupg rclone age jq

echo "== ssh: keys only"
install -d -m 755 /etc/ssh/sshd_config.d
cat > /etc/ssh/sshd_config.d/10-hardening.conf <<'EOF'
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin prohibit-password
X11Forwarding no
MaxAuthTries 3
EOF
# cloud-init may ship a drop-in that re-enables passwords; ours must win.
rm -f /etc/ssh/sshd_config.d/50-cloud-init.conf
sshd -t && systemctl reload ssh 2>/dev/null || systemctl reload sshd

echo "== swap 2G (the API build of admin-web needs headroom)"
if ! swapon --show | grep -q swapfile; then
  fallocate -l 2G /swapfile && chmod 600 /swapfile && mkswap /swapfile >/dev/null && swapon /swapfile
  grep -q '/swapfile' /etc/fstab || echo '/swapfile none swap sw 0 0' >> /etc/fstab
fi
sysctl -w vm.swappiness=10 >/dev/null; echo 'vm.swappiness=10' > /etc/sysctl.d/60-swappiness.conf

echo "== journald cap"
install -d /etc/systemd/journald.conf.d
printf '[Journal]\nSystemMaxUse=300M\n' > /etc/systemd/journald.conf.d/size.conf
systemctl restart systemd-journald

echo "== docker"
if ! command -v docker >/dev/null; then
  curl -fsSL https://get.docker.com | sh
fi
install -d /etc/docker
cat > /etc/docker/daemon.json <<'EOF'
{
  "log-driver": "json-file",
  "log-opts": { "max-size": "20m", "max-file": "3" },
  "live-restore": false
}
EOF
systemctl restart docker

echo "== ufw (host firewall; the Hetzner Cloud Firewall is the outer layer)"
# NOTE: Docker-published ports bypass ufw INPUT. Never publish anything except 80/443
# from containers; the Hetzner Cloud Firewall must restrict 80/443 to Cloudflare ranges.
ufw default deny incoming
ufw default allow outgoing
ufw allow 22/tcp
ufw allow 80/tcp
ufw allow 443/tcp
ufw allow 443/udp
ufw --force enable

echo "== fail2ban"
cat > /etc/fail2ban/jail.d/sshd.local <<'EOF'
[sshd]
enabled = true
maxretry = 4
bantime = 1h
findtime = 10m
EOF
systemctl enable --now fail2ban
systemctl restart fail2ban

echo "== unattended upgrades"
cat > /etc/apt/apt.conf.d/20auto-upgrades <<'EOF'
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
EOF

echo "== done"
echo "hostname: $(hostname)  docker: $(docker --version | cut -d, -f1)  ufw: $(ufw status | head -1)"
echo "NEXT: attach this server in Dokploy (Settings -> Servers), then deploy the compose."

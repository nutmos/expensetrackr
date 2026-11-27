#!/usr/bin/env bash
# Deploys expensetrackr on the Lightsail instance. Run on the instance by
# .github/workflows/deploy.yml (over SSH, as ec2-user), from ~/expensetrackr:
#
#   IMAGE=ghcr.io/nutmos/expensetrackr:sha-abc1234 SITE_ADDRESS=1.2.3.4 ./deploy.sh
#
# Optional, for a domain proxied by Cloudflare (orange cloud, docs/deploy.md):
#
#   CLOUDFLARE=true     trust CF-Connecting-IP from Cloudflare's IP ranges
#                       (real visitor address in the app) and refuse
#                       connections that do not come through Cloudflare
#   CF_ORIGIN_CERT      PEM certificate and key of a Cloudflare Origin CA
#   CF_ORIGIN_KEY       certificate; served instead of a Let's Encrypt one
#                       (Cloudflare SSL/TLS mode "Full (strict)")
#
# Optional, encryption at rest (docs/encryption.md):
#
#   EXPENSE_ENCRYPTION_KEY       key that encrypts personal data in the
#                                database; once set, it is kept in app.env
#                                (mode 600) next to this script, so a later
#                                deploy without it keeps using it
#   EXPENSE_ENCRYPTION_OLD_KEYS  earlier keys, comma-separated, while rotating
#
# Three containers on one Docker network, no compose needed:
#
#   expensetrackr-db     postgres, data in the "expensetrackr-pgdata" volume
#   expensetrackr-app    the server (port 8080, not published on the host)
#   expensetrackr-caddy  HTTPS on :443 (and :80 for redirects/ACME), proxies to app
#
# The Postgres password is generated on the first run and kept in .env
# (mode 600) next to this script. If the new app container does not become
# healthy, the previous image is started again and the script fails.
set -euo pipefail

: "${IMAGE:?IMAGE is required, e.g. ghcr.io/owner/repo:sha-abc1234}"
: "${SITE_ADDRESS:?SITE_ADDRESS is required (domain name or public IP)}"

CLOUDFLARE=${CLOUDFLARE:-false}
[[ $CLOUDFLARE == true || $CLOUDFLARE == false ]] || { echo "CLOUDFLARE must be true or false, not '$CLOUDFLARE'" >&2; exit 1; }
if [[ -n "${CF_ORIGIN_CERT:-}" && -z "${CF_ORIGIN_KEY:-}" ]] || [[ -z "${CF_ORIGIN_CERT:-}" && -n "${CF_ORIGIN_KEY:-}" ]]; then
  echo "CF_ORIGIN_CERT and CF_ORIGIN_KEY go together; set both or neither" >&2
  exit 1
fi

cd "$(dirname "$0")"

NET=expensetrackr
DB=expensetrackr-db
APP=expensetrackr-app
CADDY=expensetrackr-caddy
PG_IMAGE=postgres:18.6-alpine
CADDY_IMAGE=caddy:2.10-alpine

docker() { sudo docker "$@"; }

# --- Docker -----------------------------------------------------------------
if ! type -P docker >/dev/null 2>&1; then
  echo "==> Installing Docker"
  if command -v dnf >/dev/null 2>&1; then
    sudo dnf install -y docker
  elif command -v apt-get >/dev/null 2>&1; then
    sudo apt-get update -y && sudo apt-get install -y docker.io
  else
    echo "No dnf or apt-get; install Docker on the instance first." >&2
    exit 1
  fi
fi
sudo systemctl enable --now docker

# --- Secrets ----------------------------------------------------------------
if [[ ! -f .env ]]; then
  echo "==> Generating database password (.env)"
  umask 077
  echo "POSTGRES_PASSWORD=$(head -c 32 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 32)" > .env
fi
# shellcheck disable=SC1091
source .env

# Encryption key(s) for the app (docker run --env-file). A key given to this
# run replaces the stored one; without one, the stored file is reused, since
# an encrypted database does not start without its key.
if [[ -n "${EXPENSE_ENCRYPTION_KEY:-}" ]]; then
  echo "==> Storing the encryption key (app.env)"
  (
    umask 077
    printf 'EXPENSE_ENCRYPTION_KEY=%s\n' "$EXPENSE_ENCRYPTION_KEY" > app.env
    if [[ -n "${EXPENSE_ENCRYPTION_OLD_KEYS:-}" ]]; then
      printf 'EXPENSE_ENCRYPTION_OLD_KEYS=%s\n' "$EXPENSE_ENCRYPTION_OLD_KEYS" >> app.env
    fi
  )
elif [[ ! -f app.env ]]; then
  ( umask 077; : > app.env )
fi

# --- Registry login (ghcr.io, short-lived token from the workflow) ----------
if [[ -n "${GHCR_TOKEN:-}" ]]; then
  echo "$GHCR_TOKEN" | docker login ghcr.io -u "${GHCR_USER:-github}" --password-stdin >/dev/null
fi

echo "==> Pulling $IMAGE"
docker pull "$IMAGE"
docker pull -q "$PG_IMAGE" >/dev/null
docker pull -q "$CADDY_IMAGE" >/dev/null
[[ -n "${GHCR_TOKEN:-}" ]] && docker logout ghcr.io >/dev/null || true

docker network inspect "$NET" >/dev/null 2>&1 || docker network create "$NET" >/dev/null
# The app believes X-Real-IP / X-Forwarded-Proto only from this network,
# i.e. from Caddy (EXPENSE_TRUSTED_PROXIES).
NET_SUBNETS=$(docker network inspect -f '{{range .IPAM.Config}}{{.Subnet}},{{end}}' "$NET")
NET_SUBNETS=${NET_SUBNETS%,}
[[ -n $NET_SUBNETS ]] || { echo "Docker network $NET has no subnet" >&2; exit 1; }

# --- Postgres ---------------------------------------------------------------
if ! docker container inspect "$DB" >/dev/null 2>&1; then
  echo "==> Starting Postgres"
  docker run -d --name "$DB" --network "$NET" --restart unless-stopped \
    -e POSTGRES_USER=expense \
    -e POSTGRES_PASSWORD="$POSTGRES_PASSWORD" \
    -e POSTGRES_DB=expense \
    -v expensetrackr-pgdata:/var/lib/postgresql \
    --health-cmd "pg_isready -U expense -d expense" \
    --health-interval 2s --health-timeout 3s --health-retries 30 \
    "$PG_IMAGE" >/dev/null
else
  docker start "$DB" >/dev/null
fi

wait_healthy() { # name, timeout seconds
  local name=$1 deadline=$((SECONDS + $2)) status
  while (( SECONDS < deadline )); do
    status=$(docker inspect -f '{{.State.Health.Status}}' "$name" 2>/dev/null || echo missing)
    [[ $status == healthy ]] && return 0
    [[ $status == missing ]] && return 1
    sleep 2
  done
  return 1
}
wait_healthy "$DB" 90 || { echo "Postgres did not become healthy" >&2; docker logs --tail 50 "$DB" >&2; exit 1; }

# --- App --------------------------------------------------------------------
run_app() {
  docker run -d --name "$APP" --network "$NET" --restart unless-stopped \
    -e EXPENSE_DATABASE_URL="postgres://expense:${POSTGRES_PASSWORD}@${DB}:5432/expense?sslmode=disable" \
    -e EXPENSE_TRUSTED_PROXIES="$NET_SUBNETS" \
    --env-file app.env \
    "$1" >/dev/null
}

PREVIOUS=$(docker inspect -f '{{.Config.Image}}' "$APP" 2>/dev/null || true)
echo "==> Deploying $IMAGE${PREVIOUS:+ (was $PREVIOUS)}"
docker rm -f "$APP" >/dev/null 2>&1 || true
run_app "$IMAGE"

if ! wait_healthy "$APP" 120; then
  echo "New app container is not healthy; logs:" >&2
  docker logs --tail 100 "$APP" >&2 || true
  docker rm -f "$APP" >/dev/null 2>&1 || true
  if [[ -n "$PREVIOUS" ]]; then
    echo "==> Rolling back to $PREVIOUS" >&2
    run_app "$PREVIOUS"
    wait_healthy "$APP" 120 || echo "Rollback is not healthy either" >&2
  fi
  exit 1
fi

# --- Caddy (HTTPS on 443) ---------------------------------------------------
# Recreated only when its configuration changes (Caddyfile, site address,
# TLS mode, Cloudflare ranges, origin certificate), so certificates (kept in
# the caddy-data volume) and open connections survive app deploys.
#
# TLS_MODE: a bare IP gets a certificate from Caddy's internal CA; a domain
# gets a Cloudflare Origin CA certificate when one is given, else one from
# Let's Encrypt (ACME).
mkdir -p certs
if [[ -n "${CF_ORIGIN_CERT:-}" ]]; then
  ( umask 077
    printf '%s\n' "$CF_ORIGIN_CERT" > certs/origin.pem
    printf '%s\n' "$CF_ORIGIN_KEY" > certs/origin.key )
else
  rm -f certs/origin.pem certs/origin.key
fi
if [[ $SITE_ADDRESS =~ ^[0-9.]+$ || $SITE_ADDRESS == *:* ]]; then
  TLS_MODE=internal
  [[ $CLOUDFLARE == true ]] && echo "warning: CLOUDFLARE=true with a bare IP; Cloudflare proxies only DNS names" >&2
elif [[ -f certs/origin.pem ]]; then
  TLS_MODE=origin
else
  TLS_MODE=acme
fi

# Cloudflare's published IP ranges (https://www.cloudflare.com/ips/), with
# the list as of this script as a fallback if they cannot be fetched.
CF_RANGES_FALLBACK="173.245.48.0/20 103.21.244.0/22 103.22.200.0/22 103.31.4.0/22
141.101.64.0/18 108.162.192.0/18 190.93.240.0/20 188.114.96.0/20 197.234.240.0/22
198.41.128.0/17 162.158.0.0/15 104.16.0.0/13 104.24.0.0/14 172.64.0.0/13
131.0.72.0/22 2400:cb00::/32 2606:4700::/32 2803:f800::/32 2405:b500::/32
2405:8100::/32 2a06:98c0::/29 2c0f:f248::/32"
cloudflare_ranges() {
  local v4 v6
  v4=$(curl -fsS --max-time 10 https://www.cloudflare.com/ips-v4 2>/dev/null) || v4=
  v6=$(curl -fsS --max-time 10 https://www.cloudflare.com/ips-v6 2>/dev/null) || v6=
  # Only accept a list that looks like CIDR ranges and nothing else.
  if [[ -n $v4 && -n $v6 ]] && ! printf '%s\n%s\n' "$v4" "$v6" | grep -qvE '^([0-9.]+/[0-9]+|[0-9a-fA-F:]+/[0-9]+)?$'; then
    echo $v4 $v6
  else
    echo "warning: could not fetch Cloudflare IP ranges; using the built-in list" >&2
    echo $CF_RANGES_FALLBACK
  fi
}
if [[ $CLOUDFLARE == true ]]; then
  CF_RANGES=$(cloudflare_ranges)
  TRUSTED_PROXIES=$CF_RANGES
  # private_ranges: the smoke test below reaches Caddy through Docker's
  # port forwarding, from the bridge gateway.
  ALLOWED_REMOTES="$CF_RANGES private_ranges"
else
  TRUSTED_PROXIES=127.0.0.1/32 # nobody: no proxy in front of Caddy
  ALLOWED_REMOTES="0.0.0.0/0 ::/0"
fi

CADDY_CONF=$(printf '%s|' "$SITE_ADDRESS" "$TLS_MODE" "$TRUSTED_PROXIES" "$ALLOWED_REMOTES" \
  "$(cat Caddyfile certs/origin.pem 2>/dev/null | sha256sum | cut -d' ' -f1)" | sha256sum | cut -d' ' -f1)
CURRENT_CONF=$(docker inspect -f '{{index .Config.Labels "conf"}}' "$CADDY" 2>/dev/null || true)
if [[ "$CADDY_CONF" != "$CURRENT_CONF" ]]; then
  echo "==> (Re)starting Caddy for $SITE_ADDRESS (tls: $TLS_MODE, cloudflare: $CLOUDFLARE)"
  docker rm -f "$CADDY" >/dev/null 2>&1 || true
  docker run -d --name "$CADDY" --network "$NET" --restart unless-stopped \
    --label "conf=$CADDY_CONF" \
    -p 80:80 -p 443:443 -p 443:443/udp \
    -e SITE_ADDRESS="$SITE_ADDRESS" \
    -e TLS_MODE="$TLS_MODE" \
    -e TRUSTED_PROXIES="$TRUSTED_PROXIES" \
    -e ALLOWED_REMOTES="$ALLOWED_REMOTES" \
    -v "$PWD/Caddyfile:/etc/caddy/Caddyfile:ro" \
    -v "$PWD/certs:/certs:ro" \
    -v expensetrackr-caddy-data:/data \
    -v expensetrackr-caddy-config:/config \
    "$CADDY_IMAGE" >/dev/null
else
  docker start "$CADDY" >/dev/null
fi

# --- Smoke test through HTTPS -----------------------------------------------
# -k: with a bare IP Caddy serves a certificate from its own internal CA, and
# a Cloudflare Origin CA certificate is trusted only by Cloudflare.
# Up to 3 minutes: with a domain, the first Let's Encrypt certificate is
# obtained while we wait.
for _ in $(seq 1 90); do
  if curl -fsSk --resolve "${SITE_ADDRESS}:443:127.0.0.1" "https://${SITE_ADDRESS}/api/healthz" >/dev/null 2>&1; then
    echo "==> Healthy at https://${SITE_ADDRESS}"
    docker image prune -f >/dev/null
    exit 0
  fi
  sleep 2
done
echo "App is healthy but https://${SITE_ADDRESS}/api/healthz does not answer through Caddy:" >&2
curl -sSk -v --resolve "${SITE_ADDRESS}:443:127.0.0.1" "https://${SITE_ADDRESS}/api/healthz" -o /dev/null 2>&1 | grep -E '^\*|^< HTTP|curl:' >&2 || true
docker logs --tail 50 "$CADDY" >&2 || true
exit 1

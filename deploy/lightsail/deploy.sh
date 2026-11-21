#!/usr/bin/env bash
# Deploys expensetrackr on the Lightsail instance. Run on the instance by
# .github/workflows/deploy.yml (over SSH, as ec2-user), from ~/expensetrackr:
#
#   IMAGE=ghcr.io/nutmos/expensetrackr:sha-abc1234 SITE_ADDRESS=1.2.3.4 ./deploy.sh
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
# Recreated only when the Caddyfile or site address changes, so certificates
# (kept in the caddy-data volume) and open connections survive app deploys.
# A bare IP gets a certificate from Caddy's internal CA; a domain gets one
# from Let's Encrypt (ACME).
if [[ $SITE_ADDRESS =~ ^[0-9.]+$ || $SITE_ADDRESS == *:* ]]; then
  TLS_ISSUER=internal
else
  TLS_ISSUER=acme
fi
CADDY_CONF="$SITE_ADDRESS|$TLS_ISSUER|$(sha256sum Caddyfile | cut -d' ' -f1)"
CURRENT_CONF=$(docker inspect -f '{{index .Config.Labels "conf"}}' "$CADDY" 2>/dev/null || true)
if [[ "$CADDY_CONF" != "$CURRENT_CONF" ]]; then
  echo "==> (Re)starting Caddy for $SITE_ADDRESS"
  docker rm -f "$CADDY" >/dev/null 2>&1 || true
  docker run -d --name "$CADDY" --network "$NET" --restart unless-stopped \
    --label "conf=$CADDY_CONF" \
    -p 80:80 -p 443:443 -p 443:443/udp \
    -e SITE_ADDRESS="$SITE_ADDRESS" \
    -e TLS_ISSUER="$TLS_ISSUER" \
    -v "$PWD/Caddyfile:/etc/caddy/Caddyfile:ro" \
    -v expensetrackr-caddy-data:/data \
    -v expensetrackr-caddy-config:/config \
    "$CADDY_IMAGE" >/dev/null
else
  docker start "$CADDY" >/dev/null
fi

# --- Smoke test through HTTPS -----------------------------------------------
# -k: with a bare IP Caddy serves a certificate from its own internal CA.
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

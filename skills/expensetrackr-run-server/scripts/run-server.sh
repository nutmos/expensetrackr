#!/usr/bin/env bash
# run-server.sh: start, check and stop the local expensetrackr stack (the
# server built from the Dockerfile + Postgres) with Docker Compose. Made for
# AI agents: every step checks its preconditions and prints one clear line.
#
# Usage:
#   run-server.sh up [--port N] [--no-build]  build (unless --no-build) and start;
#                                              waits until the server is healthy
#   run-server.sh status                       containers, health, URL, login state
#   run-server.sh logs [N]                     last N lines of server logs (default 100)
#   run-server.sh down                         stop everything; data is kept
#   run-server.sh reset --yes                  stop everything and DELETE all data
#
# Environment:
#   EXPENSE_PORT   host port for the server (default 8080; also --port)
#   EXPENSE_PG_PORT host port for Postgres (default 5432)
#
# Exit status: 0 ok, 1 the stack is not healthy / a step failed, 2 usage or
# a precondition (Docker not running, port in use, missing --yes).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT"
[ -f compose.yaml ] && [ -f Dockerfile ] || { echo "run-server: compose.yaml/Dockerfile not found in $ROOT" >&2; exit 2; }

PORT="${EXPENSE_PORT:-8080}"
die() { echo "run-server: $*" >&2; exit 2; }
url() { echo "http://127.0.0.1:${PORT}"; }

need_docker() {
	command -v docker >/dev/null || die "docker is not installed (install Docker Desktop, or Docker Engine with Compose v2)"
	docker info >/dev/null 2>&1 || die "the Docker daemon is not running; ask the user to start Docker Desktop (macOS: open -a Docker), then retry"
	docker compose version >/dev/null 2>&1 || die "Docker Compose v2 is missing (docker compose version)"
}

# The port the app container currently publishes, or "".
current_port() {
	docker compose port app 8080 2>/dev/null | awk -F: 'NF{print $NF}' || true
}

port_busy() { # port_busy N: something other than our app listens on 127.0.0.1:N
	local p=$1
	[ "$(current_port)" = "$p" ] && return 1
	if command -v lsof >/dev/null; then
		lsof -nP -iTCP:"$p" -sTCP:LISTEN >/dev/null 2>&1
	else
		(exec 3<>"/dev/tcp/127.0.0.1/$p") 2>/dev/null
	fi
}

login_state() {
	local body code
	body=$(curl -s -m 5 "$(url)/api/auth/me" || true)
	case "$body" in
	*setup_required*) echo "no account yet: the first registration creates the owner (web page or POST /api/auth/register)" ;;
	*unauthenticated*) echo "accounts exist: log in on the web page, or use an API token" ;;
	*) echo "unknown (/api/auth/me answered: ${body:0:80})" ;;
	esac
}

cmd_status() {
	need_docker
	docker compose ps --format '{{.Service}}: {{.State}} {{.Health}} {{.Ports}}'
	local p; p=$(current_port)
	if [ -z "$p" ]; then echo "server: not running (run-server.sh up)"; return 1; fi
	PORT=$p
	if curl -sf -m 5 "$(url)/api/healthz" >/dev/null; then
		echo "server: healthy at $(url)  (API docs: $(url)/swagger/)"
		echo "login: $(login_state)"
	else
		echo "server: not answering at $(url)/api/healthz (run-server.sh logs)"; return 1
	fi
}

cmd_up() {
	local build=--build
	while [ $# -gt 0 ]; do
		case "$1" in
		--port) [ $# -ge 2 ] || die "--port needs a number"; PORT=$2; shift 2 ;;
		--no-build) build=; shift ;;
		*) die "unknown option for up: $1" ;;
		esac
	done
	[[ "$PORT" =~ ^[0-9]+$ ]] || die "port must be a number: $PORT"
	need_docker
	if port_busy "$PORT"; then
		die "port $PORT is already in use on 127.0.0.1 (another server?); pick a free one: run-server.sh up --port 18080"
	fi
	echo "run-server: starting (this builds the image the first time; a few minutes)..."
	if ! EXPENSE_PORT=$PORT docker compose up -d $build --wait; then
		echo "run-server: the stack did not become healthy; recent server logs:" >&2
		docker compose logs --tail 30 app >&2 || true
		exit 1
	fi
	cmd_status
}

cmd_logs() {
	need_docker
	local n=${1:-100}
	[[ "$n" =~ ^[0-9]+$ ]] || die "logs takes a line count"
	docker compose logs --no-color --tail "$n" app
}

cmd_down() {
	need_docker
	docker compose down
	echo "run-server: stopped; data is kept in the pgdata volume (run-server.sh up to start again)"
}

cmd_reset() {
	[ "${1:-}" = "--yes" ] || die "reset deletes ALL local data (users, balances, transactions); confirm with the user, then run: run-server.sh reset --yes"
	need_docker
	docker compose down --volumes
	echo "run-server: stopped and deleted all local data"
}

case "${1:-}" in
up) shift; cmd_up "$@" ;;
status) cmd_status ;;
logs) shift; cmd_logs "$@" ;;
down) cmd_down ;;
reset) shift; cmd_reset "$@" ;;
*) sed -n '2,23p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
esac

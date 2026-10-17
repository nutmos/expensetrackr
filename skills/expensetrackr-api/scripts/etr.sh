#!/usr/bin/env bash
# etr.sh: tiny curl wrapper for the expensetrackr JSON API, for scripts and
# AI agents. It keeps the session cookie in a private cookie jar, logs in
# from environment variables (once, and again after a 401), always sends
# Content-Type: application/json with a body, and never sends Origin or
# Sec-Fetch-Site (so the server's CSRF guard lets it through).
#
# Usage:
#   etr.sh login                          log in (normally automatic)
#   etr.sh logout                         end the session, delete the jar
#   etr.sh GET    PATH                    e.g. /api/balances
#   etr.sh POST   PATH JSON|@FILE|-       create
#   etr.sh PUT    PATH JSON|@FILE|- [--if-match VERSION]
#   etr.sh DELETE PATH [--if-match VERSION]
#   etr.sh edit   PATH JQ_FILTER          GET, keep the editable fields, apply
#                                         the jq filter, PUT with If-Match;
#                                         on 409 refetch and re-apply (3 tries)
#
# The response body goes to stdout; "HTTP <status> [ETag <etag>]" goes to
# stderr. Exit status: 0 for 2xx, 1 for any other HTTP status, 2 for usage,
# network or login errors.
#
# Environment:
#   EXPENSETRACKR_URL         base URL (default http://127.0.0.1:8080)
#   EXPENSETRACKR_USERNAME    login username   (needed only to log in)
#   EXPENSETRACKR_PASSWORD    login password   (never printed or put in argv)
#   EXPENSETRACKR_COOKIE_JAR  cookie jar path
#                             (default ${XDG_CACHE_HOME:-~/.cache}/expensetrackr/cookies.txt)
#
# Requires curl; `edit` and `login` also need jq.
set -euo pipefail

BASE="${EXPENSETRACKR_URL:-http://127.0.0.1:8080}"
BASE="${BASE%/}"
JAR="${EXPENSETRACKR_COOKIE_JAR:-${XDG_CACHE_HOME:-$HOME/.cache}/expensetrackr/cookies.txt}"

die() { echo "etr: $*" >&2; exit 2; }

ensure_jar() {
	umask 077
	mkdir -p "$(dirname "$JAR")"
	[ -e "$JAR" ] || : >"$JAR"
	chmod 600 "$JAR"
}

# raw METHOD PATH [BODY_FILE] [IF_MATCH] -> sets STATUS, ETAG; body in $OUT_BODY
raw() {
	local method=$1 path=$2 body=${3:-} ifm=${4:-} hdr
	hdr=$(mktemp)
	local args=(-sS -o "$OUT_BODY" -D "$hdr" -w '%{http_code}' -X "$method"
		-b "$JAR" -c "$JAR" -H 'Accept: application/json')
	if [ -n "$body" ]; then
		args+=(-H 'Content-Type: application/json' --data-binary "@$body")
	fi
	if [ -n "$ifm" ]; then
		ifm=${ifm//\"/}
		args+=(-H "If-Match: \"$ifm\"")
	fi
	STATUS=$(curl "${args[@]}" "$BASE$path") || { rm -f "$hdr"; die "request to $BASE$path failed (is the server running?)"; }
	ETAG=$(tr -d '\r' <"$hdr" | awk 'tolower($1)=="etag:" {print $2}' | tail -n1)
	rm -f "$hdr"
}

login() {
	[ -n "${EXPENSETRACKR_USERNAME:-}" ] || die "set EXPENSETRACKR_USERNAME (and EXPENSETRACKR_PASSWORD) to log in"
	[ -n "${EXPENSETRACKR_PASSWORD:-}" ] || die "set EXPENSETRACKR_PASSWORD to log in"
	command -v jq >/dev/null || die "jq is required"
	local creds
	creds=$(mktemp)
	jq -n '{username: env.EXPENSETRACKR_USERNAME, password: env.EXPENSETRACKR_PASSWORD}' >"$creds"
	raw POST /api/auth/login "$creds"
	rm -f "$creds"
	if [ "$STATUS" != 200 ]; then
		echo "HTTP $STATUS" >&2
		cat "$OUT_BODY" >&2; echo >&2
		die "login failed"
	fi
}

# call METHOD PATH [BODY_FILE] [IF_MATCH]: one request, re-logging in once on 401.
call() {
	raw "$@"
	if [ "$STATUS" = 401 ] && [ "$2" != /api/auth/login ] && [ -n "${EXPENSETRACKR_USERNAME:-}" ]; then
		local saved; saved=$(mktemp); cp "$OUT_BODY" "$saved"
		login
		cp "$saved" "$OUT_BODY"; rm -f "$saved"
		raw "$@"
	fi
}

report() {
	if [ -n "$ETAG" ]; then echo "HTTP $STATUS ETag $ETAG" >&2; else echo "HTTP $STATUS" >&2; fi
	cat "$OUT_BODY"
	[ -s "$OUT_BODY" ] && echo
	case $STATUS in 2??) return 0 ;; *) return 1 ;; esac
}

# body_file ARG -> path of a file holding the JSON body
body_file() {
	local f
	f=$(mktemp)
	case $1 in
		-) cat >"$f" ;;
		@*) cat "${1#@}" >"$f" ;;
		*) printf '%s' "$1" >"$f" ;;
	esac
	echo "$f"
}

# Editable fields per resource: a GET response sent back as-is is a 400
# (unknown fields such as amount_minor), so `edit` keeps only these.
project() {
	case $1 in
		/api/transactions/*)
			echo '{amount, currency, type, balance_uid, to_balance_uid, category_uid, spent_at, note}
			      + (if .rate_source == "manual" then {to_amount} else {} end)
			      | with_entries(select(.value != null))' ;;
		/api/balances/*)
			echo '{name, type, currency, description, balance, debt, limit} | with_entries(select(.value != null))' ;;
		/api/categories/*)
			echo '{name, type, description}' ;;
		*) die "edit supports /api/transactions/UID, /api/balances/UID and /api/categories/UID" ;;
	esac
}

edit() {
	local path=$1 filter=$2 proj attempt next
	command -v jq >/dev/null || die "jq is required"
	proj=$(project "$path")
	for attempt in 1 2 3; do
		call GET "$path"
		[ "$STATUS" = 200 ] || { report; return 1; }
		next=$(mktemp)
		jq "$proj | $filter" "$OUT_BODY" >"$next" || { rm -f "$next"; die "jq filter failed"; }
		call PUT "$path" "$next" "$ETAG"
		rm -f "$next"
		if [ "$STATUS" = 409 ] && jq -e '.code == "version_conflict"' "$OUT_BODY" >/dev/null 2>&1; then
			echo "etr: version conflict (attempt $attempt), refetching" >&2
			continue
		fi
		report
		return
	done
	report
}

OUT_BODY=$(mktemp)
trap 'rm -f "$OUT_BODY"' EXIT

[ $# -ge 1 ] || { sed -n '2,30p' "$0" >&2; exit 2; }
ensure_jar
cmd=$1; shift
case $cmd in
	login)
		login
		echo "HTTP $STATUS" >&2
		jq '{username, display_name}' "$OUT_BODY" ;;
	logout)
		raw POST /api/auth/logout
		rm -f "$JAR"
		report ;;
	edit)
		[ $# -eq 2 ] || die "usage: etr.sh edit PATH JQ_FILTER"
		edit "$1" "$2" ;;
	GET|get|DELETE|delete|POST|post|PUT|put)
		method=$(echo "$cmd" | tr '[:lower:]' '[:upper:]')
		[ $# -ge 1 ] || die "usage: etr.sh $method PATH ..."
		path=$1; shift
		case $path in /*) ;; *) die "PATH must start with / (e.g. /api/balances)" ;; esac
		body="" ifm=""
		while [ $# -gt 0 ]; do
			case $1 in
				--if-match) [ $# -ge 2 ] || die "--if-match needs a version"; ifm=$2; shift 2 ;;
				*) [ -z "$body" ] || die "only one body argument"; body=$(body_file "$1"); shift ;;
			esac
		done
		case $method in
			POST|PUT) [ -n "$body" ] || die "$method needs a JSON body (string, @file or -)" ;;
			GET|DELETE) [ -z "$body" ] || die "$method takes no body" ;;
		esac
		call "$method" "$path" "$body" "$ifm"
		[ -z "$body" ] || rm -f "$body"
		report ;;
	-h|--help|help) sed -n '2,30p' "$0" ;;
	*) die "unknown command $cmd (try --help)" ;;
esac

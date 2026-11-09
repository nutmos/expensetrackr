#!/usr/bin/env bash
# log-tx.sh: create one expensetrackr transaction with a Bearer API token.
#
# Usage:
#   log-tx.sh expense  SOURCE_BALANCE AMOUNT CURRENCY [options]
#   log-tx.sh income   SOURCE_BALANCE AMOUNT CURRENCY [options]
#   log-tx.sh transfer SOURCE_BALANCE DEST_BALANCE AMOUNT CURRENCY [options]
#   log-tx.sh asset_purchase SOURCE_BALANCE DEST_BALANCE AMOUNT CURRENCY [options]
#   log-tx.sh return_on_investment ACCOUNT_OR_CARD ASSET AMOUNT CURRENCY [options]
#     (dividend/interest received into ACCOUNT_OR_CARD, linked to ASSET,
#      an other_asset whose value is not changed)
#
# Options:
#   --category NAME       category name, required except for a transfer
#                         (expense category for expense and asset_purchase;
#                         income category for income and return_on_investment)
#   --at RFC3339          spent_at with offset (required)
#   --note TEXT           optional note
#   --to-amount AMOUNT    received amount (cross-currency transfer only; optional)
#   --to-currency CODE    must match destination balance currency if set
#
# Environment:
#   EXPENSETRACKR_TOKEN   write-scoped personal API token (ext_…) — required
#   EXPENSETRACKR_URL     base URL (default http://127.0.0.1:8080)
#
# The token is never placed in argv: curl gets it via -H @tempfile.
# Requires curl and jq. Exit 0 on 201; 1 on API/HTTP errors; 2 on usage.
set -euo pipefail

BASE="${EXPENSETRACKR_URL:-http://127.0.0.1:8080}"
BASE="${BASE%/}"

die() { echo "log-tx: $*" >&2; exit 2; }
fail() { echo "log-tx: $*" >&2; exit 1; }

usage() {
	cat >&2 <<'EOF'
Usage:
  log-tx.sh expense|income SOURCE AMOUNT CURRENCY --category NAME --at RFC3339 [--note TEXT]
  log-tx.sh transfer SOURCE DEST AMOUNT CURRENCY --at RFC3339 [--to-amount A] [--to-currency C] [--note TEXT]
  log-tx.sh asset_purchase SOURCE ASSET AMOUNT CURRENCY --category NAME --at RFC3339 [--note TEXT]
  log-tx.sh return_on_investment ACCOUNT_OR_CARD ASSET AMOUNT CURRENCY --category NAME --at RFC3339 [--note TEXT]
EOF
	exit 2
}

[ -n "${EXPENSETRACKR_TOKEN:-}" ] || die "set EXPENSETRACKR_TOKEN (write-scoped ext_… token)"
command -v jq >/dev/null || die "jq is required"
command -v curl >/dev/null || die "curl is required"

TYPE="${1:-}"
shift || true
case "$TYPE" in
expense | income | transfer | asset_purchase | return_on_investment) ;;
"" | -h | --help) usage ;;
*) die "unknown type '$TYPE' (expense|income|transfer|asset_purchase|return_on_investment)" ;;
esac

SOURCE=""
DEST=""
AMOUNT=""
CURRENCY=""
case "$TYPE" in
expense | income)
	[ $# -ge 3 ] || usage
	SOURCE=$1 AMOUNT=$2 CURRENCY=$3
	shift 3
	;;
transfer | asset_purchase | return_on_investment)
	[ $# -ge 4 ] || usage
	SOURCE=$1 DEST=$2 AMOUNT=$3 CURRENCY=$4
	shift 4
	;;
esac

CATEGORY=""
SPENT_AT=""
NOTE=""
TO_AMOUNT=""
TO_CURRENCY=""
while [ $# -gt 0 ]; do
	case "$1" in
	--category)
		[ $# -ge 2 ] || die "--category needs a value"
		CATEGORY=$2
		shift 2
		;;
	--at)
		[ $# -ge 2 ] || die "--at needs a value"
		SPENT_AT=$2
		shift 2
		;;
	--note)
		[ $# -ge 2 ] || die "--note needs a value"
		NOTE=$2
		shift 2
		;;
	--to-amount)
		[ $# -ge 2 ] || die "--to-amount needs a value"
		TO_AMOUNT=$2
		shift 2
		;;
	--to-currency)
		[ $# -ge 2 ] || die "--to-currency needs a value"
		TO_CURRENCY=$2
		shift 2
		;;
	*) die "unknown option: $1" ;;
	esac
done

[ -n "$SPENT_AT" ] || die "--at RFC3339-with-offset is required (e.g. 2026-10-06T12:30:00+08:00)"
case "$TYPE" in
transfer)
	[ -z "$CATEGORY" ] || die "category is not allowed on a transfer"
	;;
*)
	[ -n "$CATEGORY" ] || die "--category NAME is required for $TYPE (ask the user which category)"
	;;
esac
if [ "$TYPE" != transfer ]; then
	[ -z "$TO_AMOUNT$TO_CURRENCY" ] || die "--to-amount/--to-currency are only allowed on a transfer"
fi

HDR=$(mktemp)
OUT=$(mktemp)
TXJSON=$(mktemp)
BALANCES=$(mktemp)
CATEGORIES=$(mktemp)
cleanup() { rm -f "$HDR" "$OUT" "$TXJSON" "$BALANCES" "$CATEGORIES"; }
trap cleanup EXIT
chmod 600 "$HDR"
printf 'Authorization: Bearer %s\n' "$EXPENSETRACKR_TOKEN" >"$HDR"

api() {
	# api METHOD PATH [BODY] -> sets STATUS; body in $OUT
	local method=$1 path=$2 body=${3:-}
	local args=(-sS -o "$OUT" -w '%{http_code}' -X "$method"
		-H @"$HDR" -H 'Accept: application/json')
	if [ -n "$body" ]; then
		args+=(-H 'Content-Type: application/json' --data-binary "$body")
	fi
	STATUS=$(curl "${args[@]}" "$BASE$path") || fail "request to $BASE$path failed (is the server running?)"
}

api GET /api/balances
[ "$STATUS" = 200 ] || {
	echo "HTTP $STATUS" >&2
	cat "$OUT" >&2
	fail "GET /api/balances failed"
}
cp "$OUT" "$BALANCES"

api GET /api/categories
[ "$STATUS" = 200 ] || {
	echo "HTTP $STATUS" >&2
	cat "$OUT" >&2
	fail "GET /api/categories failed"
}
cp "$OUT" "$CATEGORIES"

resolve_balance() {
	local name=$1 field=${2:-uid}
	local matches
	matches=$(jq -r --arg n "$name" '
		[.balances[] | select((.name | ascii_downcase) == ($n | ascii_downcase))]
		| if length == 0 then "NONE"
		  elif length > 1 then "AMBIGUOUS"
		  else .[0].'"$field"' end' "$BALANCES")
	case "$matches" in
	NONE) fail "no balance named '$name'" ;;
	AMBIGUOUS) fail "ambiguous balance name '$name'; ask the user which account" ;;
	esac
	printf '%s\n' "$matches"
}

resolve_category() {
	local name=$1 want=$2
	local matches
	matches=$(jq -r --arg n "$name" --arg t "$want" '
		[.categories[]
		 | select((.name | ascii_downcase) == ($n | ascii_downcase) and .type == $t)]
		| if length == 0 then "NONE"
		  elif length > 1 then "AMBIGUOUS"
		  else .[0].uid end' "$CATEGORIES")
	case "$matches" in
	NONE) fail "no $want category named '$name'" ;;
	AMBIGUOUS) fail "ambiguous category name '$name'" ;;
	esac
	printf '%s\n' "$matches"
}

SRC_UID=$(resolve_balance "$SOURCE")
SRC_CUR=$(resolve_balance "$SOURCE" currency)
# Currency on the wire must match the source balance.
CUR_UP=$(printf '%s' "$CURRENCY" | tr '[:lower:]' '[:upper:]')
[ "$CUR_UP" = "$SRC_CUR" ] || fail "currency $CUR_UP does not match balance '$SOURCE' ($SRC_CUR)"

DEST_UID=""
if [ -n "$DEST" ]; then
	DEST_UID=$(resolve_balance "$DEST")
fi

CAT_UID=""
if [ -n "$CATEGORY" ]; then
	want=expense
	# asset_purchase uses an expense category; return_on_investment an income one
	if [ "$TYPE" = income ] || [ "$TYPE" = return_on_investment ]; then
		want=income
	fi
	CAT_UID=$(resolve_category "$CATEGORY" "$want")
fi

BODY=$(jq -n \
	--arg type "$TYPE" \
	--arg amount "$AMOUNT" \
	--arg currency "$CUR_UP" \
	--arg balance_uid "$SRC_UID" \
	--arg to_balance_uid "$DEST_UID" \
	--arg category_uid "$CAT_UID" \
	--arg spent_at "$SPENT_AT" \
	--arg note "$NOTE" \
	--arg to_amount "$TO_AMOUNT" \
	--arg to_currency "$TO_CURRENCY" '
	{type: $type, amount: $amount, currency: $currency, balance_uid: $balance_uid, spent_at: $spent_at}
	| if $to_balance_uid != "" then . + {to_balance_uid: $to_balance_uid} else . end
	| if $category_uid != "" then . + {category_uid: $category_uid} else . end
	| if $note != "" then . + {note: $note} else . end
	| if $to_amount != "" then . + {to_amount: $to_amount} else . end
	| if $to_currency != "" then . + {to_currency: $to_currency} else . end
')

api POST /api/transactions "$BODY"
if [ "$STATUS" != 201 ]; then
	echo "HTTP $STATUS" >&2
	cat "$OUT" >&2
	echo >&2
	fail "POST /api/transactions failed"
fi

cp "$OUT" "$TXJSON"
# Refresh balances for the confirmation line (do not clobber the TX response).
api GET /api/balances
if [ "$STATUS" = 200 ]; then cp "$OUT" "$BALANCES"; fi

jq -r --argjson bal "$(cat "$BALANCES")" '
	def bal_line($uid):
		($bal.balances[] | select(.uid == $uid)
		 | "\(.name) now \(.balance // .debt) \(.currency)");
	"logged \(.type) \(.amount) \(.currency)"
	+ (if .note != "" then " (\(.note))" else "" end)
	+ (if .type == "return_on_investment"
	   then " into \(.account) (from investment \(.to_account))"
	   else " from \(.account)"
	     + (if .to_account != null then " → \(.to_account)" else "" end)
	   end)
	+ " at \(.spent_at)"
	+ " [uid \(.uid)]",
	(bal_line(.balance_uid)),
	(if .to_balance_uid != null then bal_line(.to_balance_uid) else empty end),
	(if .conversion_rates != null then
		(.conversion_rates[] | "rate: 1 \(.from) = \(.rate) \(.to)")
	 else empty end)
' "$TXJSON"

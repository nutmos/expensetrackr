# MCP server

[← Back to the README](../README.md) · [All docs](README.md)

`expense-mcp` is a [Model Context Protocol](https://modelcontextprotocol.io)
server that lets an LLM client (Claude Desktop, Claude Code, Cursor, ...)
**read and update transactions and balances**. It runs over **stdio**: the client starts
it as a subprocess and talks JSON-RPC on its stdin/stdout.

It is a thin client of the HTTP API: each tool call becomes one or two
requests to `/api/...` with a personal **API token**, so authentication,
per-user ownership, validation, balance updates and optimistic locking are
exactly the API's. It needs no database access and can run on another
machine than the server.

## Setup

1. Start the server (`make up`, or `make db-up` + `make run`).
2. In the web page, **Settings → API tokens**, create a token (e.g. "Claude",
   scope `read` + `write`; `read` alone for a read-only assistant) and copy it.
3. Build the binary: `make build-mcp` (writes `bin/expense-mcp`), or
   `go install github.com/nutmos/expensetrackr/cmd/mcp@latest` (installs it as
   `mcp`).
4. Register it with your client.

Claude Code:

```sh
claude mcp add expensetrackr \
  --env EXPENSETRACKR_URL=http://127.0.0.1:8080 \
  --env EXPENSETRACKR_TOKEN=ext_... \
  -- /path/to/expensetrackr/bin/expense-mcp
```

Claude Desktop (`claude_desktop_config.json`), Cursor (`.cursor/mcp.json`)
and most other clients use the same shape:

```json
{
  "mcpServers": {
    "expensetrackr": {
      "command": "/path/to/expensetrackr/bin/expense-mcp",
      "env": {
        "EXPENSETRACKR_URL": "http://127.0.0.1:8080",
        "EXPENSETRACKR_TOKEN": "ext_..."
      }
    }
  }
}
```

From the container image instead of a local build:

```json
"command": "docker",
"args": ["run", "-i", "--rm", "--network", "host", "-e", "EXPENSETRACKR_URL", "-e", "EXPENSETRACKR_TOKEN",
         "--entrypoint", "/usr/local/bin/expense-mcp", "ghcr.io/nutmos/expensetrackr"]
```

| Setting | Default | |
|---|---|---|
| `EXPENSETRACKR_TOKEN` (env only) | — | **Required.** Personal API token (`ext_...`). Never a flag, so it stays out of process lists. |
| `-url` / `EXPENSETRACKR_URL` | `http://127.0.0.1:8080` | Server root (no `/api`). Use `https://` for anything but localhost: the token is sent with every request. |
| `-read-only` / `EXPENSETRACKR_MCP_READ_ONLY=1` | off | Only offer the five read tools. |
| `-version` | | Print the version and exit. |

Logs and startup errors go to stderr; stdout carries only the protocol.

## Tools

| Tool | Reads / writes | Does |
|---|---|---|
| `list_balances` | read | Balances with uid, name, type, currency, amount and per-currency totals (`type`, `payable` filters; hidden balances only with `include_hidden`). Used to turn an account name into a `balance_uid`. |
| `get_balance` | read | One balance by `uid` (hidden ones too), with its `version`. |
| `create_balance` | write | `POST /api/balances`: `name`, `type`, `currency`, `description`; `balance` for `payment_account` / `other_asset`, `debt` and `limit` for `credit_card` / `other_liability`. |
| `update_balance` | write | **Partial** update: `uid`, optional `version`, and only the fields to change (`name`, `currency`, `description`, `balance`, `debt`, `limit`, `hidden`). The type cannot change. Setting `balance` / `debt` overrides the amount and the server records a `balance_adjustment` transaction ([balances.md §2](balances.md#2-manual-adjustment-put-on-a-balance)); `hidden` calls `/hide` or `/unhide`. |
| `list_categories` | read | Categories with uid, name and type (`type` filter). Used to turn a category name into a `category_uid`. |
| `list_transactions` | read | `GET /api/transactions`, newest first: `from`, `to`, `type`, `category_uid`, `balance_uid`, `limit` (default 50, max 500), `cursor` (the previous page's `next_cursor`). |
| `get_transaction` | read | One transaction by `uid`, with its `version`. |
| `create_transaction` | write | `POST /api/transactions` with the same fields (`type`, `amount`, `currency`, `balance_uid`, `to_balance_uid`, `category_uid`, `spent_at`, `note`, `to_amount`, `to_currency`, `import_ref`, `foreign_amount`, `foreign_currency`, `interest_amount`, `fee_amount`). |
| `update_transaction` | write | **Partial** update: `uid`, optional `version`, and only the fields to change. |
| `delete_transaction` | write (destructive) | `DELETE /api/transactions/:uid`, optional `version`. |

Amounts may be sent as strings (`"120.50"`, preferred) or JSON numbers; they
are passed to the API as text, never through a float. Unknown arguments are
rejected so a misspelt field (`category` for `category_uid`) is reported
instead of ignored.

### How `update_transaction` works

The API's `PUT` is a full replace, which is easy for a model to get wrong
(an omitted `note` clears it). The tool does the read-modify-write itself:

1. `GET /api/transactions/:uid` (the current record and its `ETag`).
2. If `version` was given and differs from the current one: stop with a
   `version_conflict` error carrying `current`, writing nothing. Pass the
   version you showed the user so a change made meanwhile (web page, another
   agent) is not overwritten unnoticed.
3. Copy the editable fields (`type`, `amount`, `currency`, `balance_uid`,
   `to_balance_uid`, `category_uid`, `spent_at`, `note`, `foreign_amount`,
   `foreign_currency`, `interest_amount`, `fee_amount`, `gain_loss`,
   `entire_asset_sale`) and apply the given changes. `""` clears `note` /
   `import_ref` / `interest_amount` / `fee_amount` / `gain_loss`, and `""` for either foreign field clears both; an omitted
   `import_ref` is kept.
4. Clean up for the resulting type: `to_balance_uid` is dropped for types
   without a destination, `category_uid` for a transfer (and for a repay_debt
   left without interest or fees, or an asset_sale left without `gain_loss`),
   and `foreign_amount` / `foreign_currency`, `interest_amount` / `fee_amount`
   and `gain_loss` / `entire_asset_sale` for types that don't allow them, unless sent
   explicitly. A type change still needs the new type's own fields
   (e.g. `category_uid` when a transfer becomes an expense: 422 otherwise).
5. **Manual-rate transfers:** a cross-currency transfer whose rate came from
   the user (`rate_source: "manual"`) keeps its `to_amount` while nothing the
   conversion depends on changes. If `amount`, `currency`, either balance or
   the date changes, the tool asks for `to_amount` again (or `""` to switch to
   the ECB rate) rather than letting the server silently refetch a rate.
6. `PUT` with `If-Match` from step 1. A concurrent change between 1 and 6 is a
   409 from the API, returned as is.

Balance adjustments can be edited too, as in the API: they keep their type,
the tool carries over their `adjustment_direction`, and `adjustment_direction`,
`amount`, `balance_uid` (any balance), `spent_at` and `note` may be changed.
`delete_transaction` on one reverts the balance change it recorded.

### How `update_balance` works

The same read-modify-write as `update_transaction`, against
`/api/balances/:uid`:

1. `GET /api/balances/:uid` (the current record and its `ETag`); a given
   `version` that differs stops with `version_conflict`, writing nothing.
   Balances move with every transaction, so pass the version you showed the
   user before overriding an amount.
2. Copy `name`, `type`, `currency`, `description` and the amounts that apply
   to the type (`balance`, or `debt` and `limit`), apply the changes (`""`
   clears `description`) and `PUT` with `If-Match`. `type` is not accepted:
   it is fixed at creation. Changing `balance` / `debt` records a
   `balance_adjustment` transaction for the difference; a limit, name or
   description change records nothing.
3. If `hidden` was given and differs, `POST /api/balances/:uid/hide` (or
   `/unhide`) with the new version. `hidden` alone skips the `PUT`.

There is no tool to delete a balance: deleting one also deletes every
transaction that uses it. Hide it instead, or delete it in the web page.

### Errors

Tool errors come back as results with `isError: true` (so the model sees them
and can correct itself), with the API's status and JSON body plus a short
hint, e.g.

```
expensetrackr API returned HTTP 422 Unprocessable Entity: {"error":"validation failed","fields":{"category_uid":"is required for a expense"}}
Hint: "fields" names each invalid field; fix those and try again ...
```

401 means a bad or revoked token; 403 `insufficient_scope` a read token on a
write tool. The token never appears in results or logs.

## Instructions sent to the model

The `initialize` result carries short rules that clients add to the model's
context: never invent amounts, accounts, categories or dates; resolve names
with the list tools and talk in names; RFC 3339 with offset; which category
type each transaction type needs; balance adjustments are created by the server only (but may be edited or deleted);
check for an existing balance before creating one, and prefer recording the
missing transaction over overriding a balance's amount (confirm an override);
confirm before deleting. They mirror the safety rules of the
[log-transaction skill](../skills/expensetrackr-log-transaction/SKILL.md).

## Protocol

Hand-written (no SDK dependency) in [`pkg/mcp`](../pkg/mcp): newline-delimited
JSON-RPC 2.0 on stdio; `initialize` (protocol revisions 2025-11-25,
2025-06-18, 2025-03-26 and 2024-11-05), `ping`, `tools/list` (with
`readOnlyHint` / `destructiveHint` annotations) and `tools/call`. Requests
are handled one at a time.

Tests: `pkg/mcp/server_test.go` covers the protocol, argument handling and
the update merge without a database; `pkg/mcp/integration_test.go` drives
every tool against the real API on a throwaway Postgres schema (skipped
without `EXPENSE_TEST_DATABASE_URL`, like the other database tests).

Try it by hand:

```sh
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"sh","version":"0"}}}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_transactions","arguments":{"limit":5}}}' \
  | EXPENSETRACKR_TOKEN=ext_... bin/expense-mcp
```

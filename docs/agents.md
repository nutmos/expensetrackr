# AI agents

[← Back to the README](../README.md) · [All docs](README.md)

Agent skills shipped in [`skills/`](../skills) and how agents authenticate.

[`skills/expensetrackr-api/`](../skills/expensetrackr-api/SKILL.md) is an agent
skill (the `SKILL.md` convention used by Claude Agent Skills, Cursor and
similar tools) that teaches an LLM agent to use this API: login and CSRF
rules, looking up balance/category uids, logging expenses, income, transfers
(incl. cross-currency), asset purchases and returns on investment, bulk statement imports, editing with `If-Match`, error
handling and safety rules. Details are in its
[`reference.md`](../skills/expensetrackr-api/reference.md); the helper
[`scripts/etr.sh`](../skills/expensetrackr-api/scripts/etr.sh) wraps curl with a
cookie jar, auto-login from `EXPENSETRACKR_USERNAME` / `EXPENSETRACKR_PASSWORD`
and GET → PUT edits with conflict retry. For recording a single expense,
income, transfer or asset purchase with a Bearer API token, see the focused
[`skills/expensetrackr-log-transaction/`](../skills/expensetrackr-log-transaction/SKILL.md)
skill. To start a local server first,
[`skills/expensetrackr-run-server/`](../skills/expensetrackr-run-server/SKILL.md)
runs it from the `Dockerfile` with Docker Compose; its
[`scripts/run-server.sh`](../skills/expensetrackr-run-server/scripts/run-server.sh)
checks Docker and the port, waits until the server is healthy and reports
whether an account exists (`up`, `status`, `logs`, `down`, `reset --yes`).
Copy or symlink the folders into your agent's skills directory (e.g.
`~/.claude/skills/` or `.cursor/skills/`).

**API tokens** are the simpler option for agents: create one in
**Settings → API tokens** (e.g. named "Expense agent", scope `read` or
`write`), copy it once, and send it as a Bearer header — no login, cookie jar
or `Origin` header needed:

```sh
curl -H "Authorization: Bearer $EXPENSE_TOKEN" http://127.0.0.1:8080/api/transactions
```

Tokens can't manage tokens, change the password or log out (403
`session_required`); a `read` token gets 403 `insufficient_scope` on writes.
See [docs/auth.md](auth.md#api-tokens).

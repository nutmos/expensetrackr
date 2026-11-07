# Authentication: username + password (issue #30)

Basic login with a **username and password only**. Email is not used anywhere
in the flow (the `users.email` column stays, optional). SSO (Apple / Google via
`user_identities`) is a later follow-up.

## Summary

| Topic | Decision |
|-------|----------|
| Password hashing | bcrypt (`golang.org/x/crypto/bcrypt` v0.43.0, cost 12). Stored in `users.password_hash`; never returned (responses show `has_password`). |
| Password rules | 8–72 **bytes** (72 is bcrypt's input limit; longer passwords are rejected, never silently truncated). |
| Username rules | 3–32 chars, lowercase letters, digits, `.`, `_`, `-`, starting with a letter or digit. Input is lowercased; unique case-insensitively. |
| Sessions | Server-side, table `sessions`. A random 32-byte token (base64url) lives only in the cookie; the DB stores its SHA-256. |
| Cookie | `session`, `HttpOnly`, `SameSite=Lax`, `Path=/`, `Secure` only when the request came over TLS. |
| Expiry | **30 days, sliding**: every authenticated request (at most once a minute) moves `expires_at` to now + 30 days and re-sends the cookie. An idle session dies after 30 days. Expired rows are deleted when seen and on every new login. |
| Registration | Open only on **first run** (no user has a password yet). Afterwards only a logged-in user can register more accounts (403 `registration_closed` otherwise). |
| Brute force | In-memory throttle per *username + client IP*: 5 failures within 15 minutes → 429 with `Retry-After` for 5 minutes. Reset on success and on restart. |
| CSRF | Origin / `Sec-Fetch-Site` check + JSON-only request bodies (see below). |
| API tokens | `Authorization: Bearer ext_...` for scripts and agents; hash-only storage, `read`/`write` scopes, session-only token management (see "API tokens"). |

## Endpoints (`/api/auth`)

| Method | Path | Auth | Result |
|--------|------|------|--------|
| POST | `/api/auth/register` | first run: none; afterwards: session | `{"username","password","display_name"?}` → 201 user. First run also logs the new user in. 422 rule violations, 409 username taken, 403 `registration_closed`, 400 unknown fields (e.g. `email`). |
| POST | `/api/auth/login` | none | `{"username","password"}` → 200 user + `Set-Cookie`. Updates `last_login_at`. Unknown user, wrong password and `status != active` all give the same **401 `invalid username or password`**; an unknown user still costs one bcrypt comparison (dummy hash) so timing does not reveal which usernames exist. 429 when throttled. |
| POST | `/api/auth/logout` | none | Deletes the session (if any), clears the cookie. Always 204. |
| GET | `/api/auth/me` | session | 200 current user; 401 otherwise, with `code: "setup_required"` while no account exists (the web page then offers the Create account button on its Log in page). |
| PUT | `/api/auth/password` | session | `{"current_password","new_password"}` → 204. 403 wrong current password, 422 rules. **Every other session of the user is deleted**; the current one stays. |

## What is public

Everything under `/api` needs a valid session or API token (401 JSON
`{"error":"authentication required","code":"unauthenticated"}`) **except**:

- `POST /api/auth/login`, `POST /api/auth/register` (handler enforces first-run), `POST /api/auth/logout`
- `GET /api/healthz`
- `GET /api/openapi.yaml`, `GET /api/openapi.json` and the Swagger UI at `/swagger/` — the API
  description contains no data, so it stays public (and "Try it out" works once logged in, since the
  browser sends the cookie).

The HTML shell (`/`, `/transactions`, …) and `/static/*` are public too; they
contain no data, and the page asks `/api/auth/me` before showing anything.

The users API (`/api/users…`) requires a login, and each user reaches only
their **own** account: any other uid is 404, `GET /api/users` lists just the
caller, and `POST /api/users` is disabled (403 `use_register`; accounts are
created with `POST /api/auth/register`). There are no roles yet.

## CSRF

The session is a cookie, so a malicious site could try to make the browser
send requests. For every non-GET/HEAD/OPTIONS request to `/api/*`:

1. If `Sec-Fetch-Site` is present it must be `same-origin` or `none` → else 403 `code: csrf`.
2. If `Origin` is present its host must equal the request's `Host` (`null` is rejected) → else 403.
3. A request with a body must be `Content-Type: application/json` → else 415. Plain HTML forms
   cannot send that cross-site without a CORS preflight, which this server never approves.

Non-browser clients (curl, scripts) send neither header and are unaffected.
Requests with an `Authorization: Bearer` token skip steps 1–2 (see "CSRF and
Bearer requests").
`SameSite=Lax` on the cookie is a further layer. The login endpoint is guarded
too (prevents login CSRF).

## Table `sessions`

```sql
CREATE TABLE sessions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    token_hash   TEXT NOT NULL UNIQUE CHECK (length(token_hash) = 64), -- sha256 hex
    user_uid     TEXT NOT NULL REFERENCES users (uid) ON DELETE CASCADE,
    created_at   TEXT NOT NULL,   -- RFC 3339 UTC
    expires_at   TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    user_agent   TEXT              -- optional, informational
);
-- + indexes on user_uid and expires_at
```

Part of the baseline schema (`pkg/store/schema.go`). Deleting a user deletes
their sessions.

## API tokens

Personal API tokens let scripts and AI agents call the API without a browser
login. Manage them in **Settings → API tokens** (or the endpoints below, with
a session).

| Topic | Decision |
|-------|----------|
| Format | `ext_` + 32 random bytes from `crypto/rand`, base64url (47 characters). The `ext_` prefix makes leaked tokens easy to recognize and scan for. |
| Storage | Table `api_tokens` (schema v2). Only the SHA-256 (hex) of the token is stored, plus a non-secret display prefix (the first 12 characters, e.g. `ext_Ab12Cd34`). The plaintext is returned **once**, by `POST /api/tokens`. |
| Lookup | By hash (unique index), then the stored hash is compared again with `subtle.ConstantTimeCompare`. |
| Sending | `Authorization: Bearer ext_...`. When this header is present the session cookie is **ignored**, so a token request never gains session-only rights and a bad token is 401 even with a valid cookie. |
| Rejected (401 `invalid_token`) | Unknown, malformed (no `ext_` prefix, longer than 128 characters), revoked or expired tokens, and tokens of a disabled or password-less user. |
| Scopes | `read`: GET/HEAD only. `write`: every method. A `read` token on another method gets 403 `insufficient_scope`. |
| Session only (403 `session_required`) | `GET/POST /api/tokens`, `DELETE /api/tokens/:uid`, `PUT /api/auth/password`, `POST /api/auth/logout`, `POST /api/auth/register`, `PUT`/`PATCH`/`DELETE /api/users/:uid`. A token can't mint, list or revoke tokens, change credentials or the account (the username is the login name), delete it, create password accounts or end sessions. It can read the account and change its preferences. |
| Expiry | Optional `expires_in_days` (1–3650). Without it the token lives until revoked. |
| Last used | `last_used_at` is written at most once a minute (same throttle as the session touch). |
| Revocation | `DELETE /api/tokens/:uid` sets `revoked_at`; the row is kept for audit and no longer listed. Another user's token is 404. Deleting a user deletes their tokens (`ON DELETE CASCADE`). |

### CSRF and Bearer requests

The Origin / `Sec-Fetch-Site` checks exist because browsers attach the
cookie automatically. They are **skipped** for requests with an
`Authorization: Bearer` header: a browser never adds that header by itself,
a cross-site page cannot set it without a CORS preflight (which this server
never approves), and such a request is authenticated only by the token,
never by the cookie. The `Content-Type: application/json` rule for bodies
still applies to every request.

### Endpoints (`/api/tokens`, session only)

| Method | Path | Result |
|--------|------|--------|
| GET | `/api/tokens` | 200 `{"tokens":[...],"count":n}`: your active tokens (`uid`, `name`, `prefix`, `scopes`, `created_at`, `last_used_at`, `expires_at`); never the secret or hash. |
| POST | `/api/tokens` | `{"name","scopes",["expires_in_days"]}` → 201 the token plus `token` (the plaintext, only here). 422 for a missing or too long name (max 64 characters), missing or unknown scopes, or out-of-range days. |
| DELETE | `/api/tokens/:uid` | 204 revoked; 404 not yours / unknown / already revoked; 400 not a UUID. |

### Using a token from an agent

```sh
export EXPENSE_TOKEN=ext_...   # from Settings → API tokens
curl -H "Authorization: Bearer $EXPENSE_TOKEN" http://127.0.0.1:8080/api/transactions
curl -X POST -H "Authorization: Bearer $EXPENSE_TOKEN" -H "Content-Type: application/json" \
  -d '{"amount":"12.50","currency":"SGD","balance_uid":"<balance uid>","spent_at":"2026-11-05T12:30:00+08:00","note":"Lunch"}' \
  http://127.0.0.1:8080/api/transactions
```

### Table `api_tokens` (schema v2)

```sql
CREATE TABLE api_tokens (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    uid          TEXT NOT NULL UNIQUE,
    user_uid     TEXT NOT NULL REFERENCES users (uid) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    prefix       TEXT NOT NULL,          -- non-secret display prefix
    token_hash   TEXT NOT NULL UNIQUE CHECK (length(token_hash) = 64), -- sha256 hex
    scopes       TEXT NOT NULL,          -- space-separated: "read write"
    created_at   TEXT NOT NULL,          -- RFC 3339 UTC
    last_used_at TEXT,
    expires_at   TEXT,                   -- NULL: never expires
    revoked_at   TEXT                    -- NULL: active
);
CREATE INDEX idx_api_tokens_user_uid ON api_tokens (user_uid);
```

## First run

1. Start the server with an empty (or existing, password-less) database.
2. Open the page: signed-out visitors always land on **Log in** (`/login`;
   other URLs redirect to `/login?next=<url>` and return there after login).
   While no account exists it also shows "No account yet — create one to get
   started." and a **Create account** button leading to `/register`
   (username, password, optional display name, and a "Back to log in" button).
   This calls `POST /api/auth/register` and logs you in.
3. From then on the Log in page has no Create account button (visiting
   `/register` falls back to Log in with a "registration is closed" note);
   registration is closed to anonymous
   callers. Pre-existing passwordless profiles (from `/api/users`) do not count
   as "set up" and cannot log in; a new account cannot reuse their username
   (409) — rename or delete such a profile first.

Forgot the only password? There is no reset flow yet; with shell access,
`UPDATE users SET password_hash = NULL` re-opens first-run registration.

## Web page

- Signed out: only the login / create-account card is shown (deep links such as
  `/transactions/new` too). Errors show inline; the password field is cleared.
- Signed in: the header shows "Signed in as <display name>" and a **Log out**
  button. uids are never shown.
- Any API call that returns 401 (expired or revoked session) switches back to
  the login card.

## Tests

`pkg/auth` (hashing, rules, tokens, throttle), `pkg/store/sessions_test.go`
(register first-run rule, sessions,
password change) and `pkg/api/auth_test.go` (first-run register, login
success/failure, disabled user, throttle, sliding expiry, logout, every
protected route → 401, password change invalidates other sessions, CSRF).
Other API tests run as a seeded logged-in user (`loggedIn` helper adds the
cookie); there is no auth bypass in production code.

## Data ownership

Transactions, balances and categories belong to the logged-in user (cookie
session or Bearer API token). Every list/get/create/update/delete is scoped
to that account; another user's uid is a **404** (existence does not leak).
A transaction may only reference the owner's balances and categories.

Migration v3 assigns any pre-existing balances and transactions to the
earliest-created user (`ORDER BY created_at ASC, id ASC`). If the database
has such rows but no users, those orphans are deleted so the migration can
apply `NOT NULL user_uid` without failing. Balance names are unique per user.

`/api/users` ownership was added later (issue #76): every user reaches only
their own account.

## Follow-ups

- **SSO** (Sign in with Apple / Google) using `user_identities`.
- Roles / admin (who may create users or edit others' profiles), disabling users via API.
- Password reset (needs email or an admin), "log out everywhere", session list.
- Persistent / global rate limiting (the throttle is per process and in memory).

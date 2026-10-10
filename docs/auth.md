# Authentication: username + password (issue #30, schema v14)

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

## Endpoints (`/api/auth`)

| Method | Path | Auth | Result |
|--------|------|------|--------|
| POST | `/api/auth/register` | first run: none; afterwards: session | `{"username","password","display_name"?}` → 201 user. First run also logs the new user in. 422 rule violations, 409 username taken, 403 `registration_closed`, 400 unknown fields (e.g. `email`). |
| POST | `/api/auth/login` | none | `{"username","password"}` → 200 user + `Set-Cookie`. Updates `last_login_at`. Unknown user, wrong password and `status != active` all give the same **401 `invalid username or password`**; an unknown user still costs one bcrypt comparison (dummy hash) so timing does not reveal which usernames exist. 429 when throttled. |
| POST | `/api/auth/logout` | none | Deletes the session (if any), clears the cookie. Always 204. |
| GET | `/api/auth/me` | session | 200 current user; 401 otherwise, with `code: "setup_required"` while no account exists (the web page then shows "Create your account"). |
| PUT | `/api/auth/password` | session | `{"current_password","new_password"}` → 204. 403 wrong current password, 422 rules. **Every other session of the user is deleted**; the current one stays. |

## What is public

Everything under `/api` needs a valid session (401 JSON
`{"error":"authentication required","code":"unauthenticated"}`) **except**:

- `POST /api/auth/login`, `POST /api/auth/register` (handler enforces first-run), `POST /api/auth/logout`
- `GET /api/healthz`
- `GET /api/openapi.yaml`, `GET /api/openapi.json` and the Swagger UI at `/swagger/` — the API
  description contains no data, so it stays public (and "Try it out" works once logged in, since the
  browser sends the cookie).

The HTML shell (`/`, `/transactions`, …) and `/static/*` are public too; they
contain no data, and the page asks `/api/auth/me` before showing anything.

The users API (`/api/users…`) is kept and now requires a session. Any
logged-in user can manage all profiles (there are no roles yet).

## CSRF

The session is a cookie, so a malicious site could try to make the browser
send requests. For every non-GET/HEAD/OPTIONS request to `/api/*`:

1. If `Sec-Fetch-Site` is present it must be `same-origin` or `none` → else 403 `code: csrf`.
2. If `Origin` is present its host must equal the request's `Host` (`null` is rejected) → else 403.
3. A request with a body must be `Content-Type: application/json` → else 415. Plain HTML forms
   cannot send that cross-site without a CORS preflight, which this server never approves.

Non-browser clients (curl, scripts) send neither header and are unaffected.
`SameSite=Lax` on the cookie is a further layer. The login endpoint is guarded
too (prevents login CSRF).

## Schema v14: `sessions`

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

Migration v13 → v14 only creates the table (`IF NOT EXISTS`) in one SQL
transaction with the `user_version` bump; no existing data is touched.
Deleting a user deletes their sessions.

## First run

1. Start the server with an empty (or existing, password-less) database.
2. Open the page: it shows **Create your account** (username, password,
   optional display name). This calls `POST /api/auth/register` and logs you in.
3. From then on the page shows **Log in**; registration is closed to anonymous
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
(migration v13→v14 incl. idempotency, register first-run rule, sessions,
password change) and `pkg/api/auth_test.go` (first-run register, login
success/failure, disabled user, throttle, sliding expiry, logout, every
protected route → 401, password change invalidates other sessions, CSRF).
Other API tests run as a seeded logged-in user (`loggedIn` helper adds the
cookie); there is no auth bypass in production code.

## Follow-ups

- **Per-user data ownership**: transactions, balances and categories are still
  shared by every account. Needs `user_uid` columns, a migration assigning
  existing rows to the first user, and scoping in every query.
- **SSO** (Sign in with Apple / Google) using `user_identities`.
- Roles / admin (who may create users or edit others' profiles), disabling users via API.
- Password reset (needs email or an admin), "log out everywhere", session list.
- Persistent / global rate limiting (the throttle is per process and in memory).

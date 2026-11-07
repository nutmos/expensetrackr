# User profiles

Status: **profiles only**. Login, sessions and endpoint protection are not
implemented yet. The schema has fields ready for basic username/password
login and SSO (Sign in with Apple, Google), so adding those later needs no
table rebuild.

## Tables

### `users`

| Column                | Type / rule                                                       | API |
|-----------------------|-------------------------------------------------------------------|-----|
| `id`                  | `INTEGER PRIMARY KEY` (internal)                                  | never exposed |
| `uid`                 | UUID v4, `NOT NULL UNIQUE`, immutable                             | `uid` (read-only) |
| `username`            | nullable, 3–32 chars `[a-z0-9._-]`, starts with letter/digit, stored lowercased; unique (`NOCASE` index) | `username` |
| `email`               | nullable, bare `name@domain.tld`, ≤ 254 chars, stored lowercased; unique (`NOCASE` index) | `email` |
| `email_verified_at`   | nullable RFC 3339; set by a future verification flow; **cleared when the email changes** | read-only |
| `display_name`        | `NOT NULL`, 1–100 chars                                           | `display_name` (required) |
| `preferences`         | `TEXT NOT NULL DEFAULT '{}'`, `CHECK (json_valid(preferences) AND json_type(preferences) = 'object')`; one JSON object per user holding only the keys the user set (see [Preferences](#preferences)) | `preferences` (object, defaults filled in) |
| `password_hash`       | nullable; a bcrypt hash written by the auth endpoints (`pkg/auth`) | **never exposed**; `has_password` (bool) instead |
| `password_updated_at` | nullable RFC 3339                                                 | read-only |
| `status`              | `active` \| `disabled` (`CHECK`), default `active`                | read-only for now |
| `last_login_at`       | nullable RFC 3339; set by future login                            | read-only |
| `created_at`, `updated_at` | RFC 3339 UTC                                                 | read-only |

Usernames and emails may be NULL for any number of users. A unique index
allows repeated NULLs.

### `user_identities` (SSO links)

| Column             | Rule |
|--------------------|------|
| `id`               | internal |
| `uid`              | UUID v4, unique |
| `user_uid`         | `NOT NULL REFERENCES users (uid) ON DELETE CASCADE` (the store opens SQLite with `foreign_keys(1)`) |
| `provider`         | lowercase identifier. The DB checks only the format; the allowed list is in Go (`user.Providers`: `apple`, `google`), so adding a provider needs no migration |
| `provider_subject` | the provider's stable user id (OIDC `sub`), 1–255 chars |
| `email`            | nullable; the email the provider reported (Apple may give a private relay address). Informational only: never used to match accounts |
| `created_at`, `last_used_at` | RFC 3339 |

`UNIQUE (provider, provider_subject)`: a provider account can be linked to
only one user. One user can have several identities (e.g. Apple and Google).

## Schema

Both tables and their indexes are part of the baseline schema
(`pkg/store/schema.go`). Transactions and balances are not tied to a user
yet.

## API

| Method | Path | Notes |
|--------|------|-------|
| POST   | `/api/users` | 201 + user, `Location: /api/users/<uid>`. Body: `display_name` (required), `username`, `email`, `preferences` (all optional) |
| GET    | `/api/users` | 200 `{"users":[...],"count":n}`, oldest first |
| GET    | `/api/users/:uid` | 200 + user |
| PUT    | `/api/users/:uid` | Full replace of the profile fields. Omitted `username`/`email` are removed; omitted `preferences` resets every preference to its default. A user with a password must keep a username: 422 `username_required` |
| PATCH  | `/api/users/:uid` | Only the fields present change. **`preferences` replaces the whole object** (no deep merge); `null` resets every preference to its default. `username`/`email`: `null` or `""` removes (not the username of a user with a password: 422 `username_required`) |
| DELETE | `/api/users/:uid` | 204; linked identities, sessions, tokens and data are deleted too. 409 `last_login_user` for the last user with a password |
| GET    | `/api/users/:uid/identities` | 200 `{"identities":[...],"count":n}`. Read-only; empty until SSO exists |
| GET    | `/api/users/:uid/preferences` | 200 + preferences (defaults filled in). Own uid only (403 otherwise) |
| PUT    | `/api/users/:uid/preferences` | Replace the whole preferences object; omitted keys go back to their defaults. Own uid only; any invalid preference is a **400** with `fields` |

Response shape:

```json
{
  "uid": "…", "username": "alex", "email": "alex@example.com", "email_verified_at": null,
  "display_name": "Alex Example", "preferences": {"base_currency": "THB"},
  "status": "active", "has_password": false, "password_updated_at": null,
  "last_login_at": null, "created_at": "2026-10-08T12:00:00Z", "updated_at": null
}
```

Errors:
- **400:** malformed JSON or unknown fields. `password` and `password_hash`
  are unknown fields, so they are rejected.
- **422:** invalid fields, for example `preferences` that is not a JSON object,
  has an unknown key, or an unsupported `base_currency` (`fields["preferences.base_currency"]`).
- **409:** a duplicate `username` or `email`, case-insensitive (`fields.username` / `fields.email`).
- **400 / 404:** a malformed / unknown uid.

Read-only fields (`uid`, `status`, `has_password`, timestamps) may be sent
back, e.g. a GET response used as a PUT body, and are ignored. Like the rest
of the API, these endpoints require a login session (see [auth.md](auth.md)).

## Preferences

Each user has their own preferences, stored as **one JSON object** in
`users.preferences` and modelled in Go as `user.Preferences` (a typed struct).

| Key | Type | Default | Rule |
|-----|------|---------|------|
| `base_currency` | ISO 4217 code | `USD` | One of the codes from `GET /api/currencies` (`pkg/money`); trimmed and upper-cased. `""` means the default |

- **Defaults:** the column stores only what the user set (`{}` for a new
  user); responses always show every key with its effective value. The
  server has no other default currency (balances and transactions always
  carry their own), so `USD` (`user.DefaultBaseCurrency`) is used as a
  neutral, widely used default.
- **Reading is tolerant:** unknown keys in a stored row are ignored and
  missing keys get their defaults, so rows written by older or newer
  versions keep working. Adding a key is a new struct field; no migration.
- **Writing is strict:** unknown keys and invalid values are rejected.
- **Scope:** `GET`/`PUT /api/users/:uid/preferences` only work for the
  logged-in user's own uid. The user resource (`GET /api/users/:uid`,
  `/api/auth/me`) includes `preferences` too.
- **Web UI:** the **Settings** tab (`/settings`) edits the base currency
  with the searchable currency dropdown. The **Add balance** form preselects
  it as the new balance's currency (it can still be changed).

Example:

```
GET /api/users/<uid>/preferences      → 200 {"base_currency":"USD"}
PUT /api/users/<uid>/preferences {"base_currency":"thb"} → 200 {"base_currency":"THB"}
PUT /api/users/<uid>/preferences {"base_currency":"XYZ"} → 400 {"error":"invalid preferences","fields":{"base_currency":"must be a supported ISO 4217 currency code (see /api/currencies)"}}
```

## Auth-readiness decisions

- **Passwords:**
  - Only a bcrypt hash is stored, in `password_hash`, written by the auth
    endpoints (`POST /api/auth/register`, `PUT /api/auth/password`; see
    [auth.md](auth.md)), which also stamp `password_updated_at`.
  - The API exposes only `has_password`. Hashes never appear in JSON
    (`json:"-"`), and the API tests check this for every response.
- **Login identifier:** `username` or `email`, both normalized to lowercase
  and unique. `status = 'disabled'` is reserved for blocking sign-in.
- **SSO:**
  - Accounts are matched only on `(provider, provider_subject)`, never on
    email. `Store.FindUserByIdentity` performs that lookup.
  - The future callback creates links with `Store.CreateIdentity`. It returns
    `ErrDuplicateIdentity` if the provider account is already linked and
    `ErrNotFound` if the user doesn't exist.
- **Email verification:** `email_verified_at` resets to NULL whenever the
  email changes.

## Follow-ups (not done)

- Login endpoints, password hashing and policy, and rate limiting.
- The SSO flows: OIDC with Google, and Sign in with Apple (including the Apple
  client-secret JWT and private relay emails).
- Sessions (a `sessions` table or signed cookies), CSRF protection, logout.
- Requiring auth on existing endpoints.
- Multi-user scoping: add `user_uid` to `transactions`, `balances` and
  `categories`, backfill existing rows to one owner, and filter every query.
- Status management (disable/enable) and email verification.
- A web UI for the rest of the profile (display name, username, email).

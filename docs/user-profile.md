# User profiles (schema v10)

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
| `preferences`         | `TEXT NOT NULL DEFAULT '{}'`, `CHECK (json_valid(preferences) AND json_type(preferences) = 'object')`; stored compacted; ≤ 32 KiB | `preferences` (JSON object) |
| `password_hash`       | nullable; a PHC-format hash string (argon2id recommended, bcrypt acceptable) | **never exposed**; `has_password` (bool) instead |
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

## Migration

v9 → v10 (`addUsers` in `pkg/store/migrate.go`) creates both tables and their
indexes with `IF NOT EXISTS`, in one SQL transaction together with the
`user_version` bump, so it is idempotent. Nothing else changes: existing
transactions, balances and categories are not tied to a user yet. New
databases get the same DDL (`usersSchema`) directly.

## API

| Method | Path | Notes |
|--------|------|-------|
| POST   | `/api/users` | 201 + user, `Location: /api/users/<uid>`. Body: `display_name` (required), `username`, `email`, `preferences` (all optional) |
| GET    | `/api/users` | 200 `{"users":[...],"count":n}`, oldest first |
| GET    | `/api/users/:uid` | 200 + user |
| PUT    | `/api/users/:uid` | Full replace of the profile fields. Omitted `username`/`email` are removed; omitted `preferences` becomes `{}` |
| PATCH  | `/api/users/:uid` | Only the fields present change. **`preferences` replaces the whole object** (no deep merge); `null` resets it to `{}`. `username`/`email`: `null` or `""` removes |
| DELETE | `/api/users/:uid` | 204; linked identities are deleted too |
| GET    | `/api/users/:uid/identities` | 200 `{"identities":[...],"count":n}`. Read-only; empty until SSO exists |

Response shape:

```json
{
  "uid": "…", "username": "nat", "email": "nat@example.com", "email_verified_at": null,
  "display_name": "Nat", "preferences": {"currency": "THB", "theme": "dark"},
  "status": "active", "has_password": false, "password_updated_at": null,
  "last_login_at": null, "created_at": "2026-10-08T12:00:00Z", "updated_at": null
}
```

Errors:
- **400:** malformed JSON or unknown fields. `password` and `password_hash`
  are unknown fields, so they are rejected.
- **422:** invalid fields, for example `preferences` that is not a JSON object.
- **409:** a duplicate `username` or `email`, case-insensitive (`fields.username` / `fields.email`).
- **400 / 404:** a malformed / unknown uid.

Read-only fields (`uid`, `status`, `has_password`, timestamps) may be sent
back, e.g. a GET response used as a PUT body, and are ignored. Like the rest
of the API, these endpoints do not require authentication yet.

## Auth-readiness decisions

- **Passwords:**
  - Only a hash is stored, in `password_hash`. The future login code must hash
    with argon2id (or bcrypt) and call `Store.SetPasswordHash`. That method
    also stamps `password_updated_at` and is not exposed over HTTP.
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
- A web UI for the profile and preferences.

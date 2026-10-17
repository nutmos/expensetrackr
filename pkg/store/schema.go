package store

// baselineSchema is the complete v1 schema, created in an empty database by
// migration 0 -> 1 (createBaseline). Do not edit it once a release depends on
// it; change the schema by appending a migration instead (see migrate.go).
//
// Table "transactions" (one row per transaction):
//   - uid: server-assigned UUID v4, immutable, unique.
//   - amount_minor: integer minor units (e.g. 12050 = 120.50 THB). No floats.
//   - amount_scale: number of decimal places used for amount_minor (ISO 4217
//     exponent at the time of writing), so each row is self-describing.
//   - balance_uid: UUID of the paying balance (payment_account or credit_card).
//     No FK: deleting a balance leaves the uid and the account name snapshot.
//   - account: denormalized balance name at write time (display if the
//     balance is later renamed or deleted).
//   - type: expense | income | transfer | asset_purchase | balance_adjustment.
//   - to_balance_uid, to_account: destination of a transfer / asset purchase.
//   - category_uid: uid of one of the owner's categories (user_categories),
//     or NULL.
//   - spent_at: RFC 3339 text exactly as entered (offset preserved),
//     e.g. 2026-10-06T21:06:00+08:00.
//   - spent_at_unix: the same instant as Unix seconds, used for ordering and
//     from/to filtering regardless of the offset each row was entered with.
//   - created_at / updated_at: RFC 3339 UTC; updated_at is NULL until edited.
//   - version: optimistic-locking counter (1 on insert, +1 per update).
//   - balance_applied: 1 if the row moved its balances; 0 for
//     balance_adjustment rows, which never do.
//   - adjustment_direction: set exactly for type 'balance_adjustment' rows,
//     which are written by the server when a balance amount is edited by
//     hand: they never carry a destination or category and never moved a
//     balance, so editing history can't double-count them.
//   - to_amount_minor, to_amount_scale, to_currency, exchange_rate,
//     rate_source, rate_date: cross-currency transfers
//     (docs/exchange-rates.md); NULL otherwise.
//   - import_ref: optional client reference for bulk import idempotency,
//     unique per balance via the partial index idx_transactions_import_ref
//     (docs/bulk-import.md).
//
// Table "balances": one row per payment account, credit card, other asset or
// other liability.
//   - uid: server-assigned UUID v4, immutable, unique; clients never set it.
//   - name: unique case-insensitively (ASCII folding, SQLite NOCASE).
//   - type: payment_account | credit_card | other_asset | other_liability.
//   - amount_scale: minor-unit digits of currency for the *_minor columns.
//   - balance_minor: asset types only (required), may be negative (overdraft).
//   - debt_minor, limit_minor: liability types only (both required). Debt may
//     be negative (overpaid, in credit) and may exceed the limit (reported as
//     over_limit by the API); limit >= 0.
//   - version: optimistic-locking counter, 1 on insert, +1 on every change
//     (manual PUT/PATCH or a transaction moving the balance).
//
// Table "users": one row per person.
//   - uid: server-assigned UUID v4, immutable, unique (FK target).
//   - username, email: optional login identifiers, stored lowercased by the
//     application, unique when set (case-insensitive indexes; NULLs allowed
//     many times).
//   - preferences: JSON object (json_valid + json_type = 'object'), '{}'
//     default.
//   - password_hash: PHC string (argon2id). NULL = no password. Never
//     returned by the API.
//   - status: active | disabled (reserved for blocking sign-in).
//   - email_verified_at, password_updated_at, last_login_at: set by auth
//     flows; read-only in the API.
//
// Table "user_identities": linked SSO accounts (Sign in with Apple / Google).
//   - (provider, provider_subject) unique: the provider's stable "sub" id.
//   - provider: lowercase identifier; the allowed list lives in Go
//     (pkg/user.Providers) so new providers need no migration.
//   - user_uid references users(uid) ON DELETE CASCADE (foreign_keys is on).
//
// Table "sessions": server-side login sessions.
//   - token_hash: SHA-256 (hex) of the random cookie token; the raw token is
//     never stored.
//   - user_uid: references users(uid); sessions go with their user.
//   - created_at, expires_at, last_seen_at: RFC 3339 UTC. expires_at slides
//     forward on use (see pkg/auth.SessionTTL).
//   - user_agent: optional, informational only.
//
// Table "user_categories": one row per user, all of a user's categories as a
// JSON array (docs/categories.md), created lazily on the first write (a
// missing row reads as no categories).
//   - categories: JSON array of {uid, name, type, description, created_at,
//     updated_at}; uids are globally unique UUID v4 (transactions link to
//     them by transactions.category_uid). Validated in Go (pkg/category,
//     store limits); the CHECK only guarantees a JSON array.
//   - version: optimistic-locking counter (1 on insert, +1 per write); every
//     write is a read-modify-write that requires the version it read.
//   - user_uid references users(uid); the row goes with its user.
//
// Table "meta": small key/value table (JSON values) for server bookkeeping.
// Empty in a new database; no key is currently in use.
//
// Tables "fx_rates" and "fx_rate_lookups": Frankfurter (ECB) rate cache
// (docs/exchange-rates.md).
//   - fx_rates: one rate per (base, quote, rate_date), the date Frankfurter
//     reported. Historical ECB rates never change, so rows are kept forever.
//     rate is the decimal text as received.
//   - fx_rate_lookups: requested date ('latest' or YYYY-MM-DD) -> rate_date
//     actually returned (weekends/holidays map to the previous working day).
//     fetched_at lets the client expire 'latest' and recent dates after a few
//     hours (ECB may not have published that day's rate yet).
const baselineSchema = `
CREATE TABLE transactions (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    uid           TEXT    NOT NULL UNIQUE CHECK (length(uid) = 36),
    amount_minor  INTEGER NOT NULL CHECK (amount_minor > 0),
    amount_scale  INTEGER NOT NULL CHECK (amount_scale BETWEEN 0 AND 4),
    currency      TEXT    NOT NULL CHECK (length(currency) = 3),
    balance_uid   TEXT    NOT NULL CHECK (length(balance_uid) = 36),
    account       TEXT    NOT NULL CHECK (length(account) > 0),
    type          TEXT    NOT NULL DEFAULT 'expense' CHECK (type IN ('expense', 'income', 'transfer', 'asset_purchase', 'balance_adjustment')),
    to_balance_uid TEXT   CHECK (to_balance_uid IS NULL OR length(to_balance_uid) = 36),
    to_account    TEXT,
    category_uid  TEXT    CHECK (category_uid IS NULL OR length(category_uid) = 36),
    spent_at      TEXT    NOT NULL,
    spent_at_unix INTEGER NOT NULL,
    note          TEXT    NOT NULL DEFAULT '',
    created_at    TEXT    NOT NULL,
    updated_at    TEXT,
    version       INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    balance_applied INTEGER NOT NULL DEFAULT 0 CHECK (balance_applied IN (0, 1)),
    adjustment_direction TEXT CHECK (adjustment_direction IS NULL OR adjustment_direction IN ('increase', 'decrease')),
    to_amount_minor INTEGER CHECK (to_amount_minor IS NULL OR to_amount_minor > 0),
    to_amount_scale INTEGER CHECK (to_amount_scale IS NULL OR to_amount_scale BETWEEN 0 AND 4),
    to_currency   TEXT    CHECK (to_currency IS NULL OR length(to_currency) = 3),
    exchange_rate TEXT,
    rate_source   TEXT    CHECK (rate_source IS NULL OR rate_source IN ('frankfurter', 'manual')),
    rate_date     TEXT    CHECK (rate_date IS NULL OR length(rate_date) = 10),
    import_ref    TEXT    CHECK (import_ref IS NULL OR length(import_ref) BETWEEN 1 AND 200),
    CHECK ((type = 'balance_adjustment') = (adjustment_direction IS NOT NULL)),
    CHECK (type <> 'balance_adjustment' OR (to_balance_uid IS NULL AND category_uid IS NULL AND balance_applied = 0))
);
CREATE INDEX idx_transactions_spent_at_unix ON transactions (spent_at_unix DESC, id DESC);
CREATE INDEX idx_transactions_balance_uid ON transactions (balance_uid);
CREATE UNIQUE INDEX idx_transactions_uid ON transactions (uid);
CREATE INDEX idx_transactions_type ON transactions (type, spent_at_unix DESC);
CREATE INDEX idx_transactions_category_uid ON transactions (category_uid);
CREATE INDEX idx_transactions_to_balance_uid ON transactions (to_balance_uid);
CREATE UNIQUE INDEX idx_transactions_import_ref ON transactions (balance_uid, import_ref) WHERE import_ref IS NOT NULL;

CREATE TABLE balances (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    uid           TEXT    NOT NULL UNIQUE CHECK (length(uid) = 36),
    name          TEXT    NOT NULL COLLATE NOCASE UNIQUE CHECK (length(trim(name)) > 0),
    type          TEXT    NOT NULL CHECK (type IN ('payment_account', 'credit_card', 'other_asset', 'other_liability')),
    currency      TEXT    NOT NULL CHECK (length(currency) = 3),
    description   TEXT    NOT NULL DEFAULT '',
    amount_scale  INTEGER NOT NULL CHECK (amount_scale BETWEEN 0 AND 4),
    balance_minor INTEGER,
    debt_minor    INTEGER,
    limit_minor   INTEGER CHECK (limit_minor >= 0),
    created_at    TEXT    NOT NULL,
    updated_at    TEXT,
    version       INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    CHECK (
        (type IN ('payment_account', 'other_asset')
            AND balance_minor IS NOT NULL AND debt_minor IS NULL AND limit_minor IS NULL)
        OR
        (type IN ('credit_card', 'other_liability')
            AND balance_minor IS NULL AND debt_minor IS NOT NULL AND limit_minor IS NOT NULL)
    )
);
CREATE INDEX idx_balances_type ON balances (type, name);
CREATE UNIQUE INDEX idx_balances_uid ON balances (uid);

CREATE TABLE users (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    uid                 TEXT    NOT NULL UNIQUE CHECK (length(uid) = 36),
    username            TEXT    CHECK (username IS NULL OR (length(username) BETWEEN 3 AND 32 AND username = lower(username))),
    email               TEXT    CHECK (email IS NULL OR (length(email) BETWEEN 3 AND 254 AND email = lower(email))),
    email_verified_at   TEXT,
    display_name        TEXT    NOT NULL CHECK (length(trim(display_name)) > 0),
    preferences         TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(preferences) AND json_type(preferences) = 'object'),
    password_hash       TEXT    CHECK (password_hash IS NULL OR length(password_hash) > 0),
    password_updated_at TEXT,
    status              TEXT    NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    last_login_at       TEXT,
    created_at          TEXT    NOT NULL,
    updated_at          TEXT
);
CREATE UNIQUE INDEX idx_users_username ON users (username COLLATE NOCASE);
CREATE UNIQUE INDEX idx_users_email ON users (email COLLATE NOCASE);

CREATE TABLE user_identities (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    uid              TEXT    NOT NULL UNIQUE CHECK (length(uid) = 36),
    user_uid         TEXT    NOT NULL REFERENCES users (uid) ON DELETE CASCADE,
    provider         TEXT    NOT NULL CHECK (length(provider) BETWEEN 1 AND 32 AND provider = lower(provider)),
    provider_subject TEXT    NOT NULL CHECK (length(provider_subject) BETWEEN 1 AND 255),
    email            TEXT,
    created_at       TEXT    NOT NULL,
    last_used_at     TEXT,
    UNIQUE (provider, provider_subject)
);
CREATE INDEX idx_user_identities_user_uid ON user_identities (user_uid);

CREATE TABLE sessions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    token_hash   TEXT    NOT NULL UNIQUE CHECK (length(token_hash) = 64),
    user_uid     TEXT    NOT NULL REFERENCES users (uid) ON DELETE CASCADE,
    created_at   TEXT    NOT NULL,
    expires_at   TEXT    NOT NULL,
    last_seen_at TEXT    NOT NULL,
    user_agent   TEXT
);
CREATE INDEX idx_sessions_user_uid ON sessions (user_uid);
CREATE INDEX idx_sessions_expires_at ON sessions (expires_at);

CREATE TABLE user_categories (
    user_uid   TEXT    PRIMARY KEY REFERENCES users (uid) ON DELETE CASCADE,
    categories TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(categories) AND json_type(categories) = 'array'),
    version    INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at TEXT    NOT NULL,
    updated_at TEXT
);

CREATE TABLE meta (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL CHECK (json_valid(value)),
    updated_at TEXT NOT NULL
);

CREATE TABLE fx_rates (
    base       TEXT NOT NULL CHECK (length(base) = 3),
    quote      TEXT NOT NULL CHECK (length(quote) = 3),
    rate_date  TEXT NOT NULL CHECK (length(rate_date) = 10),
    rate       TEXT NOT NULL,
    fetched_at TEXT NOT NULL,
    PRIMARY KEY (base, quote, rate_date)
);
CREATE TABLE fx_rate_lookups (
    base           TEXT NOT NULL,
    quote          TEXT NOT NULL,
    requested_date TEXT NOT NULL,
    rate_date      TEXT NOT NULL,
    fetched_at     TEXT NOT NULL,
    PRIMARY KEY (base, quote, requested_date)
);
`

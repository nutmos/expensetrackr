package store

// baselineSchema is the complete v1 Postgres schema, created in an empty
// database by migration 0 -> 1 (createBaseline). Do not edit it once a
// release depends on it; change the schema by appending a migration instead
// (see migrate.go).
//
// Conventions:
//   - id: internal bigint identity, never exposed by the API.
//   - uid: the public id, a uuid (server-assigned v4, immutable).
//   - Times are timestamptz, written and read in UTC at second precision
//     (the API formats them as RFC 3339 "...Z").
//   - JSON documents are jsonb. Case-insensitive uniqueness uses lower(...)
//     indexes. Every unique constraint and index has an explicit name; the
//     store maps violations by name (see pgerr.go).
//   - Amounts are bigint minor units with their scale (ISO 4217 exponent at
//     the time of writing) next to them. No floats anywhere.
//
// Table "users": one row per person.
//   - username, email: optional login identifiers, stored lowercased by the
//     application, unique case-insensitively (NULLs allowed many times).
//   - preferences: jsonb object of the keys the user set ('{}' default).
//   - password_hash: bcrypt hash (pkg/auth); NULL = no password. Never
//     returned by the API.
//   - status: active | disabled.
//
// Table "user_identities": linked SSO accounts (Sign in with Apple / Google);
// (provider, provider_subject) is unique.
//
// Table "sessions": server-side login sessions. token_hash is the SHA-256
// (hex) of the cookie token; the raw token is never stored.
//
// Table "api_tokens": personal API tokens. token_hash is the SHA-256 (hex)
// of the token; prefix is a non-secret hint; scopes is space-separated.
//
// Table "user_categories": one row per user holding all of the user's
// categories as a jsonb array (docs/categories.md), created on the first
// write. version is the row's optimistic-locking counter.
//
// Table "balances": payment accounts, credit cards, other assets and other
// liabilities of one user (user_uid).
//   - name: unique per user, case-insensitively.
//   - balance_minor: asset types only (may be negative: overdraft).
//   - debt_minor, limit_minor: liability types only; debt may be negative
//     (in credit) and may exceed the limit; limit >= 0.
//   - version: optimistic-locking counter, +1 on every change (manual PUT or
//     a transaction moving the balance).
//
// Table "transactions" (one row per transaction, owned by user_uid):
//   - balance_uid / to_balance_uid: source and destination balances. No
//     foreign key: deleting a balance keeps its uid and the account / to_account
//     name snapshots.
//   - spent_at: the RFC 3339 text exactly as entered, offset included (a
//     timestamptz would lose the offset). spent_at_utc is the same instant,
//     used for ordering, filtering and paging.
//   - balance_applied: whether the row moved its balances (false only for
//     balance_adjustment rows, recorded by manual balance edits).
//   - to_amount_minor ... rate_date: cross-currency transfers
//     (docs/exchange-rates.md); NULL otherwise. Rates are decimal text as
//     received or computed (exact, never floats).
//   - import_ref: optional client reference for bulk-import idempotency,
//     unique per balance (docs/bulk-import.md).
//
// Tables "fx_rates" and "fx_rate_lookups": Frankfurter (ECB) rate cache.
// fx_rates holds one rate per (base, quote, rate_date); fx_rate_lookups maps a
// requested date ('latest' or YYYY-MM-DD) to the rate date returned.
const baselineSchema = `
CREATE TABLE users (
    id                  bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    uid                 uuid        NOT NULL CONSTRAINT users_uid_key UNIQUE,
    username            text        CHECK (username IS NULL OR (char_length(username) BETWEEN 3 AND 32 AND username = lower(username))),
    email               text        CHECK (email IS NULL OR (char_length(email) BETWEEN 3 AND 254 AND email = lower(email))),
    email_verified_at   timestamptz,
    display_name        text        NOT NULL CHECK (char_length(btrim(display_name)) > 0),
    preferences         jsonb       NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(preferences) = 'object'),
    password_hash       text        CHECK (password_hash IS NULL OR password_hash <> ''),
    password_updated_at timestamptz,
    status              text        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    last_login_at       timestamptz,
    created_at          timestamptz NOT NULL,
    updated_at          timestamptz
);
CREATE UNIQUE INDEX users_username_uq ON users (lower(username));
CREATE UNIQUE INDEX users_email_uq ON users (lower(email));

CREATE TABLE user_identities (
    id               bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    uid              uuid        NOT NULL CONSTRAINT user_identities_uid_key UNIQUE,
    user_uid         uuid        NOT NULL CONSTRAINT user_identities_user_fk REFERENCES users (uid) ON DELETE CASCADE,
    provider         text        NOT NULL CHECK (char_length(provider) BETWEEN 1 AND 32 AND provider = lower(provider)),
    provider_subject text        NOT NULL CHECK (char_length(provider_subject) BETWEEN 1 AND 255),
    email            text,
    created_at       timestamptz NOT NULL,
    last_used_at     timestamptz,
    CONSTRAINT user_identities_provider_subject_uq UNIQUE (provider, provider_subject)
);
CREATE INDEX user_identities_user_uid_idx ON user_identities (user_uid);

CREATE TABLE sessions (
    id           bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    token_hash   text        NOT NULL CONSTRAINT sessions_token_hash_key UNIQUE CHECK (char_length(token_hash) = 64),
    user_uid     uuid        NOT NULL REFERENCES users (uid) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL,
    expires_at   timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    user_agent   text
);
CREATE INDEX sessions_user_uid_idx ON sessions (user_uid);
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);

CREATE TABLE api_tokens (
    id           bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    uid          uuid        NOT NULL CONSTRAINT api_tokens_uid_key UNIQUE,
    user_uid     uuid        NOT NULL REFERENCES users (uid) ON DELETE CASCADE,
    name         text        NOT NULL,
    prefix       text        NOT NULL,
    token_hash   text        NOT NULL CONSTRAINT api_tokens_token_hash_key UNIQUE CHECK (char_length(token_hash) = 64),
    scopes       text        NOT NULL,
    created_at   timestamptz NOT NULL,
    last_used_at timestamptz,
    expires_at   timestamptz,
    revoked_at   timestamptz
);
CREATE INDEX api_tokens_user_uid_idx ON api_tokens (user_uid);

CREATE TABLE user_categories (
    user_uid   uuid        PRIMARY KEY REFERENCES users (uid) ON DELETE CASCADE,
    categories jsonb       NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(categories) = 'array'),
    version    bigint      NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at timestamptz NOT NULL,
    updated_at timestamptz
);

CREATE TABLE balances (
    id            bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    uid           uuid        NOT NULL CONSTRAINT balances_uid_key UNIQUE,
    user_uid      uuid        NOT NULL REFERENCES users (uid) ON DELETE CASCADE,
    name          text        NOT NULL CHECK (char_length(btrim(name)) > 0),
    type          text        NOT NULL CHECK (type IN ('payment_account', 'credit_card', 'other_asset', 'other_liability')),
    currency      char(3)     NOT NULL,
    description   text        NOT NULL DEFAULT '',
    amount_scale  smallint    NOT NULL CHECK (amount_scale BETWEEN 0 AND 4),
    balance_minor bigint,
    debt_minor    bigint,
    limit_minor   bigint      CHECK (limit_minor >= 0),
    created_at    timestamptz NOT NULL,
    updated_at    timestamptz,
    version       bigint      NOT NULL DEFAULT 1 CHECK (version >= 1),
    CHECK (
        (type IN ('payment_account', 'other_asset')
            AND balance_minor IS NOT NULL AND debt_minor IS NULL AND limit_minor IS NULL)
        OR
        (type IN ('credit_card', 'other_liability')
            AND balance_minor IS NULL AND debt_minor IS NOT NULL AND limit_minor IS NOT NULL)
    )
);
CREATE UNIQUE INDEX balances_user_name_uq ON balances (user_uid, lower(name));
CREATE INDEX balances_user_type_idx ON balances (user_uid, type, name);

CREATE TABLE transactions (
    id                   bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    uid                  uuid        NOT NULL CONSTRAINT transactions_uid_key UNIQUE,
    user_uid             uuid        NOT NULL REFERENCES users (uid) ON DELETE CASCADE,
    type                 text        NOT NULL DEFAULT 'expense' CHECK (type IN ('expense', 'income', 'transfer', 'asset_purchase', 'return_on_investment', 'balance_adjustment')),
    amount_minor         bigint      NOT NULL CHECK (amount_minor > 0),
    amount_scale         smallint    NOT NULL CHECK (amount_scale BETWEEN 0 AND 4),
    currency             char(3)     NOT NULL,
    balance_uid          uuid        NOT NULL,
    account              text        NOT NULL CHECK (account <> ''),
    to_balance_uid       uuid,
    to_account           text,
    category_uid         uuid,
    spent_at             text        NOT NULL,
    spent_at_utc         timestamptz NOT NULL,
    note                 text        NOT NULL DEFAULT '',
    created_at           timestamptz NOT NULL,
    updated_at           timestamptz,
    version              bigint      NOT NULL DEFAULT 1 CHECK (version >= 1),
    balance_applied      boolean     NOT NULL DEFAULT false,
    adjustment_direction text        CHECK (adjustment_direction IN ('increase', 'decrease')),
    to_amount_minor      bigint      CHECK (to_amount_minor > 0),
    to_amount_scale      smallint    CHECK (to_amount_scale BETWEEN 0 AND 4),
    to_currency          char(3),
    exchange_rate        text,
    rate_source          text        CHECK (rate_source IN ('frankfurter', 'manual')),
    rate_date            date,
    import_ref           text        CHECK (char_length(import_ref) BETWEEN 1 AND 200),
    CHECK ((type = 'balance_adjustment') = (adjustment_direction IS NOT NULL)),
    CHECK (type <> 'balance_adjustment' OR (to_balance_uid IS NULL AND category_uid IS NULL AND NOT balance_applied))
);
CREATE INDEX transactions_user_spent_idx ON transactions (user_uid, spent_at_utc DESC, id DESC);
CREATE INDEX transactions_balance_uid_idx ON transactions (balance_uid);
CREATE INDEX transactions_to_balance_uid_idx ON transactions (to_balance_uid);
CREATE INDEX transactions_category_uid_idx ON transactions (category_uid);
CREATE UNIQUE INDEX transactions_import_ref_uq ON transactions (balance_uid, import_ref) WHERE import_ref IS NOT NULL;

CREATE TABLE fx_rates (
    base       char(3)     NOT NULL,
    quote      char(3)     NOT NULL,
    rate_date  date        NOT NULL,
    rate       text        NOT NULL,
    fetched_at timestamptz NOT NULL,
    PRIMARY KEY (base, quote, rate_date)
);
CREATE TABLE fx_rate_lookups (
    base           char(3)     NOT NULL,
    quote          char(3)     NOT NULL,
    requested_date text        NOT NULL,
    rate_date      date        NOT NULL,
    fetched_at     timestamptz NOT NULL,
    PRIMARY KEY (base, quote, requested_date)
);
`

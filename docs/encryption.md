# Encryption at rest

[← Back to the README](../README.md) · [All docs](README.md)

With an encryption key set, the server stores your personal data in Postgres
**encrypted** (issue #192). Anyone who gets a copy of the database (a backup,
a disk image or a `psql` session) sees ciphertext instead of what you spent
money on and where. The server decrypts the data when it reads it, so the
web page, the API and the MCP server work exactly as before.

It is **off by default**. Existing databases are encrypted the first time
the server starts with a key.

## Turn it on

1. Make a key (32 random bytes, base64):

   ```bash
   expense-server generate-encryption-key
   # or: openssl rand -base64 32
   # or, in Docker: docker run --rm --entrypoint /usr/local/bin/expense-server ghcr.io/nutmos/expensetrackr generate-encryption-key
   ```

2. **Keep a copy of the key somewhere safe, away from the database** (a
   password manager, for example). Without it the encrypted data cannot be
   read back by anyone, including you.

3. Start the server with the key in `EXPENSE_ENCRYPTION_KEY`, or with the
   path of a file that holds it in `EXPENSE_ENCRYPTION_KEY_FILE` (or the
   `-encryption-key-file` flag), for example a Docker secret:

   ```bash
   EXPENSE_ENCRYPTION_KEY='…' make run
   EXPENSE_ENCRYPTION_KEY='…' make up          # compose.yaml passes it to the app
   ```

   The key itself is never accepted as a flag, so it stays out of process
   lists. On the Lightsail deployment, set the repository secret
   `EXPENSE_ENCRYPTION_KEY` ([Deploying](deploy.md#settings)).

On start the server logs `encryption at rest: on (key id 1a2b3c4d)`. The
first start with a key encrypts every existing value in one database
transaction and logs `store: encrypted N existing value(s) with key …`.

From then on the database **requires** the key: a server started without it
stops with `the database is encrypted: set EXPENSE_ENCRYPTION_KEY to its
key`, and one started with another key stops with `the database is
encrypted with a different key`. Neither changes anything or shows any
ciphertext. `expense-server reset-password` needs the key too (it reads the
same variables).

There is no way to turn encryption off again for a database once it is on.

## What is encrypted

| Table | Encrypted columns |
|---|---|
| `transactions` | `note`, `account`, `to_account` (the balance names copied onto each transaction) |
| `balances` | `name`, `description` |
| `user_categories` | `categories`: the whole category list (names, descriptions), stored as a JSON string |
| `users` | `display_name` |

What stays in clear, and why:

- **Amounts, currencies, dates, types and ids**: Postgres adds them up for
  the reports, filters transactions by date and type, and orders them by
  date. Encrypting them would mean loading every transaction into the
  server for each report and page. Without the text above, they show
  amounts and dates but not what they were for.
- **Usernames and email addresses**: login looks users up by them, and they
  must be unique.
- **Password hashes, session and API token hashes**: already one-way hashes.
- **API token names, session user agents, import references, exchange rates**.

Empty values (an empty note or description) stay empty.

## How it works

Each value is encrypted with **AES-256-GCM** and a fresh random 96-bit nonce,
so two equal values give different ciphertexts, and any change to a stored
value is detected (it fails to decrypt instead of decrypting to something
else). Stored values look like

```
enc1:1a2b3c4d:qz0F…
```

where `1a2b3c4d` is the **key id**: the first 4 bytes of the SHA-256 of the
key, in hex. It tells which key encrypted the value and is authenticated with
it. The code is in [`pkg/fieldcrypt`](../pkg/fieldcrypt/fieldcrypt.go); the
store seals values as it writes them and opens them as it reads them
([`pkg/store/encryption.go`](../pkg/store/encryption.go)).

The table `encryption_state` (schema v10) has one row once the database is
encrypted: the key id and a known value encrypted with that key, which is how
the server recognises a missing or wrong key at startup.

Because names are encrypted, the balance list is sorted by name in the
server rather than in SQL (type first, then name ignoring case).

## Change the key (rotation)

1. Make a new key (`expense-server generate-encryption-key`).
2. Restart the server with the **new** key in `EXPENSE_ENCRYPTION_KEY` and
   the **old** one in `EXPENSE_ENCRYPTION_OLD_KEYS` (comma-separated if
   there are several). On start it re-encrypts every value with the new key
   in one transaction (`store: re-encrypted N value(s) with key …`).
3. Afterwards the old key is no longer needed: remove it from
   `EXPENSE_ENCRYPTION_OLD_KEYS`. A server started with only the old key now
   stops with `the database is encrypted with a different key`.

## What it does not protect against

Encryption at rest protects the database and its backups. It does **not**
protect against someone who can read the server's environment or memory
(they have the key), or who can log in to the app as you. Keep the key out
of the database's backups, and keep using TLS between the server and a
remote Postgres (`sslmode=require` or stricter in `EXPENSE_DATABASE_URL`) so
the decrypted data is not readable on the network either: the server
decrypts values itself, so what it sends to the browser is in clear, over
your HTTPS.

## Settings

| Flag | Env var | Meaning |
|---|---|---|
| (none) | `EXPENSE_ENCRYPTION_KEY` | The key: 32 bytes as base64 (standard or URL-safe, padded or not) or 64 hex digits. Empty: encryption off |
| `-encryption-key-file` | `EXPENSE_ENCRYPTION_KEY_FILE` | A file holding the key instead (surrounding whitespace ignored). Not together with `EXPENSE_ENCRYPTION_KEY` |
| (none) | `EXPENSE_ENCRYPTION_OLD_KEYS` | Earlier keys, comma-separated, while rotating |

## Tests

`pkg/fieldcrypt` has unit tests for the format, tampering, wrong keys and
rotation. `pkg/store/encryption_test.go` checks that nothing personal is in
clear in the tables, that an existing database is encrypted on the first
start with a key, that the key is then required, and rotation.

Set `EXPENSE_TEST_ENCRYPTION_KEY` to run **every** store and API test with
encryption on; CI runs them both ways:

```bash
EXPENSE_TEST_ENCRYPTION_KEY="$(openssl rand -base64 32)" make test
```

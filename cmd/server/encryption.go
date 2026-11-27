package main

// Encryption at rest (issue #192, docs/encryption.md): the key comes from
// EXPENSE_ENCRYPTION_KEY or a file (EXPENSE_ENCRYPTION_KEY_FILE /
// -encryption-key-file, e.g. a Docker secret), never from a flag value, so
// it stays out of process lists. EXPENSE_ENCRYPTION_OLD_KEYS lists earlier
// keys while rotating. `expense-server generate-encryption-key` prints a new
// key.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/nutmos/expensetrackr/pkg/fieldcrypt"
)

// generateKeyCmd is the subcommand that prints a new key.
const generateKeyCmd = "generate-encryption-key"

// runGenerateKey prints a new random key on stdout; the exit code is 0 or 1.
func runGenerateKey(stdout, stderr io.Writer) int {
	k, err := fieldcrypt.GenerateKey()
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", generateKeyCmd, err)
		return 1
	}
	fmt.Fprintln(stdout, k)
	return 0
}

// loadKeyring builds the keyring from the key (EXPENSE_ENCRYPTION_KEY), the
// path of a file holding it, and the comma-separated old keys. No key and no
// file: nil (encryption off).
func loadKeyring(key, keyFile, oldKeys string) (*fieldcrypt.Keyring, error) {
	if keyFile != "" {
		if key != "" {
			return nil, errors.New("set either EXPENSE_ENCRYPTION_KEY or the key file, not both")
		}
		b, err := os.ReadFile(keyFile)
		if err != nil {
			return nil, fmt.Errorf("encryption key file: %w", err)
		}
		key = strings.TrimSpace(string(b))
		if key == "" {
			return nil, fmt.Errorf("encryption key file %s is empty", keyFile)
		}
	}
	return fieldcrypt.ParseKeyring(key, oldKeys)
}

// keyringFromEnv is loadKeyring with the key and old keys from the
// environment and keyFile from the flag (or its environment variable).
func keyringFromEnv(keyFile string) (*fieldcrypt.Keyring, error) {
	return loadKeyring(os.Getenv("EXPENSE_ENCRYPTION_KEY"), keyFile, os.Getenv("EXPENSE_ENCRYPTION_OLD_KEYS"))
}

// keyFileUsage is the help text of the -encryption-key-file flag.
const keyFileUsage = "file holding the key that encrypts personal data at rest (env EXPENSE_ENCRYPTION_KEY_FILE); or put the key itself in EXPENSE_ENCRYPTION_KEY; see docs/encryption.md"

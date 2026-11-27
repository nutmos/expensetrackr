package main

// `expense-server reset-password`: set a new password for a user straight in
// the database, for when it is forgotten (there is no email, so no reset
// link). Whoever can run it can already read the database, so it asks for
// nothing else. See docs/auth.md "Forgotten password".

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/nutmos/expensetrackr/pkg/auth"
	"github.com/nutmos/expensetrackr/pkg/store"
	"github.com/nutmos/expensetrackr/pkg/user"
)

// resetPasswordCmd is the subcommand name: expense-server reset-password ...
const resetPasswordCmd = "reset-password"

// resetPasswordTimeout bounds the whole command (connect, migrate, update).
const resetPasswordTimeout = time.Minute

// runResetPassword runs the subcommand with args (after "reset-password")
// and returns the exit code: 0 done, 1 failed, 2 bad usage.
func runResetPassword(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet(resetPasswordCmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbURL := fs.String("database-url", envOr("EXPENSE_DATABASE_URL", ""), "Postgres connection URL (env EXPENSE_DATABASE_URL; required)")
	keyFile := fs.String("encryption-key-file", envOr("EXPENSE_ENCRYPTION_KEY_FILE", ""), keyFileUsage)
	fromStdin := fs.Bool("password-stdin", false, "read the new password from the first line of standard input instead of generating one")
	fs.Usage = func() {
		fmt.Fprintf(stderr, `Usage: expense-server %s [flags] USERNAME

Sets a new password for USERNAME and logs the user out everywhere (API
tokens keep working). Without -password-stdin a random password is
generated and printed; change it afterwards on the Settings page.

Flags:
`, resetPasswordCmd)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 || strings.TrimSpace(fs.Arg(0)) == "" {
		fmt.Fprintln(stderr, "reset-password: expected exactly one USERNAME (flags go before it)")
		fs.Usage()
		return 2
	}
	username := strings.ToLower(strings.TrimSpace(fs.Arg(0)))
	if *dbURL == "" {
		fmt.Fprintln(stderr, "reset-password: no database: set -database-url or EXPENSE_DATABASE_URL")
		return 2
	}

	var password string
	generated := !*fromStdin
	if generated {
		password = rand.Text() // 26 base32 characters, 130 bits
	} else {
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			fmt.Fprintf(stderr, "reset-password: read password: %v\n", err)
			return 1
		}
		password = strings.TrimRight(line, "\r\n")
		if msg := auth.CheckPasswordRules(password); msg != "" {
			fmt.Fprintf(stderr, "reset-password: the password %s\n", msg)
			return 2
		}
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		fmt.Fprintf(stderr, "reset-password: hash password: %v\n", err)
		return 1
	}

	keys, err := keyringFromEnv(*keyFile)
	if err != nil {
		fmt.Fprintf(stderr, "reset-password: encryption key: %v\n", err)
		return 2
	}
	st, err := store.Open(*dbURL, store.WithKeyring(keys))
	if err != nil {
		fmt.Fprintf(stderr, "reset-password: open database: %v\n", err)
		return 1
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), resetPasswordTimeout)
	defer cancel()

	u, err := st.GetUserByUsername(ctx, username)
	if errors.Is(err, store.ErrNotFound) {
		fmt.Fprintf(stderr, "reset-password: no user %q\n", username)
		return 1
	} else if err != nil {
		fmt.Fprintf(stderr, "reset-password: %v\n", err)
		return 1
	}
	if err := st.ResetPassword(ctx, u.UID, hash); err != nil {
		fmt.Fprintf(stderr, "reset-password: %v\n", err)
		return 1
	}

	fmt.Fprintf(stderr, "Password of %q reset; all of its sessions were logged out.\n", username)
	if u.Status != user.StatusActive {
		fmt.Fprintf(stderr, "Note: the account is %q, so it still can't log in.\n", u.Status)
	}
	fmt.Fprintln(stderr, "After several wrong attempts a running server may refuse logins for up to 5 more minutes.")
	if generated {
		fmt.Fprintln(stderr, "New password (change it on the Settings page after logging in):")
		fmt.Fprintln(stdout, password)
	}
	return 0
}

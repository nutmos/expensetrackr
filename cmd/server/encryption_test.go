package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/fieldcrypt"
)

func TestLoadKeyring(t *testing.T) {
	key, _ := fieldcrypt.GenerateKey()
	old, _ := fieldcrypt.GenerateKey()
	want, _ := fieldcrypt.ParseKey(key)
	oldKey, _ := fieldcrypt.ParseKey(old)

	if r, err := loadKeyring("", "", ""); r != nil || err != nil {
		t.Errorf("no key: %v, %v", r, err)
	}
	r, err := loadKeyring(key, "", old)
	if err != nil || r.PrimaryID() != want.ID() || !r.Has(oldKey.ID()) {
		t.Errorf("env key: %v", err)
	}

	file := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(file, []byte(key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r, err := loadKeyring("", file, ""); err != nil || r.PrimaryID() != want.ID() {
		t.Errorf("key file: %v", err)
	}
	if _, err := loadKeyring(key, file, ""); err == nil {
		t.Error("key and key file both accepted")
	}
	if _, err := loadKeyring("", filepath.Join(t.TempDir(), "missing"), ""); err == nil {
		t.Error("missing key file accepted")
	}
	empty := filepath.Join(t.TempDir(), "empty")
	_ = os.WriteFile(empty, []byte("\n"), 0o600)
	if _, err := loadKeyring("", empty, ""); err == nil {
		t.Error("empty key file accepted")
	}
	if _, err := loadKeyring("not a key", "", ""); err == nil {
		t.Error("bad key accepted")
	}
}

func TestGenerateKeyCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runGenerateKey(&out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if _, err := fieldcrypt.ParseKey(strings.TrimSpace(out.String())); err != nil {
		t.Errorf("printed key does not parse: %v", err)
	}
}

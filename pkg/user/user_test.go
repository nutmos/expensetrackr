package user

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/validate"
)

func fieldErrs(t *testing.T, err error) map[string]string {
	t.Helper()
	var ve *validate.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want ValidationError, got %v", err)
	}
	return ve.Fields
}

func TestNormalize(t *testing.T) {
	for _, bad := range []string{"no-at", "a@b", "a@@b.com", "a b@c.com", strings.Repeat("a", 250) + "@x.com"} {
		if _, msg := NormalizeEmail(bad); msg == "" {
			t.Errorf("email %q accepted", bad)
		}
	}
	for _, bad := range []string{"ab", "-abc", "has space", "ünï", strings.Repeat("a", 33)} {
		if _, msg := NormalizeUsername(bad); msg == "" {
			t.Errorf("username %q accepted", bad)
		}
	}
}

func TestUserJSONNeverHasHash(t *testing.T) {
	h := "$argon2id$secret"
	b, _ := json.Marshal(User{UID: "u", DisplayName: "x", PasswordHash: &h, HasPassword: true})
	if strings.Contains(string(b), "argon2id") || strings.Contains(string(b), "password_hash") || !strings.Contains(string(b), `"has_password":true`) {
		t.Errorf("json: %s", b)
	}
}

func TestIdentityValidate(t *testing.T) {
	if err := (Identity{Provider: ProviderApple, ProviderSubject: "001234.abcd"}).Validate(); err != nil {
		t.Error(err)
	}
	f := fieldErrs(t, Identity{Provider: "facebook", ProviderSubject: " "}.Validate())
	if f["provider"] == "" || f["provider_subject"] == "" {
		t.Errorf("identity errors: %v", f)
	}
}

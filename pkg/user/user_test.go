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

func sp(s string) *string { return &s }

func TestNormalizePreferences(t *testing.T) {
	ok := map[string]string{
		``:                          `{}`,
		`null`:                      `{}`,
		`{}`:                        `{}`,
		` { "a" : [1, 2], "b":{} }`: `{"a":[1,2],"b":{}}`,
	}
	for in, want := range ok {
		got, msg := NormalizePreferences(json.RawMessage(in))
		if msg != "" || string(got) != want {
			t.Errorf("%q: got %q %q, want %q", in, got, msg, want)
		}
	}
	for in, want := range map[string]string{
		`[]`:         "must be a JSON object",
		`"x"`:        "must be a JSON object",
		`42`:         "must be a JSON object",
		`true`:       "must be a JSON object",
		`{"a":}`:     "must be valid JSON",
		`{"a":1} {}`: "must be valid JSON",
		`{`:          "must be valid JSON",
	} {
		if _, msg := NormalizePreferences(json.RawMessage(in)); msg != want {
			t.Errorf("%q: msg %q, want %q", in, msg, want)
		}
	}
	big := `{"k":"` + strings.Repeat("x", MaxPreferencesSize) + `"}`
	if _, msg := NormalizePreferences(json.RawMessage(big)); !strings.Contains(msg, "at most") {
		t.Errorf("oversized: %q", msg)
	}
}

func TestValidate(t *testing.T) {
	u, err := Input{DisplayName: "  Nat  ", Username: sp(" Nat.E "), Email: sp(" Nat@Example.COM ")}.Validate()
	if err != nil || u.DisplayName != "Nat" || *u.Username != "nat.e" || *u.Email != "nat@example.com" ||
		string(u.Preferences) != "{}" || u.Status != StatusActive {
		t.Fatalf("normalize: %+v %v", u, err)
	}
	// "" username/email mean none.
	if u, err := (Input{DisplayName: "x", Username: sp(""), Email: sp(" ")}).Validate(); err != nil || u.Username != nil || u.Email != nil {
		t.Errorf("empty identifiers: %+v %v", u, err)
	}
	f := fieldErrs(t, func() error {
		_, err := Input{Username: sp("a"), Email: sp("Nat <nat@example.com>"), Preferences: json.RawMessage(`[1]`)}.Validate()
		return err
	}())
	for _, k := range []string{"display_name", "username", "email", "preferences"} {
		if f[k] == "" {
			t.Errorf("want error on %s: %v", k, f)
		}
	}
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

func patchOf(t *testing.T, s string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestApplyPatch(t *testing.T) {
	stored, _ := Input{DisplayName: "Nat", Username: sp("nat"), Email: sp("nat@example.com"),
		Preferences: json.RawMessage(`{"theme":"dark","currency":"THB"}`)}.Validate()

	in := stored.Input()
	if err := in.ApplyPatch(patchOf(t, `{"preferences":{"theme":"light"},"uid":"x","status":"disabled","has_password":true}`)); err != nil {
		t.Fatal(err)
	}
	u, err := in.Validate()
	// Whole-object replace: "currency" is gone. Read-only fields are ignored.
	if err != nil || string(u.Preferences) != `{"theme":"light"}` || *u.Username != "nat" || u.Status != StatusActive {
		t.Errorf("replace prefs: %+v %v", u, err)
	}
	in = stored.Input()
	_ = in.ApplyPatch(patchOf(t, `{"preferences":null,"email":null,"username":""}`))
	if u, err := in.Validate(); err != nil || string(u.Preferences) != "{}" || u.Email != nil || u.Username != nil {
		t.Errorf("null/empty clears: %+v %v", u, err)
	}
	in = stored.Input()
	_ = in.ApplyPatch(patchOf(t, `{"preferences":"dark"}`))
	if _, err := in.Validate(); fieldErrs(t, err)["preferences"] == "" {
		t.Error("string preferences should fail")
	}
	var re *validate.RequestError
	for _, bad := range []string{`{}`, `{"uid":"x"}`, `{"password":"secret"}`, `{"password_hash":"x"}`, `{"email":5}`, `{"display_name":1}`} {
		in = stored.Input()
		if err := in.ApplyPatch(patchOf(t, bad)); !errors.As(err, &re) {
			t.Errorf("%s: want RequestError, got %v", bad, err)
		}
	}
	in = stored.Input()
	if err := in.ApplyPatch(patchOf(t, `{"display_name":null}`)); fieldErrs(t, err)["display_name"] == "" {
		t.Error("null display_name")
	}
}

func TestUserJSONNeverHasHash(t *testing.T) {
	h := "$argon2id$secret"
	b, _ := json.Marshal(User{UID: "u", DisplayName: "x", Preferences: json.RawMessage(`{}`), PasswordHash: &h, HasPassword: true})
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

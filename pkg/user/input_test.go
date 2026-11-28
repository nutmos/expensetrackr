package user

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/validate"
)

func sp(s string) *string { return &s }

func TestValidate(t *testing.T) {
	u, err := Input{DisplayName: "  Nat  ", Username: sp(" Nat.E "), Email: sp(" Nat@Example.COM ")}.Validate()
	if err != nil || u.DisplayName != "Nat" || *u.Username != "nat.e" || *u.Email != "nat@example.com" ||
		u.Preferences != (Preferences{}) || u.Status != StatusActive {
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
		Preferences: json.RawMessage(`{"base_currency":"THB"}`)}.Validate()

	in := stored.Input()
	if err := in.ApplyPatch(patchOf(t, `{"preferences":{"base_currency":"EUR"},"uid":"x","status":"disabled","has_password":true}`)); err != nil {
		t.Fatal(err)
	}
	u, err := in.Validate()
	// Whole-object replace: "currency" is gone. Read-only fields are ignored.
	if err != nil || u.Preferences.BaseCurrency != "EUR" || *u.Username != "nat" || u.Status != StatusActive {
		t.Errorf("replace prefs: %+v %v", u, err)
	}
	in = stored.Input()
	_ = in.ApplyPatch(patchOf(t, `{"preferences":null,"email":null,"username":""}`))
	if u, err := in.Validate(); err != nil || u.Preferences != (Preferences{}) || u.Email != nil || u.Username != nil {
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

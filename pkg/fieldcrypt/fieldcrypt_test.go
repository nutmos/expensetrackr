package fieldcrypt

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func mustKey(t *testing.T, b byte) *Key {
	t.Helper()
	raw := make([]byte, KeySize)
	for i := range raw {
		raw[i] = b
	}
	k, err := NewKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSealOpenRoundTrip(t *testing.T) {
	r := NewKeyring(mustKey(t, 1))
	for _, plain := range []string{"x", "Lunch with Ann", "ข้าวมันไก่ 🍗", strings.Repeat("a", 5000)} {
		sealed, err := r.Seal(plain)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(sealed, "enc1:"+r.PrimaryID()+":") || (len(plain) > 3 && strings.Contains(sealed, plain)) {
			t.Errorf("sealed %q = %q", plain, sealed)
		}
		if KeyID(sealed) != r.PrimaryID() {
			t.Errorf("KeyID = %q, want %q", KeyID(sealed), r.PrimaryID())
		}
		got, err := r.Open(sealed)
		if err != nil || got != plain {
			t.Errorf("Open = %q, %v; want %q", got, err, plain)
		}
	}
}

func TestSealIsRandomized(t *testing.T) {
	r := NewKeyring(mustKey(t, 1))
	a, _ := r.Seal("same")
	b, _ := r.Seal("same")
	if a == b {
		t.Errorf("two seals of the same value are equal: %q", a)
	}
}

func TestEmptyAndNilKeyring(t *testing.T) {
	r := NewKeyring(mustKey(t, 1))
	if s, err := r.Seal(""); s != "" || err != nil {
		t.Errorf("Seal(\"\") = %q, %v", s, err)
	}
	if s, err := r.Open(""); s != "" || err != nil {
		t.Errorf("Open(\"\") = %q, %v", s, err)
	}
	var off *Keyring
	if off.Enabled() || off.PrimaryID() != "" || off.Has("00000000") {
		t.Error("nil keyring reports a key")
	}
	if s, err := off.Seal("plain"); s != "plain" || err != nil {
		t.Errorf("nil Seal = %q, %v", s, err)
	}
	if s, err := off.Open("plain"); s != "plain" || err != nil {
		t.Errorf("nil Open = %q, %v", s, err)
	}
}

func TestOpenErrors(t *testing.T) {
	r := NewKeyring(mustKey(t, 1))
	other := NewKeyring(mustKey(t, 2))
	sealed, _ := r.Seal("secret")

	if _, err := r.Open("plain text"); !errors.Is(err, ErrNotSealed) {
		t.Errorf("plain: %v", err)
	}
	if _, err := other.Open(sealed); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("other key: %v", err)
	}
	// Flip one byte of the ciphertext: authentication fails.
	id, body, _ := split(sealed)
	raw, _ := base64.RawURLEncoding.DecodeString(body)
	raw[len(raw)-1] ^= 1
	if _, err := r.Open("enc1:" + id + ":" + base64.RawURLEncoding.EncodeToString(raw)); !errors.Is(err, ErrCorrupt) {
		t.Errorf("tampered: %v", err)
	}
	if _, err := r.Open("enc1:" + id + ":!!"); !errors.Is(err, ErrCorrupt) {
		t.Errorf("bad base64: %v", err)
	}
	// Relabelling a value with another key's id fails even if that key is
	// in the ring: the id is authenticated.
	both := NewKeyring(mustKey(t, 2), mustKey(t, 1))
	if _, err := both.Open("enc1:" + both.PrimaryID() + ":" + body); !errors.Is(err, ErrCorrupt) {
		t.Errorf("relabelled: %v", err)
	}
}

func TestRotationOpensOldKeys(t *testing.T) {
	oldKey, newKey := mustKey(t, 1), mustKey(t, 2)
	sealedOld, _ := NewKeyring(oldKey).Seal("from before")
	r := NewKeyring(newKey, oldKey)
	if got, err := r.Open(sealedOld); err != nil || got != "from before" {
		t.Errorf("open old = %q, %v", got, err)
	}
	sealedNew, _ := r.Seal("now")
	if KeyID(sealedNew) != newKey.ID() {
		t.Errorf("sealed with %s, want the primary %s", KeyID(sealedNew), newKey.ID())
	}
	if !r.Has(oldKey.ID()) || !r.Has(newKey.ID()) {
		t.Error("keyring is missing a key")
	}
}

func TestParseKey(t *testing.T) {
	raw := make([]byte, KeySize)
	for i := range raw {
		raw[i] = byte(i * 7)
	}
	want, _ := NewKey(raw)
	for name, s := range map[string]string{
		"hex":        hex.EncodeToString(raw),
		"std":        base64.StdEncoding.EncodeToString(raw),
		"raw std":    base64.RawStdEncoding.EncodeToString(raw),
		"url":        base64.URLEncoding.EncodeToString(raw),
		"spaces":     "  " + base64.StdEncoding.EncodeToString(raw) + "\n",
		"raw url":    base64.RawURLEncoding.EncodeToString(raw),
		"upper hex":  strings.ToUpper(hex.EncodeToString(raw)),
		"std padded": base64.StdEncoding.EncodeToString(raw),
	} {
		k, err := ParseKey(s)
		if err != nil || k.ID() != want.ID() {
			t.Errorf("%s: %v, id %v", name, err, k)
		}
	}
	for name, s := range map[string]string{
		"empty":     "",
		"short":     base64.StdEncoding.EncodeToString(raw[:16]),
		"long":      base64.StdEncoding.EncodeToString(append(raw, 1)),
		"not a key": "correct horse battery staple",
	} {
		if _, err := ParseKey(s); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestGenerateKey(t *testing.T) {
	a, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := GenerateKey()
	if a == b {
		t.Error("two generated keys are equal")
	}
	if _, err := ParseKey(a); err != nil {
		t.Errorf("generated key does not parse: %v", err)
	}
}

func TestParseKeyring(t *testing.T) {
	k1, _ := GenerateKey()
	k2, _ := GenerateKey()
	k3, _ := GenerateKey()
	if r, err := ParseKeyring("", ""); r != nil || err != nil {
		t.Errorf("empty: %v, %v", r, err)
	}
	if _, err := ParseKeyring("", k2); err == nil {
		t.Error("old keys without a primary accepted")
	}
	if _, err := ParseKeyring(k1, k2+",nope"); err == nil {
		t.Error("bad old key accepted")
	}
	r, err := ParseKeyring(k1, " "+k2+" , ,"+k3)
	if err != nil {
		t.Fatal(err)
	}
	p1, _ := ParseKey(k1)
	p2, _ := ParseKey(k2)
	p3, _ := ParseKey(k3)
	if r.PrimaryID() != p1.ID() || !r.Has(p2.ID()) || !r.Has(p3.ID()) {
		t.Errorf("keyring ids: primary %s", r.PrimaryID())
	}
}

func TestKeyIDOfMalformed(t *testing.T) {
	for _, s := range []string{"", "plain", "enc1:", "enc1:zzzzzzzz:abc", "enc1:1234:abc", "enc1:12345678:", "enc2:12345678:abc"} {
		if id := KeyID(s); id != "" {
			t.Errorf("KeyID(%q) = %q", s, id)
		}
	}
}

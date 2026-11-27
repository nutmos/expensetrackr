// Package fieldcrypt encrypts individual database values (issue #192): the
// free-text personal data the store keeps (transaction notes, account and
// balance names, balance descriptions, categories, display names), so a copy
// of the database, a backup or a look at it with psql does not reveal them.
//
// Values are sealed with AES-256-GCM under a 32-byte key and stored as text:
//
//	enc1:<key id>:<base64url(nonce || ciphertext || tag)>
//
// The key id is the first 4 bytes of SHA-256(key) in hex. It tells which key
// sealed a value, so a key can be rotated: the new key seals, older keys
// still open (see Keyring). Every value gets a fresh random nonce, so equal
// values do not produce equal ciphertexts.
//
// The empty string is never sealed: it stays "" (an empty note reveals only
// that it is empty), which keeps the columns' ” defaults and checks valid.
package fieldcrypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// KeySize is the key length in bytes (AES-256).
const KeySize = 32

// prefix starts every sealed value; the version lets the format change later.
const prefix = "enc1:"

var (
	// ErrNotSealed: the value is not in the sealed format.
	ErrNotSealed = errors.New("fieldcrypt: value is not encrypted")
	// ErrUnknownKey: the value was sealed with a key the keyring does not have.
	ErrUnknownKey = errors.New("fieldcrypt: value was encrypted with an unknown key")
	// ErrCorrupt: the value is in the sealed format but does not decrypt
	// (damaged, or not made by this package).
	ErrCorrupt = errors.New("fieldcrypt: encrypted value is damaged or was not made with this key")
)

// Key is one AES-256 key with its id.
type Key struct {
	id   string
	aead cipher.AEAD
}

// NewKey makes a Key from 32 raw bytes.
func NewKey(raw []byte) (*Key, error) {
	if len(raw) != KeySize {
		return nil, fmt.Errorf("fieldcrypt: key must be %d bytes, got %d", KeySize, len(raw))
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	return &Key{id: hex.EncodeToString(sum[:4]), aead: aead}, nil
}

// ParseKey decodes a key written as 64 hex digits or as standard / URL-safe
// base64 (padded or not) of 32 bytes, as GenerateKey prints it.
func ParseKey(s string) (*Key, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("fieldcrypt: empty key")
	}
	if len(s) == 2*KeySize {
		if raw, err := hex.DecodeString(s); err == nil {
			return NewKey(raw)
		}
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if raw, err := enc.DecodeString(s); err == nil {
			if len(raw) != KeySize {
				return nil, fmt.Errorf("fieldcrypt: key must decode to %d bytes, got %d", KeySize, len(raw))
			}
			return NewKey(raw)
		}
	}
	return nil, fmt.Errorf("fieldcrypt: key must be %d bytes as base64 or %d hex digits", KeySize, 2*KeySize)
}

// GenerateKey returns a new random key, base64-encoded (for
// EXPENSE_ENCRYPTION_KEY).
func GenerateKey() (string, error) {
	raw := make([]byte, KeySize)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// ID is the key's id as it appears in sealed values (8 hex digits). It is
// derived from the key but reveals nothing usable about it.
func (k *Key) ID() string { return k.id }

// Keyring seals with its primary key and opens values sealed by any of its
// keys. A nil *Keyring is valid and does nothing: Seal and Open return the
// value unchanged (encryption off).
type Keyring struct {
	primary *Key
	byID    map[string]*Key
}

// NewKeyring returns a keyring that seals with primary and also opens values
// sealed with any of old (earlier keys, kept for rotation).
func NewKeyring(primary *Key, old ...*Key) *Keyring {
	r := &Keyring{primary: primary, byID: map[string]*Key{primary.id: primary}}
	for _, k := range old {
		if k != nil {
			if _, dup := r.byID[k.id]; !dup {
				r.byID[k.id] = k
			}
		}
	}
	return r
}

// Enabled reports whether r encrypts (r is not nil).
func (r *Keyring) Enabled() bool { return r != nil }

// PrimaryID is the id of the key r seals with ("" for a nil keyring).
func (r *Keyring) PrimaryID() string {
	if r == nil {
		return ""
	}
	return r.primary.id
}

// Has reports whether r can open values sealed with key id.
func (r *Keyring) Has(id string) bool {
	if r == nil {
		return false
	}
	_, ok := r.byID[id]
	return ok
}

// Seal encrypts s with the primary key. "" stays "", and a nil keyring
// returns s unchanged.
func (r *Keyring) Seal(s string) (string, error) {
	if r == nil || s == "" {
		return s, nil
	}
	k := r.primary
	nonce := make([]byte, k.aead.NonceSize(), k.aead.NonceSize()+len(s)+k.aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("fieldcrypt: nonce: %w", err)
	}
	// The key id is authenticated too, so a value can't be relabelled.
	sealed := k.aead.Seal(nonce, nonce, []byte(s), []byte(k.id))
	return prefix + k.id + ":" + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Open decrypts a value made by Seal. "" stays "", and a nil keyring returns
// s unchanged. A non-empty value that is not sealed is ErrNotSealed; one
// sealed with a key r lacks is ErrUnknownKey; one that fails to decrypt is
// ErrCorrupt.
func (r *Keyring) Open(s string) (string, error) {
	if r == nil || s == "" {
		return s, nil
	}
	id, body, ok := split(s)
	if !ok {
		return "", ErrNotSealed
	}
	k, ok := r.byID[id]
	if !ok {
		return "", fmt.Errorf("%w (key id %s)", ErrUnknownKey, id)
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil || len(raw) < k.aead.NonceSize()+k.aead.Overhead() {
		return "", ErrCorrupt
	}
	n := k.aead.NonceSize()
	plain, err := k.aead.Open(nil, raw[:n], raw[n:], []byte(id))
	if err != nil {
		return "", ErrCorrupt
	}
	return string(plain), nil
}

// KeyID returns the id of the key that sealed s, or "" when s is not in the
// sealed format.
func KeyID(s string) string {
	id, _, ok := split(s)
	if !ok {
		return ""
	}
	return id
}

// split parses "enc1:<id>:<body>".
func split(s string) (id, body string, ok bool) {
	rest, found := strings.CutPrefix(s, prefix)
	if !found {
		return "", "", false
	}
	id, body, found = strings.Cut(rest, ":")
	if !found || len(id) != 8 || body == "" {
		return "", "", false
	}
	if _, err := hex.DecodeString(id); err != nil {
		return "", "", false
	}
	return id, body, true
}

// ParseKeyring builds a keyring from a primary key and a comma-separated
// list of older keys (either may be written as ParseKey accepts). An empty
// primary returns nil (encryption off), which is an error if old keys are
// given.
func ParseKeyring(primary, old string) (*Keyring, error) {
	primary = strings.TrimSpace(primary)
	var olds []*Key
	for i, s := range strings.Split(old, ",") {
		if strings.TrimSpace(s) == "" {
			continue
		}
		k, err := ParseKey(s)
		if err != nil {
			return nil, fmt.Errorf("old key %d: %w", i+1, err)
		}
		olds = append(olds, k)
	}
	if primary == "" {
		if len(olds) > 0 {
			return nil, errors.New("fieldcrypt: old keys given without a primary key")
		}
		return nil, nil
	}
	k, err := ParseKey(primary)
	if err != nil {
		return nil, err
	}
	return NewKeyring(k, olds...), nil
}

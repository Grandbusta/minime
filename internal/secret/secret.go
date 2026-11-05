// Package secret seals a person's secrets before they reach their database,
// with a key kept outside it (MINIME_KEY): a copy of the database alone
// holds nothing that works.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// EnvKey is where the key is: 32 bytes as 64 hex characters.
const EnvKey = "MINIME_KEY"

type Key struct{ aead cipher.AEAD }

var ErrWrongKey = errors.New("MINIME_KEY doesn't open this person's secrets: it isn't the key they were sealed with")

func NewKey() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func ParseKey(s string) (Key, error) {
	raw, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil || len(raw) != 32 {
		return Key{}, fmt.Errorf("%s must be 64 hex characters (32 bytes), like `openssl rand -hex 32` makes", EnvKey)
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return Key{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return Key{}, err
	}
	return Key{aead}, nil
}

// Seal binds value to name too, so a sealed value can't be moved to another name.
func (k Key) Seal(name, value string) []byte {
	nonce := make([]byte, k.aead.NonceSize())
	rand.Read(nonce)
	return k.aead.Seal(nonce, nonce, []byte(value), []byte(name))
}

func (k Key) Open(name string, sealed []byte) (string, error) {
	n := k.aead.NonceSize()
	if len(sealed) < n {
		return "", ErrWrongKey
	}
	plain, err := k.aead.Open(nil, sealed[:n], sealed[n:], []byte(name))
	if err != nil {
		return "", ErrWrongKey
	}
	return string(plain), nil
}

// Mask shows enough of a value to tell which it is, never enough to use it.
func Mask(v string) string {
	if len(v) < 12 {
		return "set"
	}
	return v[:3] + "…" + v[len(v)-4:]
}

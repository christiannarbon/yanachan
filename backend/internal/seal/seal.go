// Package seal encrypts small secrets at rest with AES-256-GCM.
//
// The first key in a Keyring seals; every key opens. Prepending a new key and
// later dropping the old one rotates without signing everyone out.
package seal

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// ErrOpen is the only error Open returns, whatever went wrong, so that no
// error message can carry any part of the input.
var ErrOpen = errors.New("seal: cannot open value")

const prefix = "v1."

type Keyring struct {
	aeads []cipher.AEAD
}

// NewKeyring needs at least one key, and every key must be 32 bytes.
func NewKeyring(keys [][]byte) (*Keyring, error) {
	if len(keys) == 0 {
		return nil, errors.New("seal: no keys")
	}
	k := &Keyring{}
	for i, key := range keys {
		if len(key) != 32 {
			return nil, fmt.Errorf("seal: key %d is %d bytes, want 32", i+1, len(key))
		}
		block, err := aes.NewCipher(bytes.Clone(key))
		if err != nil {
			return nil, err
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		k.aeads = append(k.aeads, aead)
	}
	return k, nil
}

// Seal encrypts plain with the first key, binding it to aad.
func (k *Keyring) Seal(plain []byte, aad string) (string, error) {
	aead := k.aeads[0]
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := aead.Seal(nil, nonce, plain, []byte(aad))
	return prefix + base64.RawURLEncoding.EncodeToString(append(nonce, ct...)), nil
}

// Open reverses Seal with any key in the ring.
func (k *Keyring) Open(sealed, aad string) ([]byte, error) {
	rest, ok := strings.CutPrefix(sealed, prefix)
	if !ok {
		return nil, ErrOpen
	}
	raw, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil {
		return nil, ErrOpen
	}
	for _, aead := range k.aeads {
		n := aead.NonceSize()
		if len(raw) < n+aead.Overhead() {
			return nil, ErrOpen
		}
		if plain, err := aead.Open(nil, raw[:n], raw[n:], []byte(aad)); err == nil {
			return plain, nil
		}
	}
	return nil, ErrOpen
}

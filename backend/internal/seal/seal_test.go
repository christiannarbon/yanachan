package seal

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func newKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

func newRing(t *testing.T, keys ...[]byte) *Keyring {
	t.Helper()
	k, err := NewKeyring(keys)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func mustSeal(t *testing.T, k *Keyring, plain []byte, aad string) string {
	t.Helper()
	s, err := k.Seal(plain, aad)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRoundTrip(t *testing.T) {
	k := newRing(t, newKey(t))
	for _, plain := range [][]byte{[]byte("gho_token"), {}} {
		s := mustSeal(t, k, plain, "session")
		if !strings.HasPrefix(s, "v1.") {
			t.Errorf("sealed = %q, want a v1. prefix", s)
		}
		got, err := k.Open(s, "session")
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, plain) {
			t.Errorf("opened %q, want %q", got, plain)
		}
	}
}

func TestSealIsRandomised(t *testing.T) {
	k := newRing(t, newKey(t))
	a := mustSeal(t, k, []byte("gho_token"), "session")
	b := mustSeal(t, k, []byte("gho_token"), "session")
	if a == b {
		t.Error("sealing the same input twice gave the same string")
	}
}

func TestWrongAADFails(t *testing.T) {
	k := newRing(t, newKey(t))
	s := mustSeal(t, k, []byte("gho_token"), "session")
	if _, err := k.Open(s, "other"); !errors.Is(err, ErrOpen) {
		t.Errorf("err = %v, want ErrOpen", err)
	}
}

func TestRotation(t *testing.T) {
	k1, k2 := newKey(t), newKey(t)
	old := newRing(t, k1)
	both := newRing(t, k2, k1)
	onlyNew := newRing(t, k2)

	s := mustSeal(t, old, []byte("gho_token"), "session")
	if _, err := both.Open(s, "session"); err != nil {
		t.Errorf("rotated ring cannot open an old value: %v", err)
	}
	if _, err := onlyNew.Open(s, "session"); !errors.Is(err, ErrOpen) {
		t.Errorf("err = %v, want ErrOpen once the old key is gone", err)
	}

	s = mustSeal(t, both, []byte("gho_token"), "session")
	if _, err := onlyNew.Open(s, "session"); err != nil {
		t.Errorf("rotated ring did not seal with the new key: %v", err)
	}
}

func TestTamperedFails(t *testing.T) {
	k := newRing(t, newKey(t))
	s := mustSeal(t, k, []byte("gho_token"), "session")
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, "v1."))
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)/2] ^= 0x01
	tampered := "v1." + base64.RawURLEncoding.EncodeToString(raw)
	if _, err := k.Open(tampered, "session"); !errors.Is(err, ErrOpen) {
		t.Errorf("err = %v, want ErrOpen", err)
	}
}

func TestMalformedInputs(t *testing.T) {
	k := newRing(t, newKey(t))
	short := "v1." + base64.RawURLEncoding.EncodeToString(make([]byte, 20))
	for _, in := range []string{"", "v1.", "v2.abcd", "v1.!!!", short} {
		_, err := k.Open(in, "session")
		if !errors.Is(err, ErrOpen) {
			t.Errorf("Open(%q) err = %v, want ErrOpen", in, err)
			continue
		}
		if err.Error() != ErrOpen.Error() {
			t.Errorf("Open(%q) err = %q, want exactly %q", in, err, ErrOpen)
		}
	}
}

func TestNewKeyringValidates(t *testing.T) {
	cases := map[string][][]byte{
		"no keys":  nil,
		"16 bytes": {bytes.Repeat([]byte{0xab}, 16)},
		"33 bytes": {bytes.Repeat([]byte{0xab}, 33)},
	}
	for name, keys := range cases {
		_, err := NewKeyring(keys)
		if err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		for _, key := range keys {
			for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawURLEncoding} {
				if strings.Contains(err.Error(), enc.EncodeToString(key)) {
					t.Errorf("%s: error %q echoes the key", name, err)
				}
			}
		}
	}
}

func TestKeysAreCopied(t *testing.T) {
	key := newKey(t)
	k := newRing(t, key)
	s := mustSeal(t, k, []byte("gho_token"), "session")
	for i := range key {
		key[i] = 0
	}
	if _, err := k.Open(s, "session"); err != nil {
		t.Errorf("mutating the caller's key broke the ring: %v", err)
	}
}

package auth

// VerifyPassword reads hashes it did not write: a database the Python
// original wrote on a machine whose OpenSSL had no scrypt holds pbkdf2 ones,
// and those must still sign in. Anything it cannot parse is a refusal, never
// a match.

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"golang.org/x/crypto/pbkdf2"
)

func TestVerifyPasswordAcceptsAPythonEraPBKDF2Hash(t *testing.T) {
	salt := []byte("0123456789abcdef")
	// Computed here from the library directly, not through derive, so a
	// derive that drifted from the original's parameters goes red.
	k := pbkdf2.Key([]byte("correct horse battery"), salt, 480_000, 32, sha256.New)
	stored := "pbkdf2$" + hex.EncodeToString(salt) + "$" + hex.EncodeToString(k)

	if !VerifyPassword("correct horse battery", stored) {
		t.Error("a pbkdf2 hash written by the original no longer verifies")
	}
	if VerifyPassword("wrong horse battery", stored) {
		t.Error("a wrong password matched a pbkdf2 hash")
	}
}

func TestVerifyPasswordRefusesWhatItCannotRead(t *testing.T) {
	good, err := HashPassword("a-long-enough-password")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword("a-long-enough-password", good) {
		t.Fatal("a fresh scrypt hash does not verify")
	}
	for name, stored := range map[string]string{
		"two parts":         "scrypt$abcd",
		"four parts":        good + "$extra",
		"salt not hex":      "scrypt$zz$" + hex.EncodeToString([]byte("x")),
		"unknown algorithm": "md5$" + hex.EncodeToString([]byte("salt")) + "$00",
		"empty":             "",
	} {
		if VerifyPassword("a-long-enough-password", stored) {
			t.Errorf("%s: %q verified", name, stored)
		}
	}
}

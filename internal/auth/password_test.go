package auth

import (
	"strings"
	"testing"
)

func TestPasswordHashRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "argon2id$v=19$") {
		t.Fatalf("hash prefix=%q", hash)
	}
	if !VerifyPassword("correct horse battery staple", hash) {
		t.Fatal("correct password did not verify")
	}
	if VerifyPassword("wrong password", hash) {
		t.Fatal("wrong password verified")
	}
	if strings.Contains(hash, "correct horse battery staple") {
		t.Fatal("hash contains plaintext password")
	}
}

func TestVerifyPasswordFailsClosed(t *testing.T) {
	for _, encoded := range []string{"", "sha256:abc", "argon2id$v=19$bad", "argon2id$v=19$m=x,t=3,p=1$salt$hash"} {
		if VerifyPassword("anything", encoded) {
			t.Fatalf("VerifyPassword accepted malformed hash %q", encoded)
		}
	}
}

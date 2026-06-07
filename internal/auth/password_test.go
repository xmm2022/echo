package auth

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"
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

func TestVerifyPasswordRejectsNonSpecArgon2Params(t *testing.T) {
	password := "correct horse battery staple"
	salt := []byte("0123456789abcdef")

	tests := []struct {
		name    string
		memory  uint32
		time    uint32
		threads uint8
	}{
		{name: "memory", memory: 1024, time: passwordTime, threads: passwordThreads},
		{name: "time", memory: passwordMemoryKiB, time: passwordTime - 1, threads: passwordThreads},
		{name: "threads", memory: passwordMemoryKiB, time: passwordTime, threads: passwordThreads + 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := passwordHashForTest(t, password, tt.memory, tt.time, tt.threads, salt)
			if VerifyPassword(password, encoded) {
				t.Fatalf("VerifyPassword accepted non-spec params %q", encoded)
			}
		})
	}
}

func TestVerifyPasswordRejectsWrongSaltOrHashLength(t *testing.T) {
	tests := []struct {
		name string
		salt []byte
		hash []byte
	}{
		{name: "short salt", salt: []byte("short salt byte"), hash: make([]byte, passwordKeyBytes)},
		{name: "short hash", salt: []byte("0123456789abcdef"), hash: make([]byte, passwordKeyBytes-1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := encodedPasswordHash(tt.salt, tt.hash, passwordMemoryKiB, passwordTime, passwordThreads)
			if VerifyPassword("anything", encoded) {
				t.Fatalf("VerifyPassword accepted wrong length salt/hash %q", encoded)
			}
		})
	}
}

func passwordHashForTest(t *testing.T, password string, memory, time uint32, threads uint8, salt []byte) string {
	t.Helper()
	hash := argon2.IDKey([]byte(password), salt, time, memory, threads, passwordKeyBytes)
	return encodedPasswordHash(salt, hash, memory, time, threads)
}

func encodedPasswordHash(salt, hash []byte, memory, time uint32, threads uint8) string {
	return fmt.Sprintf(
		"argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		memory,
		time,
		threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	)
}

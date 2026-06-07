package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	passwordSaltBytes = 16
	passwordKeyBytes  = 32
	passwordMemoryKiB = 64 * 1024
	passwordTime      = 3
	passwordThreads   = 1
)

// HashPassword returns an Argon2id hash suitable for storage.
func HashPassword(password string) (string, error) {
	salt := make([]byte, passwordSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}

	hash := argon2.IDKey([]byte(password), salt, passwordTime, passwordMemoryKiB, passwordThreads, passwordKeyBytes)
	return fmt.Sprintf(
		"argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		passwordMemoryKiB,
		passwordTime,
		passwordThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

// VerifyPassword checks a plaintext password against a stored Argon2id hash.
func VerifyPassword(password, encoded string) bool {
	params, salt, expected, ok := parsePasswordHash(encoded)
	if !ok {
		return false
	}

	got := argon2.IDKey([]byte(password), salt, params.time, params.memoryKiB, params.threads, passwordKeyBytes)
	return subtle.ConstantTimeCompare(got, expected) == 1
}

type passwordParams struct {
	memoryKiB uint32
	time      uint32
	threads   uint8
}

func parsePasswordHash(encoded string) (passwordParams, []byte, []byte, bool) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 || parts[0] != "argon2id" || parts[1] != "v=19" {
		return passwordParams{}, nil, nil, false
	}

	params, ok := parsePasswordParams(parts[2])
	if !ok {
		return passwordParams{}, nil, nil, false
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(salt) != passwordSaltBytes {
		return passwordParams{}, nil, nil, false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(expected) != passwordKeyBytes {
		return passwordParams{}, nil, nil, false
	}
	return params, salt, expected, true
}

func parsePasswordParams(raw string) (passwordParams, bool) {
	parts := strings.Split(raw, ",")
	if len(parts) != 3 {
		return passwordParams{}, false
	}
	memory, ok := parseUint32Param(parts[0], "m=")
	if !ok {
		return passwordParams{}, false
	}
	timeCost, ok := parseUint32Param(parts[1], "t=")
	if !ok {
		return passwordParams{}, false
	}
	threads, ok := parseUint8Param(parts[2], "p=")
	if !ok {
		return passwordParams{}, false
	}
	return passwordParams{memoryKiB: memory, time: timeCost, threads: threads}, true
}

func parseUint32Param(raw, prefix string) (uint32, bool) {
	if !strings.HasPrefix(raw, prefix) {
		return 0, false
	}
	value, err := strconv.ParseUint(strings.TrimPrefix(raw, prefix), 10, 32)
	if err != nil || value == 0 {
		return 0, false
	}
	return uint32(value), true
}

func parseUint8Param(raw, prefix string) (uint8, bool) {
	if !strings.HasPrefix(raw, prefix) {
		return 0, false
	}
	value, err := strconv.ParseUint(strings.TrimPrefix(raw, prefix), 10, 8)
	if err != nil || value == 0 {
		return 0, false
	}
	return uint8(value), true
}

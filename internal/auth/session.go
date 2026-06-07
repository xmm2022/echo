package auth

import (
	"encoding/base64"
	"strings"
)

const (
	CredentialBearer  = "bearer"
	CredentialSession = "session"

	sessionSelectorBytes      = 12
	sessionSelectorEncodedLen = 16
	sessionSecretBytes        = 32
	sessionSecretEncodedLen   = 43
)

func GenerateSessionToken() (token, selector, secret string, err error) {
	selector, err = randomBase64URL(12)
	if err != nil {
		return "", "", "", err
	}
	secret, err = randomBase64URL(32)
	if err != nil {
		return "", "", "", err
	}
	return selector + "." + secret, selector, secret, nil
}

func GenerateCSRFToken() (string, error) {
	return randomBase64URL(32)
}

func ParseSessionCookie(raw string) (selector, secret string, ok bool) {
	selector, secret, ok = strings.Cut(raw, ".")
	if !ok || selector == "" || secret == "" || strings.Contains(secret, ".") {
		return "", "", false
	}
	if !validSessionPart(selector, sessionSelectorEncodedLen, sessionSelectorBytes) ||
		!validSessionPart(secret, sessionSecretEncodedLen, sessionSecretBytes) {
		return "", "", false
	}
	return selector, secret, true
}

func VerifySessionSecret(secret, storedHash string) bool {
	return VerifyTokenHash(secret, storedHash)
}

func validSessionPart(part string, encodedLen, decodedLen int) bool {
	if len(part) != encodedLen {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(part)
	return err == nil && len(decoded) == decodedLen
}

func ScopesForRole(role string) ([]string, bool) {
	switch role {
	case "admin":
		return []string{"admin"}, true
	case "user":
		return []string{"discovery", "read", "playback"}, true
	default:
		return nil, false
	}
}

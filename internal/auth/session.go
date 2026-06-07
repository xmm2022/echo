package auth

import "strings"

const (
	CredentialBearer  = "bearer"
	CredentialSession = "session"
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
	selector, secret, ok = strings.Cut(strings.TrimSpace(raw), ".")
	if !ok || selector == "" || secret == "" || strings.Contains(secret, ".") {
		return "", "", false
	}
	return selector, secret, true
}

func VerifySessionSecret(secret, storedHash string) bool {
	return VerifyTokenHash(secret, storedHash)
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

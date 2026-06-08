package auth

import (
	"strings"
	"testing"
)

func TestGenerateSessionTokenShapeAndVerification(t *testing.T) {
	token, selector, secret, err := GenerateSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || selector == "" || secret == "" {
		t.Fatalf("empty token parts token=%q selector=%q secret=%q", token, selector, secret)
	}
	if token != selector+"."+secret {
		t.Fatalf("token=%q, want selector.secret", token)
	}
	parsedSelector, parsedSecret, ok := ParseSessionCookie(token)
	if !ok || parsedSelector != selector || parsedSecret != secret {
		t.Fatalf("ParseSessionCookie=%q %q %v, want generated parts", parsedSelector, parsedSecret, ok)
	}
	if _, _, ok := ParseSessionCookie("broken"); ok {
		t.Fatal("broken cookie parsed")
	}
	hash := HashToken(secret)
	if !VerifySessionSecret(secret, hash) {
		t.Fatal("session secret did not verify")
	}
	if VerifySessionSecret(secret+"x", hash) {
		t.Fatal("modified session secret verified")
	}
	if strings.Contains(hash, secret) {
		t.Fatal("session hash leaked secret")
	}
}

func TestParseSessionCookieRejectsMalformedParts(t *testing.T) {
	_, selector, secret, err := GenerateSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		raw  string
	}{
		{name: "empty selector", raw: "." + secret},
		{name: "empty secret", raw: selector + "."},
		{name: "extra dot", raw: selector + "." + secret + ".extra"},
		{name: "invalid selector base64url character", raw: selector[:len(selector)-1] + "+" + "." + secret},
		{name: "invalid secret base64url character", raw: selector + "." + secret[:len(secret)-1] + "/"},
		{name: "short selector", raw: selector[:len(selector)-1] + "." + secret},
		{name: "long selector", raw: selector + "a." + secret},
		{name: "short secret", raw: selector + "." + secret[:len(secret)-1]},
		{name: "long secret", raw: selector + "." + secret + "a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, ok := ParseSessionCookie(tt.raw); ok {
				t.Fatalf("ParseSessionCookie accepted malformed cookie %q", tt.raw)
			}
		})
	}
}

func TestScopesForRole(t *testing.T) {
	admin, ok := ScopesForRole("admin")
	if !ok || len(admin) != 1 || admin[0] != "admin" {
		t.Fatalf("admin scopes=%#v ok=%v", admin, ok)
	}
	user, ok := ScopesForRole("user")
	if !ok || strings.Join(user, ",") != "discovery,read,playback" {
		t.Fatalf("user scopes=%#v ok=%v", user, ok)
	}
	if _, ok := ScopesForRole("disabled"); ok {
		t.Fatal("unknown role produced scopes")
	}
}

func TestGenerateCSRFToken(t *testing.T) {
	token, err := GenerateCSRFToken()
	if err != nil {
		t.Fatal(err)
	}
	if token == "" {
		t.Fatal("csrf token is empty")
	}
	hash := HashToken(token)
	if !VerifyTokenHash(token, hash) {
		t.Fatal("csrf token hash did not verify")
	}
	if VerifyTokenHash(token+"x", hash) {
		t.Fatal("modified csrf token verified")
	}
}

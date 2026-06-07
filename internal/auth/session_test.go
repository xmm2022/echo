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

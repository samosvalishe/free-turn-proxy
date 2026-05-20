package auth

import "testing"

func TestNopAuthenticatorReturnsAnonymous(t *testing.T) {
	tenant, err := NopAuthenticator{}.Authenticate(t.Context(), nil)
	if err != nil {
		t.Fatalf("NopAuthenticator.Authenticate: %v", err)
	}
	if tenant != Anonymous {
		t.Fatalf("tenant = %q want Anonymous (%q)", tenant, Anonymous)
	}
}

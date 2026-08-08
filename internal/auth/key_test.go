package auth

import (
	"strings"
	"testing"
)

func TestNewKeyManagerUsesConfiguredKey(t *testing.T) {
	m, err := NewKeyManager("configured-key", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Generated() {
		t.Fatal("expected generated to be false")
	}
	if m.APIKey() != "configured-key" {
		t.Fatalf("expected configured key, got %q", m.APIKey())
	}
}

func TestNewKeyManagerGeneratesKey(t *testing.T) {
	m, err := NewKeyManager("", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !m.Generated() {
		t.Fatal("expected generated to be true")
	}
	if !strings.HasPrefix(m.APIKey(), generatedKeyPrefix) {
		t.Fatalf("expected generated key prefix %q, got %q", generatedKeyPrefix, m.APIKey())
	}

	encodedLen := len(strings.TrimPrefix(m.APIKey(), generatedKeyPrefix))
	if encodedLen < 64 {
		t.Fatalf("expected at least 64 hex chars for 256-bit entropy, got %d", encodedLen)
	}
}

func TestIsAuthorized(t *testing.T) {
	m, err := NewKeyManager("secret", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !m.IsAuthorized("Bearer secret") {
		t.Fatal("expected valid bearer token to authorize")
	}
	if m.IsAuthorized("Bearer wrong") {
		t.Fatal("expected wrong token to fail auth")
	}
	if m.IsAuthorized("bad-header") {
		t.Fatal("expected malformed header to fail auth")
	}
	if m.IsAuthorized("") {
		t.Fatal("expected empty header to fail auth")
	}
}

func TestNewKeyManagerFailsWhenGenerationDisabledAndNoConfiguredKey(t *testing.T) {
	_, err := NewKeyManager("", false)
	if err == nil {
		t.Fatal("expected error when generation is disabled and key is missing")
	}
}

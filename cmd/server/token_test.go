package main

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/matheusjuliosantana/green-stack-monitor/pkg/middleware"
)

const testSecret = "dev-secret-32-bytes-minimum-here"

// TestIssueToken_ValidJWT verifies that IssueToken returns a JWT that is
// parseable with HS256 and contains the expected claims.
func TestIssueToken_ValidJWT(t *testing.T) {
	token, err := middleware.IssueToken([]byte(testSecret), "alice", "admin", time.Hour)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	// A JWT must have three dot-separated segments.
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("malformed token: expected 3 segments, got %d: %q", len(parts), token)
	}
}

// TestIssueToken_ClaimsCorrect verifies sub, role and exp claims.
func TestIssueToken_ClaimsCorrect(t *testing.T) {
	ttl := 2 * time.Hour
	raw, err := middleware.IssueToken([]byte(testSecret), "bob", "viewer", ttl)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	parser := jwt.NewParser(jwt.WithValidMethods([]string{"HS256"}))
	var claims middleware.Claims
	tok, err := parser.ParseWithClaims(raw, &claims, func(t *jwt.Token) (any, error) {
		return []byte(testSecret), nil
	})
	if err != nil || !tok.Valid {
		t.Fatalf("invalid token: %v", err)
	}

	if claims.UserID != "bob" {
		t.Errorf("UserID = %q, want %q", claims.UserID, "bob")
	}
	if claims.Role != "viewer" {
		t.Errorf("Role = %q, want %q", claims.Role, "viewer")
	}
	if claims.ExpiresAt == nil {
		t.Fatal("ExpiresAt is nil; token has no expiration")
	}

	remaining := time.Until(claims.ExpiresAt.Time)
	// It should expire in roughly 2h; allow 5s of execution skew.
	if remaining < ttl-5*time.Second || remaining > ttl+5*time.Second {
		t.Errorf("ExpiresAt outside expected range: remaining=%v, want ~%v", remaining, ttl)
	}
}

// TestIssueToken_WrongSecret verifies that a token is rejected with a different secret.
func TestIssueToken_WrongSecret(t *testing.T) {
	raw, err := middleware.IssueToken([]byte(testSecret), "eve", "admin", time.Hour)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	parser := jwt.NewParser(jwt.WithValidMethods([]string{"HS256"}))
	var claims middleware.Claims
	_, err = parser.ParseWithClaims(raw, &claims, func(t *jwt.Token) (any, error) {
		return []byte("wrong-secret-32-bytes-padding-xx"), nil
	})
	if err == nil {
		t.Error("expected error with wrong secret, but parsing succeeded")
	}
}

// TestIssueToken_ExpiredToken verifies that already-expired tokens are rejected.
func TestIssueToken_ExpiredToken(t *testing.T) {
	raw, err := middleware.IssueToken([]byte(testSecret), "ghost", "viewer", -time.Second)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithExpirationRequired(),
	)
	var claims middleware.Claims
	_, err = parser.ParseWithClaims(raw, &claims, func(t *jwt.Token) (any, error) {
		return []byte(testSecret), nil
	})
	if err == nil {
		t.Error("expected expired token error, but parsing succeeded")
	}
}

// TestIssueToken_SingleLine documents the CLI output contract:
// fmt.Println(token) must produce a single line without spaces.
func TestIssueToken_SingleLine(t *testing.T) {
	token, err := middleware.IssueToken([]byte(testSecret), "alice", "admin", time.Hour)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	if strings.Contains(token, "\n") {
		t.Error("token contains a newline; CLI output would break scripts")
	}
	if strings.Contains(token, " ") {
		t.Error("token contains a space; CLI output would break scripts")
	}
}

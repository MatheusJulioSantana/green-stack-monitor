package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// claimsKey is the context key for the parsed JWT claims.
type claimsKey struct{}

// Claims holds the validated JWT payload.
type Claims struct {
	UserID string `json:"sub"`
	Role   string `json:"role"`
	jwt.RegisteredClaims
}

// JWTAuth returns a middleware that validates Bearer tokens using HMAC-SHA256.
//
// Security decisions:
//   - Only HS256 accepted (explicit algorithm allowlist — prevents "none" attack).
//   - Expiry is always verified; clock skew tolerance is 0.
//   - The secret is a []byte, not a string, to avoid accidental logging.
//   - The parsed claims are stored in context under an unexported key so
//     external packages must use ClaimsFromCtx — no raw map[string]interface{}.
func JWTAuth(secret []byte) func(http.Handler) http.Handler {
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
	)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, err := extractBearerToken(r)
			if err != nil {
				http.Error(w, "missing or malformed Authorization header", http.StatusUnauthorized)
				return
			}

			var claims Claims
			token, err := parser.ParseWithClaims(raw, &claims, func(t *jwt.Token) (any, error) {
				return secret, nil
			})
			if err != nil || !token.Valid {
				http.Error(w, "invalid or expired token", http.StatusUnauthorized)
				return
			}

			ctx := context.WithValue(r.Context(), claimsKey{}, &claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ClaimsFromCtx retrieves the validated Claims from the request context.
// Returns nil if the middleware was not applied.
func ClaimsFromCtx(ctx context.Context) *Claims {
	c, _ := ctx.Value(claimsKey{}).(*Claims)
	return c
}

// IssueToken creates a signed HS256 JWT for testing and CLI tooling.
// In production, tokens should be issued by your auth service.
func IssueToken(secret []byte, userID, role string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID: userID,
		Role:   role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
}

func extractBearerToken(r *http.Request) (string, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", errors.New("missing Authorization header")
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", errors.New("authorization header must be 'Bearer <token>'")
	}
	return parts[1], nil
}

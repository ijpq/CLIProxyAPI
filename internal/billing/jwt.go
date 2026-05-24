package billing

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ErrInvalidToken is returned by ParseToken when the token is malformed,
// expired, or signed with the wrong key.
var ErrInvalidToken = errors.New("billing: invalid token")

// TokenClaims is the minimal claim set issued by the portal.
type TokenClaims struct {
	UserID  string `json:"sub"`
	IsAdmin bool   `json:"adm,omitempty"`
	jwt.RegisteredClaims
}

// TokenIssuer signs and verifies portal session tokens using HS256.
type TokenIssuer struct {
	secret []byte
	ttl    time.Duration
}

// NewTokenIssuer constructs an issuer with the given shared secret and TTL.
// A zero or negative ttl defaults to 24h.
func NewTokenIssuer(secret string, ttl time.Duration) *TokenIssuer {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &TokenIssuer{secret: []byte(secret), ttl: ttl}
}

// Issue returns a signed token for the given user.
func (i *TokenIssuer) Issue(userID string, isAdmin bool) (string, error) {
	if i == nil || len(i.secret) == 0 {
		return "", fmt.Errorf("billing: token issuer not configured")
	}
	now := time.Now()
	claims := TokenClaims{
		UserID:  userID,
		IsAdmin: isAdmin,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(i.ttl)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(i.secret)
	if err != nil {
		return "", fmt.Errorf("billing: sign token: %w", err)
	}
	return signed, nil
}

// Parse validates the token and returns its claims.
func (i *TokenIssuer) Parse(raw string) (*TokenClaims, error) {
	if i == nil || len(i.secret) == 0 {
		return nil, fmt.Errorf("billing: token issuer not configured")
	}
	parsed, err := jwt.ParseWithClaims(raw, &TokenClaims{}, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, ErrInvalidToken
		}
		return i.secret, nil
	})
	if err != nil || parsed == nil || !parsed.Valid {
		return nil, ErrInvalidToken
	}
	claims, ok := parsed.Claims.(*TokenClaims)
	if !ok || claims.UserID == "" {
		return nil, ErrInvalidToken
	}
	return claims, nil
}

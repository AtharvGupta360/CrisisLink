package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/AtharvGupta360/CrisisLink/internal/platform/config"
)

// Claims is our JWT payload: the standard registered claims (exp, iat) plus the
// app-specific identity we want available on every request WITHOUT a DB lookup.
// Note: JWT payloads are only base64-encoded, not encrypted — never put secrets
// (like the password) in here. Anyone can read it; they just can't forge it.
type Claims struct {
	UserID   string `json:"userId"`
	Username string `json:"username"`
	Role     string `json:"role"`

	// Ownership bindings, carried in the token so an authorization decision needs
	// no database round trip on every request — the whole point of a stateless JWT.
	//
	// THE TRADEOFF, stated honestly: these go STALE. If an admin reassigns a
	// responder to a different unit, the old token keeps the old binding until it
	// expires. Staleness is therefore bounded by token lifetime, which is the
	// argument for keeping that lifetime short (and for refresh tokens). The
	// alternative — reading the user row on every request — is correct but throws
	// away statelessness and puts a query on the hot path.
	UnitID    string `json:"unitId,omitempty"`
	ShelterID string `json:"shelterId,omitempty"`

	jwt.RegisteredClaims
}

// GenerateToken signs a new HS256 token carrying the user's identity, expiring
// after cfg.ExpiryHours. The signature = HMAC-SHA256(header.payload, secretKey);
// without the secret key an attacker cannot alter the claims undetected.
func GenerateToken(userID, username, role, unitID, shelterID string, cfg *config.JWTConfig) (string, error) {
	claims := Claims{
		UserID:    userID,
		Username:  username,
		Role:      role,
		UnitID:    unitID,
		ShelterID: shelterID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Duration(cfg.ExpiryHours) * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(cfg.SecretKey))
	if err != nil {
		return "", fmt.Errorf("failed to sign token: %w", err)
	}
	return signed, nil
}

// ValidateToken parses and verifies a token, returning its claims. The keyfunc
// asserts the signing method is HMAC before handing back the key — this blocks
// the classic "alg confusion" attack (attacker swaps alg to 'none' or RS256 to
// bypass verification).
func ValidateToken(tokenString string, cfg *config.JWTConfig) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(cfg.SecretKey), nil
	})
	if err != nil {
		return nil, fmt.Errorf("invalid token: %w", err)
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}
	return claims, nil
}

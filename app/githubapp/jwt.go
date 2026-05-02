package githubapp

import (
	"crypto/rsa"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// signJWT produces a GitHub-App-bound JWT signed with RS256 from the given
// private key. iat is offset 60s into the past so GitHub tolerates clock
// skew on either side; exp is the 10min ceiling that GitHub enforces.
//
// `now` is injected so tests can pin time without sleeping.
func signJWT(privateKey *rsa.PrivateKey, appID int64, now time.Time) (string, error) {
	claims := jwt.MapClaims{
		"iss": appID,
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(10 * time.Minute).Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := tok.SignedString(privateKey)
	if err != nil {
		return "", fmt.Errorf("sign jwt: %w", err)
	}
	return signed, nil
}

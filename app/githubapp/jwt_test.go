package githubapp

import (
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func generateTestKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	return key
}

func TestSignJWT_RoundTripWithSameKey(t *testing.T) {
	key := generateTestKey(t)
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)

	tokenStr, err := signJWT(key, 123456, now)
	if err != nil {
		t.Fatalf("signJWT: %v", err)
	}
	if tokenStr == "" {
		t.Fatal("signJWT returned empty token")
	}

	parsed, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		return &key.PublicKey, nil
	}, jwt.WithValidMethods([]string{"RS256"}))
	if err != nil {
		t.Fatalf("jwt.Parse: %v", err)
	}
	if !parsed.Valid {
		t.Fatal("parsed token is not valid")
	}

	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatalf("claims type = %T, want MapClaims", parsed.Claims)
	}
	if got, _ := claims["iss"].(float64); int64(got) != 123456 {
		t.Errorf("iss = %v, want 123456", claims["iss"])
	}
	wantIat := now.Add(-60 * time.Second).Unix()
	if got, _ := claims["iat"].(float64); int64(got) != wantIat {
		t.Errorf("iat = %v, want %d (now-60s)", claims["iat"], wantIat)
	}
	wantExp := now.Add(10 * time.Minute).Unix()
	if got, _ := claims["exp"].(float64); int64(got) != wantExp {
		t.Errorf("exp = %v, want %d (now+10min)", claims["exp"], wantExp)
	}
}

func TestSignJWT_AlgorithmIsRS256(t *testing.T) {
	key := generateTestKey(t)
	now := time.Now()
	tokenStr, err := signJWT(key, 1, now)
	if err != nil {
		t.Fatalf("signJWT: %v", err)
	}
	parsed, _, err := jwt.NewParser().ParseUnverified(tokenStr, jwt.MapClaims{})
	if err != nil {
		t.Fatalf("ParseUnverified: %v", err)
	}
	if parsed.Method.Alg() != "RS256" {
		t.Errorf("alg = %q, want RS256", parsed.Method.Alg())
	}
}

func TestSignJWT_DifferentKeyFailsVerification(t *testing.T) {
	signKey := generateTestKey(t)
	otherKey := generateTestKey(t)

	tokenStr, err := signJWT(signKey, 1, time.Now())
	if err != nil {
		t.Fatalf("signJWT: %v", err)
	}

	_, err = jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		return &otherKey.PublicKey, nil
	}, jwt.WithValidMethods([]string{"RS256"}))
	if err == nil {
		t.Fatal("expected verification to fail with different public key")
	}
}

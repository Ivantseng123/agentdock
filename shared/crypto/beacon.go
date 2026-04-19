package crypto

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	beaconKey       = "agentdock:secret_beacon"
	beaconPlaintext = "agentdock-key-check"
	beaconTTL       = 24 * time.Hour
)

// WriteBeacon encrypts a known plaintext and stores it in Redis.
// Called by the app at startup so workers can verify their key matches.
func WriteBeacon(ctx context.Context, rdb *redis.Client, key []byte) error {
	ciphertext, err := Encrypt(key, []byte(beaconPlaintext))
	if err != nil {
		return fmt.Errorf("encrypt beacon: %w", err)
	}
	return rdb.Set(ctx, beaconKey, ciphertext, beaconTTL).Err()
}

// VerifyBeacon reads the beacon from Redis and attempts to decrypt it.
// Returns nil if the key matches, an error otherwise.
// If no beacon exists (app hasn't started yet), returns nil (skip check).
func VerifyBeacon(ctx context.Context, rdb *redis.Client, key []byte) error {
	ciphertext, err := rdb.Get(ctx, beaconKey).Bytes()
	if err == redis.Nil {
		return nil // no beacon yet, app hasn't written one
	}
	if err != nil {
		return fmt.Errorf("read beacon from Redis: %w", err)
	}
	plaintext, err := Decrypt(key, ciphertext)
	if err != nil {
		return fmt.Errorf("secret_key does not match app — decryption failed")
	}
	if string(plaintext) != beaconPlaintext {
		return fmt.Errorf("secret_key does not match app — plaintext mismatch")
	}
	return nil
}

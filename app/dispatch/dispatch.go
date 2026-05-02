// Package dispatch holds the per-job secret-encryption helper shared by
// the submitJob flow in app/app.go and the retry handler in app/bot.
// The helper lives here (not in app/) so app/bot can import it without
// creating an app→bot→app cycle.
package dispatch

import (
	"encoding/json"
	"fmt"

	"github.com/Ivantseng123/agentdock/app/config"
	"github.com/Ivantseng123/agentdock/app/githubapp"
	"github.com/Ivantseng123/agentdock/shared/crypto"
)

// buildEncryptedSecrets forks cfg.Secrets into a per-job map, overlays
// GH_TOKEN with a fresh App installation token (or the static PAT, in
// PAT mode), and AES-encrypts the JSON for the worker. The fork
// matters because multiple submitJob goroutines must not mutate
// cfg.Secrets in place.
//
// In PAT mode this is byte-for-byte equivalent to the legacy auto-merge
// in config/defaults.go: staticPATSource.MintFresh returns the static
// PAT. In App mode the worker receives a token close to the full 60min
// TTL — the spec's "per-job mint" requirement.
//
// retry_handler.go (T12) reuses this helper so retry jobs also get a
// fresh token rather than a stale 50min+ snapshot from the original
// EncryptedSecrets bundle.
func BuildEncryptedSecrets(cfg *config.Config, source githubapp.TokenSource, secretKey []byte) ([]byte, error) {
	perJob := make(map[string]string, len(cfg.Secrets)+1)
	for k, v := range cfg.Secrets {
		perJob[k] = v
	}
	token, err := source.MintFresh()
	if err != nil {
		return nil, fmt.Errorf("mint installation token: %w", err)
	}
	perJob["GH_TOKEN"] = token

	secretsJSON, err := json.Marshal(perJob)
	if err != nil {
		return nil, fmt.Errorf("marshal secrets: %w", err)
	}
	encrypted, err := crypto.Encrypt(secretKey, secretsJSON)
	if err != nil {
		return nil, fmt.Errorf("encrypt secrets: %w", err)
	}
	return encrypted, nil
}

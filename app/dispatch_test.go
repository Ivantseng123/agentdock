package app

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Ivantseng123/agentdock/app/config"
	"github.com/Ivantseng123/agentdock/shared/crypto"
)

type fakeTokenSource struct {
	token   string
	mintErr error

	mu        sync.Mutex
	mintCalls atomic.Int32
}

func (f *fakeTokenSource) Get() (string, error) { return f.token, nil }

func (f *fakeTokenSource) MintFresh() (string, error) {
	f.mintCalls.Add(1)
	if f.mintErr != nil {
		return "", f.mintErr
	}
	return f.token, nil
}

func newSecretKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return key
}

func TestBuildEncryptedSecrets_OverlaysGHTokenFromMint(t *testing.T) {
	cfg := &config.Config{
		Secrets: map[string]string{
			"GH_TOKEN":          "stale-pre-mint",
			"MANTIS_API_TOKEN":  "mantis-tok",
			"OTHER":             "kept",
		},
	}
	src := &fakeTokenSource{token: "ghs_fresh"}
	key := newSecretKey(t)

	encrypted, err := buildEncryptedSecrets(cfg, src, key)
	if err != nil {
		t.Fatalf("buildEncryptedSecrets: %v", err)
	}

	plain, err := crypto.Decrypt(key, encrypted)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(plain, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got["GH_TOKEN"] != "ghs_fresh" {
		t.Errorf("GH_TOKEN = %q, want ghs_fresh (overlaid)", got["GH_TOKEN"])
	}
	if got["MANTIS_API_TOKEN"] != "mantis-tok" {
		t.Errorf("MANTIS_API_TOKEN = %q, want mantis-tok", got["MANTIS_API_TOKEN"])
	}
	if got["OTHER"] != "kept" {
		t.Errorf("OTHER = %q, want kept", got["OTHER"])
	}
	if calls := src.mintCalls.Load(); calls != 1 {
		t.Errorf("MintFresh calls = %d, want 1", calls)
	}
}

func TestBuildEncryptedSecrets_DoesNotMutateCfgSecrets(t *testing.T) {
	cfg := &config.Config{
		Secrets: map[string]string{"GH_TOKEN": "original-pat"},
	}
	src := &fakeTokenSource{token: "minted"}
	key := newSecretKey(t)

	if _, err := buildEncryptedSecrets(cfg, src, key); err != nil {
		t.Fatalf("buildEncryptedSecrets: %v", err)
	}
	if got := cfg.Secrets["GH_TOKEN"]; got != "original-pat" {
		t.Errorf("cfg.Secrets[GH_TOKEN] = %q, want original-pat (unchanged)", got)
	}
}

func TestBuildEncryptedSecrets_MintErrorPropagates(t *testing.T) {
	cfg := &config.Config{Secrets: map[string]string{}}
	wantErr := errors.New("mint exploded")
	src := &fakeTokenSource{mintErr: wantErr}
	key := newSecretKey(t)

	_, err := buildEncryptedSecrets(cfg, src, key)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("errors.Is(%v, mint exploded) = false", err)
	}
}

func TestBuildEncryptedSecrets_PATModeEquivalentToAutomerge(t *testing.T) {
	// PAT mode: the legacy auto-merge in config/defaults.go puts
	// cfg.GitHub.Token into cfg.Secrets["GH_TOKEN"]. With staticPATSource
	// returning that same string from MintFresh, the encrypted bundle
	// must contain the same GH_TOKEN — proving regression-free behavior.
	pat := "ghp_legacy"
	cfg := &config.Config{Secrets: map[string]string{"GH_TOKEN": pat}}
	src := &fakeTokenSource{token: pat}
	key := newSecretKey(t)

	encrypted, err := buildEncryptedSecrets(cfg, src, key)
	if err != nil {
		t.Fatalf("buildEncryptedSecrets: %v", err)
	}
	plain, _ := crypto.Decrypt(key, encrypted)
	var got map[string]string
	_ = json.Unmarshal(plain, &got)
	if got["GH_TOKEN"] != pat {
		t.Errorf("PAT mode GH_TOKEN = %q, want %q", got["GH_TOKEN"], pat)
	}
}

func TestBuildEncryptedSecrets_ConcurrentCallsNoRace(t *testing.T) {
	cfg := &config.Config{Secrets: map[string]string{"K": "v"}}
	src := &fakeTokenSource{token: "tok"}
	key := newSecretKey(t)

	const n = 16
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			if _, err := buildEncryptedSecrets(cfg, src, key); err != nil {
				t.Errorf("concurrent call: %v", err)
			}
		}()
	}
	wg.Wait()
}

package dispatch

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Ivantseng123/agentdock/app/config"
	"github.com/Ivantseng123/agentdock/app/githubapp"
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

func (f *fakeTokenSource) IsAccessible(_ string) bool { return true }

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

	encrypted, err := BuildEncryptedSecrets(cfg, src, key)
	if err != nil {
		t.Fatalf("BuildEncryptedSecrets: %v", err)
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

	if _, err := BuildEncryptedSecrets(cfg, src, key); err != nil {
		t.Fatalf("BuildEncryptedSecrets: %v", err)
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

	_, err := BuildEncryptedSecrets(cfg, src, key)
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

	encrypted, err := BuildEncryptedSecrets(cfg, src, key)
	if err != nil {
		t.Fatalf("BuildEncryptedSecrets: %v", err)
	}
	plain, _ := crypto.Decrypt(key, encrypted)
	var got map[string]string
	_ = json.Unmarshal(plain, &got)
	if got["GH_TOKEN"] != pat {
		t.Errorf("PAT mode GH_TOKEN = %q, want %q", got["GH_TOKEN"], pat)
	}
}

// TestBuildEncryptedSecrets_PATMode_BlobMatchesLegacyAutomerge proves
// AC-1 byte-for-byte: the new BuildEncryptedSecrets path with a
// staticPATSource produces the same plaintext map as the legacy
// "marshal cfg.Secrets directly" path that submitJob used pre-PR.
// AES-GCM ciphertexts differ by nonce; the JSON map underneath must match.
func TestBuildEncryptedSecrets_PATMode_BlobMatchesLegacyAutomerge(t *testing.T) {
	pat := "ghp_byte_for_byte"
	cfg := &config.Config{Secrets: map[string]string{
		"GH_TOKEN":         pat,
		"MANTIS_API_TOKEN": "m1",
		"OTHER":            "stays",
	}}
	key := newSecretKey(t)

	legacyJSON, err := json.Marshal(cfg.Secrets)
	if err != nil {
		t.Fatalf("legacy marshal: %v", err)
	}
	legacyCipher, err := crypto.Encrypt(key, legacyJSON)
	if err != nil {
		t.Fatalf("legacy encrypt: %v", err)
	}

	newCipher, err := BuildEncryptedSecrets(cfg, githubapp.NewPATSource(pat), key)
	if err != nil {
		t.Fatalf("BuildEncryptedSecrets: %v", err)
	}

	legacyPlain, err := crypto.Decrypt(key, legacyCipher)
	if err != nil {
		t.Fatalf("legacy decrypt: %v", err)
	}
	newPlain, err := crypto.Decrypt(key, newCipher)
	if err != nil {
		t.Fatalf("new decrypt: %v", err)
	}

	var legacyMap, newMap map[string]string
	if err := json.Unmarshal(legacyPlain, &legacyMap); err != nil {
		t.Fatalf("legacy unmarshal: %v", err)
	}
	if err := json.Unmarshal(newPlain, &newMap); err != nil {
		t.Fatalf("new unmarshal: %v", err)
	}
	if !maps.Equal(legacyMap, newMap) {
		t.Errorf("PAT-mode plaintext divergence:\n  legacy = %v\n  new    = %v", legacyMap, newMap)
	}
}

type fakeAccessSource struct {
	*fakeTokenSource
	accessible map[string]bool
}

func (f *fakeAccessSource) IsAccessible(repo string) bool {
	v, ok := f.accessible[repo]
	if !ok {
		return false
	}
	return v
}

func TestChooseJobSource_RepoInSet_ReturnsPrimarySource(t *testing.T) {
	primary := &fakeAccessSource{
		fakeTokenSource: &fakeTokenSource{token: "ghs_primary"},
		accessible:      map[string]bool{"acme/svc": true},
	}
	chosen, fallback, err := ChooseJobSource("ghp_pat", primary, "acme/svc")
	if err != nil {
		t.Fatalf("ChooseJobSource: %v", err)
	}
	if fallback {
		t.Error("fallback should be false when repo is in set")
	}
	if chosen != primary {
		t.Errorf("chosen = %T, want primary", chosen)
	}
}

func TestChooseJobSource_NotInSetWithPAT_FallsBack(t *testing.T) {
	primary := &fakeAccessSource{
		fakeTokenSource: &fakeTokenSource{token: "ghs_primary"},
		accessible:      map[string]bool{"acme/svc": true},
	}
	chosen, fallback, err := ChooseJobSource("ghp_pat", primary, "other-org/repo")
	if err != nil {
		t.Fatalf("ChooseJobSource: %v", err)
	}
	if !fallback {
		t.Error("fallback should be true when repo is outside set + PAT configured")
	}
	if chosen == primary {
		t.Error("chosen must be a different source when falling back")
	}
	tok, _ := chosen.MintFresh()
	if tok != "ghp_pat" {
		t.Errorf("fallback source token = %q, want ghp_pat", tok)
	}
}

func TestChooseJobSource_NotInSetNoPAT_ReturnsError(t *testing.T) {
	primary := &fakeAccessSource{
		fakeTokenSource: &fakeTokenSource{token: "ghs_primary"},
		accessible:      map[string]bool{"acme/svc": true},
	}
	_, _, err := ChooseJobSource("", primary, "other-org/repo")
	if err == nil {
		t.Fatal("expected error when repo is outside set and no PAT configured")
	}
	if !strings.Contains(err.Error(), "other-org") {
		t.Errorf("error should name owner; got %v", err)
	}
	if !strings.Contains(err.Error(), "install at the org or set github.token") {
		t.Errorf("error should hint at remediation; got %v", err)
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
			if _, err := BuildEncryptedSecrets(cfg, src, key); err != nil {
				t.Errorf("concurrent call: %v", err)
			}
		}()
	}
	wg.Wait()
}

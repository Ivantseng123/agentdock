package github

import (
	"fmt"
	"net/http"
)

// tokenTransport injects an `Authorization: Bearer <token>` header on every
// outbound request, sourcing the token from tokenFn at request time. This
// lets app-side gh clients rotate tokens without rebuilding the underlying
// http.Client (e.g. when an installation token approaches expiry and the
// TokenSource mints a fresh one).
//
// Empty token from tokenFn → no Authorization header is set, leaving auth
// to whatever the delegate produces. tokenFn errors abort the request
// before it goes on the wire.
type tokenTransport struct {
	tokenFn  func() (string, error)
	delegate http.RoundTripper
}

func newTokenTransport(tokenFn func() (string, error), delegate http.RoundTripper) *tokenTransport {
	if delegate == nil {
		delegate = http.DefaultTransport
	}
	return &tokenTransport{tokenFn: tokenFn, delegate: delegate}
}

func (t *tokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := t.tokenFn()
	if err != nil {
		return nil, fmt.Errorf("token source: %w", err)
	}

	cloned := req.Clone(req.Context())
	if token != "" {
		cloned.Header.Set("Authorization", "Bearer "+token)
	}
	return t.delegate.RoundTrip(cloned)
}

package github

import (
	"reflect"
	"strings"
	"testing"
)

// This file proves the refactor in
// "refactor(shared/github): replace hand-rolled containsIgnoreCase"
// is behaviour-preserving for SearchRepos. It runs the legacy
// implementation (preserved verbatim below) and the new
// strings.Contains/ToLower implementation against a shared input
// matrix, and asserts byte-equal results.
//
// Once this PR merges, this file may be deleted: ongoing behaviour is
// pinned by the regular SearchRepos tests in discovery_test.go.

// legacyContainsIgnoreCase is the verbatim pre-refactor implementation,
// quoted from the SearchRepos hot path. Range-over-string indexes by
// rune-start, so multi-byte UTF-8 sequences leave gaps that stay 0x00 —
// the parity test below characterises whether that ever produces a
// different match outcome than strings.Contains/ToLower.
func legacyContainsIgnoreCase(s, substr string) bool {
	sLower := make([]byte, len(s))
	subLower := make([]byte, len(substr))
	for i := range s {
		if s[i] >= 'A' && s[i] <= 'Z' {
			sLower[i] = s[i] + 32
		} else {
			sLower[i] = s[i]
		}
	}
	for i := range substr {
		if substr[i] >= 'A' && substr[i] <= 'Z' {
			subLower[i] = substr[i] + 32
		} else {
			subLower[i] = substr[i]
		}
	}
	return legacyBytesContains(sLower, subLower)
}

func legacyBytesContains(s, sub []byte) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i] == sub[0] {
			match := true
			for j := 1; j < len(sub); j++ {
				if s[i+j] != sub[j] {
					match = false
					break
				}
			}
			if match {
				return true
			}
		}
	}
	return false
}

// legacySearchReposFilter mirrors the loop body of the pre-refactor
// SearchRepos: empty-query short-circuit + 25-cap + legacy matcher.
func legacySearchReposFilter(all []string, query string) []string {
	if query == "" {
		if len(all) > 25 {
			return all[:25]
		}
		return all
	}
	var matched []string
	for _, r := range all {
		if legacyContainsIgnoreCase(r, query) {
			matched = append(matched, r)
			if len(matched) >= 25 {
				break
			}
		}
	}
	return matched
}

// newSearchReposFilter mirrors the post-refactor SearchRepos loop body.
func newSearchReposFilter(all []string, query string) []string {
	if query == "" {
		if len(all) > 25 {
			return all[:25]
		}
		return all
	}
	q := strings.ToLower(query)
	var matched []string
	for _, r := range all {
		if strings.Contains(strings.ToLower(r), q) {
			matched = append(matched, r)
			if len(matched) >= 25 {
				break
			}
		}
	}
	return matched
}

func TestSearchRepos_ParityWithLegacyImpl(t *testing.T) {
	// Realistic GitHub repo names — ASCII per GitHub's repo naming rules
	// (alphanumerics, dot, hyphen, underscore, slash).
	asciiCache := []string{
		"Acme/Service",
		"acme/Library",
		"OtherOrg/service-tools",
		"OtherOrg/unrelated",
		"foo/bar",
		"Foo/Bar",
		"FOO/BAR",
		"a/b",
		"123/456",
		"with.dot/repo_name",
		"with-dash/and.dots",
	}
	asciiQueries := []string{
		"",        // empty short-circuit
		"service", // lower
		"Service", // mixed
		"SERVICE", // upper
		"foo",     // collision with multiple
		"FOO",
		"Foo",
		"missing",  // no match
		"a",        // very short
		"/",        // delimiter
		".",        // dot
		"-",        // dash
		"_",        // underscore
		"123",      // digits
		"with.dot", // dotted
	}

	for _, q := range asciiQueries {
		t.Run("ascii q="+q, func(t *testing.T) {
			oldOut := legacySearchReposFilter(asciiCache, q)
			newOut := newSearchReposFilter(asciiCache, q)
			if !reflect.DeepEqual(oldOut, newOut) {
				t.Errorf("ASCII parity broken for %q\n  legacy: %v\n  new:    %v", q, oldOut, newOut)
			}
		})
	}

	// Non-ASCII inputs are out of scope for SearchRepos in production
	// (GitHub forbids them in repo names) but we still record the
	// behavioural relationship so future readers don't have to re-derive
	// it: legacy's range-over-string lowercases by rune index, leaving
	// 0x00 gaps in multi-byte sequences. Source and query both get the
	// same gaps, so for symmetric multi-byte inputs the legacy matcher
	// happens to still match. We assert equality on a few such cases
	// rather than claiming "legacy is broken" — that turned out to be
	// wrong on real inputs.
	nonAsciiCases := []struct {
		repos []string
		query string
	}{
		{[]string{"café/repo"}, "CAFÉ"},
		{[]string{"café/repo"}, "café"},
		{[]string{"naïve/lib"}, "NAÏVE"},
		{[]string{"naïve/lib"}, "naive"}, // diacritic-stripped query — both miss
		{[]string{"日本語/repo"}, "日本"},   // CJK
	}
	for i, tc := range nonAsciiCases {
		t.Run("non-ascii", func(t *testing.T) {
			oldOut := legacySearchReposFilter(tc.repos, tc.query)
			newOut := newSearchReposFilter(tc.repos, tc.query)
			if !reflect.DeepEqual(oldOut, newOut) {
				t.Errorf("non-ASCII parity broken at case %d (%q in %v)\n  legacy: %v\n  new:    %v",
					i, tc.query, tc.repos, oldOut, newOut)
			}
		})
	}

	// 25-cap parity: 50 matching entries, both impls must cap at 25.
	bigCache := make([]string, 50)
	for i := range bigCache {
		bigCache[i] = "org/repo-" + string(rune('A'+i%26))
	}
	t.Run("cap parity", func(t *testing.T) {
		oldOut := legacySearchReposFilter(bigCache, "repo")
		newOut := newSearchReposFilter(bigCache, "repo")
		if !reflect.DeepEqual(oldOut, newOut) || len(oldOut) != 25 {
			t.Errorf("cap parity broken: legacy len=%d new len=%d", len(oldOut), len(newOut))
		}
	})
}

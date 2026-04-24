package connectivity

import (
	"strings"
	"testing"
)

func TestParseGitVersion(t *testing.T) {
	cases := []struct {
		name           string
		in             string
		wantMajor      int
		wantMinor      int
		wantErrSubstr  string
	}{
		{"plain linux build", "git version 2.31.0", 2, 31, ""},
		{"apple git with vendor suffix", "git version 2.50.1 (Apple Git-155)", 2, 50, ""},
		{"three-digit minor", "git version 2.100.0", 2, 100, ""},
		{"major only fails", "git version 2", 0, 0, "unrecognised git version number"},
		{"missing version field", "git version", 0, 0, "unrecognised git --version output"},
		{"non-numeric major", "git version a.31.0", 0, 0, "parse git major version"},
		{"non-numeric minor", "git version 2.x.0", 0, 0, "parse git minor version"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			major, minor, err := parseGitVersion(tc.in)
			if tc.wantErrSubstr != "" {
				if err == nil {
					t.Fatalf("want error containing %q, got nil", tc.wantErrSubstr)
				}
				if !strings.Contains(err.Error(), tc.wantErrSubstr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErrSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if major != tc.wantMajor || minor != tc.wantMinor {
				t.Errorf("got %d.%d, want %d.%d", major, minor, tc.wantMajor, tc.wantMinor)
			}
		})
	}
}

// TestCheckGitVersion_Smoke asserts the local git satisfies the floor we
// require in CI / dev environments. It's a smoke test rather than an
// exhaustive check because the actual binary is environment-dependent.
func TestCheckGitVersion_Smoke(t *testing.T) {
	if err := CheckGitVersion(); err != nil {
		t.Fatalf("local git fails version check (CI/dev expected to have git >= 2.31): %v", err)
	}
}

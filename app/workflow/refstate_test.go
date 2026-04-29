package workflow

import (
	"reflect"
	"testing"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

func TestRefExclusionsFor(t *testing.T) {
	cases := []struct {
		name     string
		primary  string
		refs     []queue.RefRepo
		expected []string
	}{
		{
			name:     "both populated",
			primary:  "foo/bar",
			refs:     []queue.RefRepo{{Repo: "a/b"}, {Repo: "c/d"}},
			expected: []string{"foo/bar", "a/b", "c/d"},
		},
		{
			name:     "empty primary",
			primary:  "",
			refs:     []queue.RefRepo{{Repo: "a/b"}},
			expected: []string{"a/b"},
		},
		{
			name:     "empty refs",
			primary:  "foo/bar",
			refs:     nil,
			expected: []string{"foo/bar"},
		},
		{
			name:     "both empty",
			primary:  "",
			refs:     nil,
			expected: []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := refExclusionsFor(tc.primary, tc.refs)
			// Treat nil and []string{} as equivalent for the empty case —
			// both have len 0; reflect.DeepEqual would distinguish them.
			if len(got) == 0 && len(tc.expected) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.expected) {
				t.Fatalf("got %v, want %v", got, tc.expected)
			}
		})
	}
}

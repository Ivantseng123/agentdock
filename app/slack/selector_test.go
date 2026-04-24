package slack

import (
	"fmt"
	"testing"
)

// buildOptions returns n placeholder SmartSelectorOptions.
func buildOptions(n int) []SmartSelectorOption {
	opts := make([]SmartSelectorOption, n)
	for i := 0; i < n; i++ {
		opts[i] = SmartSelectorOption{
			Label: fmt.Sprintf("opt-%d", i),
			Value: fmt.Sprintf("v-%d", i),
		}
	}
	return opts
}

// TestSelectorRenderMode_Thresholds locks the boundary behaviour that
// callers rely on. If Slack ever raises the actions-block cap, this table
// is the single source of truth to bump.
func TestSelectorRenderMode_Thresholds(t *testing.T) {
	cases := []struct {
		name   string
		spec   SmartSelectorSpec
		want   selectorRenderKind
		reason string
	}{
		{
			name:   "searchable overrides everything",
			spec:   SmartSelectorSpec{Searchable: true},
			want:   renderExternalSelect,
			reason: "external search is explicit, options count is irrelevant",
		},
		{
			name:   "1 option -> button",
			spec:   SmartSelectorSpec{Options: buildOptions(1)},
			want:   renderButton,
			reason: "far under cap",
		},
		{
			name:   "24 options -> button (exactly at cap, no extras)",
			spec:   SmartSelectorSpec{Options: buildOptions(24)},
			want:   renderButton,
			reason: "24 + 0 extras ≤ 25",
		},
		{
			name:   "25 options -> button (exactly at cap, no extras)",
			spec:   SmartSelectorSpec{Options: buildOptions(25)},
			want:   renderButton,
			reason: "25 + 0 extras ≤ 25",
		},
		{
			name:   "26 options -> static_select",
			spec:   SmartSelectorSpec{Options: buildOptions(26)},
			want:   renderStaticSelect,
			reason: "26 exceeds actions-block cap of 25",
		},
		{
			name:   "24 options + back button -> static_select",
			spec:   SmartSelectorSpec{Options: buildOptions(24), BackActionID: "back"},
			want:   renderButton,
			reason: "24 + 1 extra = 25, still ≤ cap",
		},
		{
			name:   "25 options + back button -> static_select",
			spec:   SmartSelectorSpec{Options: buildOptions(25), BackActionID: "back"},
			want:   renderStaticSelect,
			reason: "25 + 1 extra = 26 > cap; must fall back to dropdown",
		},
		{
			name: "23 options + back + cancel -> button",
			spec: SmartSelectorSpec{
				Options:        buildOptions(23),
				BackActionID:   "back",
				CancelActionID: "cancel",
			},
			want:   renderButton,
			reason: "23 + 2 extras = 25, ≤ cap",
		},
		{
			name: "24 options + back + cancel -> static_select",
			spec: SmartSelectorSpec{
				Options:        buildOptions(24),
				BackActionID:   "back",
				CancelActionID: "cancel",
			},
			want:   renderStaticSelect,
			reason: "24 + 2 extras = 26 > cap",
		},
		{
			name:   "100 options -> static_select (exactly at dropdown cap)",
			spec:   SmartSelectorSpec{Options: buildOptions(100)},
			want:   renderStaticSelect,
			reason: "100 options fits static_select",
		},
		{
			name:   "101 options -> external_select",
			spec:   SmartSelectorSpec{Options: buildOptions(101)},
			want:   renderExternalSelect,
			reason: "beyond static_select cap; auto-upgrade to type-ahead (issue #153)",
		},
		{
			name:   "500 options -> external_select",
			spec:   SmartSelectorSpec{Options: buildOptions(500)},
			want:   renderExternalSelect,
			reason: "large lists degrade to type-ahead rather than silent truncation",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := selectorRenderMode(tc.spec)
			if got != tc.want {
				t.Errorf("selectorRenderMode() = %d, want %d (%s)", got, tc.want, tc.reason)
			}
		})
	}
}

// TestSelectorRenderMode_SearchableIgnoresOptions — when Searchable is set,
// the option count has no effect; external select is always picked so the
// caller's static options list can ride along as a search corpus without
// triggering button-row rendering.
func TestSelectorRenderMode_SearchableIgnoresOptions(t *testing.T) {
	for _, n := range []int{0, 3, 25, 200} {
		spec := SmartSelectorSpec{Searchable: true, Options: buildOptions(n)}
		if got := selectorRenderMode(spec); got != renderExternalSelect {
			t.Errorf("options=%d: got %d, want renderExternalSelect", n, got)
		}
	}
}

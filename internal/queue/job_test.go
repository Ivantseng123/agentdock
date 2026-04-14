package queue

import (
	"encoding/json"
	"testing"
)

func TestSkillPayload_JSONRoundTrip(t *testing.T) {
	original := map[string]*SkillPayload{
		"code-review": {
			Files: map[string][]byte{
				"SKILL.md":        []byte("# Code Review Skill"),
				"examples/ex1.md": []byte("example content"),
			},
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded map[string]*SkillPayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	sp, ok := decoded["code-review"]
	if !ok {
		t.Fatal("missing code-review key")
	}
	if string(sp.Files["SKILL.md"]) != "# Code Review Skill" {
		t.Errorf("SKILL.md = %q", string(sp.Files["SKILL.md"]))
	}
	if string(sp.Files["examples/ex1.md"]) != "example content" {
		t.Errorf("examples/ex1.md = %q", string(sp.Files["examples/ex1.md"]))
	}
}

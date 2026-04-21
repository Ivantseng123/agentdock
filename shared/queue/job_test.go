package queue

import (
	"encoding/json"
	"strings"
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

func TestPromptContext_JSONRoundTrip(t *testing.T) {
	orig := PromptContext{
		ThreadMessages: []ThreadMessage{
			{User: "Alice", Timestamp: "2026-04-09 10:30", Text: "login broken"},
		},
		ExtraDescription: "after 3 retries",
		Channel:          "general",
		Reporter:         "Alice",
		Branch:           "main",
		Language:         "zh-TW",
		Goal:             "Use the /triage-issue skill ...",
		OutputRules:      []string{"一句話", "< 100 字"},
		AllowWorkerRules: true,
	}

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got PromptContext
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.ThreadMessages[0].User != "Alice" {
		t.Errorf("User = %q, want Alice", got.ThreadMessages[0].User)
	}
	if got.Goal != orig.Goal {
		t.Errorf("Goal = %q, want %q", got.Goal, orig.Goal)
	}
	if len(got.OutputRules) != 2 {
		t.Errorf("OutputRules len = %d, want 2", len(got.OutputRules))
	}
	if !got.AllowWorkerRules {
		t.Error("AllowWorkerRules = false, want true")
	}
}

// TestJobResult_NoIssueSpecificFields asserts that JobResult does NOT carry
// Issue-specific fields (Title, Body, Labels, Confidence, FilesFound,
// Questions, Message). These are now owned by workflow-local types (e.g.
// TriageResult) and must not appear on the wire type.
func TestJobResult_NoIssueSpecificFields(t *testing.T) {
	r := JobResult{}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	for _, banned := range []string{"title", "body", "labels", "confidence", "files_found", "open_questions", "message"} {
		if strings.Contains(s, `"`+banned+`"`) {
			t.Errorf("JobResult JSON must not contain field %q; got %s", banned, s)
		}
	}
}

func TestJob_PromptContextField_JSONRoundTrip(t *testing.T) {
	job := Job{
		ID: "test-1",
		PromptContext: &PromptContext{
			Channel:  "general",
			Reporter: "Bob",
			Goal:     "triage",
		},
	}
	data, err := json.Marshal(&job)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Job
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.PromptContext == nil {
		t.Fatal("PromptContext is nil after round-trip")
	}
	if got.PromptContext.Goal != "triage" {
		t.Errorf("Goal = %q, want triage", got.PromptContext.Goal)
	}
}

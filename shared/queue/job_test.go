package queue

import (
	"encoding/json"
	"reflect"
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
		ResponseSchema:   `{"answer": "<markdown>"}`,
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
	if got.ResponseSchema != orig.ResponseSchema {
		t.Errorf("ResponseSchema = %q, want %q", got.ResponseSchema, orig.ResponseSchema)
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

func TestJob_WorkflowArgsRoundTrips(t *testing.T) {
	j := &Job{
		ID:       "j1",
		TaskType: "pr_review",
		WorkflowArgs: map[string]string{"pr_url": "https://github.com/foo/bar/pull/7", "pr_number": "7"},
	}
	buf, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	var got Job
	if err := json.Unmarshal(buf, &got); err != nil {
		t.Fatal(err)
	}
	if got.WorkflowArgs["pr_url"] != "https://github.com/foo/bar/pull/7" {
		t.Errorf("WorkflowArgs[pr_url] = %q", got.WorkflowArgs["pr_url"])
	}
	if got.WorkflowArgs["pr_number"] != "7" {
		t.Errorf("WorkflowArgs[pr_number] = %q", got.WorkflowArgs["pr_number"])
	}
}

func TestJob_WorkflowArgsOmitEmpty(t *testing.T) {
	j := &Job{ID: "j1", TaskType: "issue"}
	buf, _ := json.Marshal(j)
	if strings.Contains(string(buf), "workflow_args") {
		t.Errorf("empty WorkflowArgs should be omitted: %s", buf)
	}
}

// TestJob_RefRepos_RoundTrip ensures the new RefRepos field marshals and
// unmarshals losslessly. Default-branch refs (Branch == "") must drop the
// field via omitempty so the wire shape stays minimal.
func TestJob_RefRepos_RoundTrip(t *testing.T) {
	in := Job{
		ID:       "abc",
		Repo:     "foo/bar",
		CloneURL: "https://example/foo/bar.git",
		RefRepos: []RefRepo{
			{Repo: "frontend/web", CloneURL: "https://example/frontend/web.git", Branch: "main"},
			{Repo: "backend/api", CloneURL: "https://example/backend/api.git"},
		},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"ref_repos":[`) {
		t.Errorf("expected ref_repos array in JSON, got: %s", raw)
	}
	// Default-branch ref must omit the branch key. Marshal a single bare
	// RefRepo so the assertion isn't muddled by Job.Branch (which has no
	// omitempty by design — it's a top-level required-shaped field).
	defaultBranchOnly, _ := json.Marshal(RefRepo{Repo: "x/y", CloneURL: "u"})
	if strings.Contains(string(defaultBranchOnly), `"branch"`) {
		t.Errorf("default-branch RefRepo should omit branch field: %s", defaultBranchOnly)
	}

	var out Job
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(in.RefRepos, out.RefRepos) {
		t.Fatalf("RefRepos mismatch:\n in=%+v\nout=%+v", in.RefRepos, out.RefRepos)
	}
}

// TestJob_OldJob_NoRefRepos_StillParses asserts pre-RefRepos jobs (no field
// in JSON) parse cleanly with RefRepos == nil. Critical for the deploy-window
// where workers running new code receive jobs queued by older app code.
func TestJob_OldJob_NoRefRepos_StillParses(t *testing.T) {
	raw := []byte(`{"id":"abc","repo":"foo/bar","clone_url":"https://example/foo/bar.git"}`)
	var out Job
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.RefRepos != nil {
		t.Fatalf("expected nil RefRepos on old-job parse, got %+v", out.RefRepos)
	}
}

// TestJob_RefRepos_OmitEmpty asserts that a job with no refs serializes
// without the ref_repos key entirely.
func TestJob_RefRepos_OmitEmpty(t *testing.T) {
	j := &Job{ID: "j1", TaskType: "ask"}
	buf, _ := json.Marshal(j)
	if strings.Contains(string(buf), "ref_repos") {
		t.Errorf("empty RefRepos should be omitted: %s", buf)
	}
}

// TestPromptContext_RefFields_RoundTrip covers the worker-filled fields on
// PromptContext. Empty paths/branches drop via omitempty; populated fields
// survive a round-trip.
func TestPromptContext_RefFields_RoundTrip(t *testing.T) {
	in := PromptContext{
		Channel:  "general",
		Reporter: "Bob",
		Goal:     "ask",
		RefRepos: []RefRepoContext{
			{Repo: "frontend/web", Branch: "main", Path: "/tmp/refs/frontend__web"},
			{Repo: "backend/api", Path: "/tmp/refs/backend__api"},
		},
		UnavailableRefs: []string{"broken/repo"},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out PromptContext
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(in.RefRepos, out.RefRepos) {
		t.Errorf("RefRepos mismatch:\n in=%+v\nout=%+v", in.RefRepos, out.RefRepos)
	}
	if !reflect.DeepEqual(in.UnavailableRefs, out.UnavailableRefs) {
		t.Errorf("UnavailableRefs mismatch:\n in=%+v\nout=%+v", in.UnavailableRefs, out.UnavailableRefs)
	}
}

// TestPromptContext_RefFields_OmitEmpty asserts the new fields drop from JSON
// when unset, so the existing wire shape for non-ref jobs is unchanged.
func TestPromptContext_RefFields_OmitEmpty(t *testing.T) {
	pc := &PromptContext{Channel: "c", Reporter: "r", Goal: "g"}
	buf, _ := json.Marshal(pc)
	if strings.Contains(string(buf), "ref_repos") {
		t.Errorf("empty RefRepos should be omitted: %s", buf)
	}
	if strings.Contains(string(buf), "unavailable_refs") {
		t.Errorf("empty UnavailableRefs should be omitted: %s", buf)
	}
}

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	workerconfig "github.com/Ivantseng123/agentdock/worker/config"
	"gopkg.in/yaml.v3"
)

func TestPrintAppGitHubPromptHeader_IncludesMigrationHint(t *testing.T) {
	var buf bytes.Buffer
	printAppGitHubPromptHeader(&buf)
	out := buf.String()
	if !strings.Contains(out, "GitHub token") {
		t.Errorf("output missing PAT prompt header: %q", out)
	}
	if !strings.Contains(out, "MIGRATION-github-app.md") {
		t.Errorf("output missing migration hint: %q", out)
	}
}

func TestInitApp_YAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	if err := runInitApp(path, false, false); err != nil {
		t.Fatalf("runInitApp: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "# REQUIRED") {
		t.Error("app.yaml output should contain # REQUIRED comments")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %o, want 0600", info.Mode().Perm())
	}
}

// TestInitApp_TracingBlockPresent confirms the OTel tracing knob lands in
// the generated app.yaml with the env-override hint comment so operators
// can wire up Jaeger / Tempo without reading the docs first.
func TestInitApp_TracingBlockPresent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	if err := runInitApp(path, false, false); err != nil {
		t.Fatalf("runInitApp: %v", err)
	}
	data, _ := os.ReadFile(path)
	content := string(data)
	if !strings.Contains(content, "tracing:") {
		t.Error("app.yaml missing tracing: block")
	}
	if !strings.Contains(content, "otlp_endpoint") {
		t.Error("app.yaml missing tracing.otlp_endpoint key")
	}
	if !strings.Contains(content, "OTEL_EXPORTER_OTLP_ENDPOINT") {
		t.Error("app.yaml missing OTEL_EXPORTER_OTLP_ENDPOINT env hint")
	}
}

func TestInitWorker_TracingBlockPresent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yaml")
	if err := runInitWorker(path, false, false); err != nil {
		t.Fatalf("runInitWorker: %v", err)
	}
	data, _ := os.ReadFile(path)
	content := string(data)
	if !strings.Contains(content, "tracing:") {
		t.Error("worker.yaml missing tracing: block")
	}
	if !strings.Contains(content, "otlp_endpoint") {
		t.Error("worker.yaml missing tracing.otlp_endpoint key")
	}
	if !strings.Contains(content, "OTEL_EXPORTER_OTLP_ENDPOINT") {
		t.Error("worker.yaml missing OTEL_EXPORTER_OTLP_ENDPOINT env hint")
	}
}

// TestInitApp_EmitsNewSchema pins the v2.3 schema shape — top-level
// workflows: / prompt_defaults:, no top-level legacy prompt: / pr_review:.
// Regression guard for issue #126: accidentally emitting the legacy shape
// would undo the refactor and mislead fresh operators.
func TestInitApp_EmitsNewSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	if err := runInitApp(path, false, false); err != nil {
		t.Fatalf("runInitApp: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse generated yaml: %v", err)
	}

	// New-shape keys must be present.
	workflows, ok := parsed["workflows"].(map[string]any)
	if !ok {
		t.Fatal("generated app.yaml missing top-level workflows: block")
	}
	for _, name := range []string{"issue", "ask", "pr_review"} {
		wf, ok := workflows[name].(map[string]any)
		if !ok {
			t.Errorf("workflows.%s missing", name)
			continue
		}
		if _, ok := wf["prompt"]; !ok {
			t.Errorf("workflows.%s.prompt missing", name)
		}
	}
	if _, ok := workflows["pr_review"].(map[string]any)["enabled"]; !ok {
		t.Error("workflows.pr_review.enabled missing (feature flag moved here)")
	}
	if _, ok := parsed["prompt_defaults"]; !ok {
		t.Error("generated app.yaml missing top-level prompt_defaults: block")
	}

	// Legacy top-level blocks must NOT be emitted.
	if _, ok := parsed["prompt"]; ok {
		t.Error("generated app.yaml still emits legacy top-level prompt: block")
	}
	if _, ok := parsed["pr_review"]; ok {
		t.Error("generated app.yaml still emits legacy top-level pr_review: block")
	}
}

func TestInitApp_JSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.json")
	if err := runInitApp(path, false, false); err != nil {
		t.Fatalf("runInitApp: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if strings.Contains(string(data), "# REQUIRED") {
		t.Error("JSON output should NOT contain comments")
	}
}

func TestInitApp_RejectsExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runInitApp(path, false, false)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' error, got %v", err)
	}
}

func TestInitApp_ForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runInitApp(path, false, true); err != nil {
		t.Fatalf("runInitApp force: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) == "existing" {
		t.Error("existing content should have been overwritten")
	}
}

func TestInitApp_InteractiveRejectsNonTTY(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	err := runInitApp(path, true, false)
	if err == nil {
		t.Fatal("expected error for interactive mode without TTY")
	}
	if !strings.Contains(err.Error(), "requires a terminal") {
		t.Errorf("expected 'requires a terminal' error, got: %v", err)
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Error("config file should not exist after TTY rejection")
	}
}

func TestInitWorker_YAML_NoBuiltinSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yaml")
	if err := runInitWorker(path, false, false); err != nil {
		t.Fatalf("runInitWorker: %v", err)
	}
	data, _ := os.ReadFile(path)
	content := string(data)
	// Built-in agents must NOT be frozen into the generated yaml; they are
	// filled at runtime by mergeBuiltinAgents so operators pick up new defaults
	// automatically on binary upgrade. Parse the yaml and inspect the agents
	// map directly — a string-contains check would have to track yaml.Marshal's
	// indent style and silently miss regressions when upstream changes it.
	var parsed map[string]any
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse generated yaml: %v", err)
	}
	agents, _ := parsed["agents"].(map[string]any)
	for name := range workerconfig.BuiltinAgents {
		if _, ok := agents[name]; ok {
			t.Errorf("worker.yaml should not snapshot built-in agent %q", name)
		}
	}
	// Guidance comment for the agents: block should be present.
	if !strings.Contains(content, "# agents:") {
		t.Error("worker.yaml should include guidance comment for agents: block")
	}
	if !strings.Contains(content, "# REQUIRED") {
		t.Error("worker.yaml should include # REQUIRED hints")
	}
}

// TestInitWorker_OpencodeBlockCommented pins the init template:
// the generated worker.yaml carries a commented-out `# opencode:`
// block documenting the three configurable fields, and does NOT
// serialize an active `opencode:` block (defaults apply at runtime
// via workerconfig.ApplyDefaults when the block is absent). Server
// mode is the default after the spec C2 deviation; operators wanting
// the legacy spawn path uncomment the block and set `mode: spawn`.
func TestInitWorker_OpencodeBlockCommented(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yaml")
	if err := runInitWorker(path, false, false); err != nil {
		t.Fatalf("runInitWorker: %v", err)
	}
	data, _ := os.ReadFile(path)
	content := string(data)

	for _, want := range []string{
		"# opencode:",
		"#   mode: server",
		"#   idle_timeout: 5m",
		"#   storage_dir:",
		"Spec C2",
		"Runtime swap caveat",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("worker.yaml missing %q", want)
		}
	}

	var parsed map[string]any
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse generated yaml: %v", err)
	}
	if _, ok := parsed["opencode"]; ok {
		t.Error("worker.yaml should NOT carry an active `opencode:` block; defaults apply at runtime")
	}
}

func TestInitWorker_JSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.json")
	if err := runInitWorker(path, false, false); err != nil {
		t.Fatalf("runInitWorker: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if strings.Contains(string(data), "# REQUIRED") {
		t.Error("JSON output should NOT contain comments")
	}
}

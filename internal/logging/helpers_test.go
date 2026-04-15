package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestComponentLogger(t *testing.T) {
	var buf bytes.Buffer
	base := slog.New(slog.NewJSONHandler(&buf, nil))
	logger := ComponentLogger(base, CompSlack)

	logger.Info("test")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if entry["component"] != CompSlack {
		t.Errorf("component = %v, want %q", entry["component"], CompSlack)
	}
}

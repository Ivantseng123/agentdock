package llm

import (
	"encoding/json"
	"testing"
)

func TestOpenAIUserContent_WithImages(t *testing.T) {
	msg := Message{
		Role:    "user",
		Content: "check this",
		Images: []ImageContent{
			{Name: "shot.png", MimeType: "image/png", Data: []byte("fakepng")},
		},
	}

	blocks := buildOpenAIUserContent(msg)

	data, err := json.Marshal(blocks)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var arr []map[string]any
	if err := json.Unmarshal(data, &arr); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if len(arr) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(arr))
	}

	if arr[0]["type"] != "image_url" {
		t.Errorf("expected type=image_url, got %v", arr[0]["type"])
	}
	imgURL := arr[0]["image_url"].(map[string]any)
	urlStr := imgURL["url"].(string)
	if len(urlStr) < 22 {
		t.Error("expected non-empty data URI")
	}
	prefix := "data:image/png;base64,"
	if urlStr[:len(prefix)] != prefix {
		t.Errorf("expected data URI prefix, got %q", urlStr[:30])
	}

	if arr[1]["type"] != "text" {
		t.Errorf("expected type=text, got %v", arr[1]["type"])
	}
}

func TestOpenAIUserContent_WithoutImages(t *testing.T) {
	msg := Message{
		Role:    "user",
		Content: "plain text",
	}

	result := buildOpenAIUserContent(msg)
	str, ok := result.(string)
	if !ok {
		t.Fatalf("expected string, got %T", result)
	}
	if str != "plain text" {
		t.Errorf("expected 'plain text', got %q", str)
	}
}

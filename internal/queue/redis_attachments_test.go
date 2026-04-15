package queue

import (
	"context"
	"testing"
	"time"
)

func TestRedisAttachmentStore_PrepareAndResolve(t *testing.T) {
	client := testRedisClient(t)
	store := NewRedisAttachmentStore(client)
	ctx := context.Background()
	jobID := "job-attach-bytes-1"

	payloads := []AttachmentPayload{
		{Filename: "screenshot.png", MimeType: "image", Data: []byte("fake-png-data"), Size: 13},
		{Filename: "error.log", MimeType: "text", Data: []byte("stack trace here"), Size: 16},
	}

	if err := store.Prepare(ctx, jobID, payloads); err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}

	results, err := store.Resolve(ctx, jobID)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].Filename != "screenshot.png" {
		t.Errorf("results[0].Filename = %q, want screenshot.png", results[0].Filename)
	}
	if string(results[0].Data) != "fake-png-data" {
		t.Errorf("results[0].Data = %q, want fake-png-data", string(results[0].Data))
	}
	if results[0].MimeType != "image" {
		t.Errorf("results[0].MimeType = %q, want image", results[0].MimeType)
	}
	if results[1].Filename != "error.log" {
		t.Errorf("results[1].Filename = %q, want error.log", results[1].Filename)
	}
	if string(results[1].Data) != "stack trace here" {
		t.Errorf("results[1].Data = %q", string(results[1].Data))
	}
}

func TestRedisAttachmentStore_SkipsOversizedFile(t *testing.T) {
	client := testRedisClient(t)
	store := NewRedisAttachmentStore(client)
	ctx := context.Background()
	jobID := "job-attach-oversize"

	bigData := make([]byte, 11*1024*1024) // 11 MB
	payloads := []AttachmentPayload{
		{Filename: "huge.bin", MimeType: "document", Data: bigData, Size: int64(len(bigData))},
		{Filename: "small.txt", MimeType: "text", Data: []byte("ok"), Size: 2},
	}

	if err := store.Prepare(ctx, jobID, payloads); err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}

	results, err := store.Resolve(ctx, jobID)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1 (oversized file should be skipped)", len(results))
	}
	if results[0].Filename != "small.txt" {
		t.Errorf("results[0].Filename = %q, want small.txt", results[0].Filename)
	}
}

func TestRedisAttachmentStore_SkipsWhenJobTotalExceeded(t *testing.T) {
	client := testRedisClient(t)
	store := NewRedisAttachmentStore(client)
	ctx := context.Background()
	jobID := "job-attach-total-exceeded"

	chunk := make([]byte, 9*1024*1024) // 9 MB each
	payloads := []AttachmentPayload{
		{Filename: "a.bin", MimeType: "document", Data: chunk, Size: int64(len(chunk))},
		{Filename: "b.bin", MimeType: "document", Data: chunk, Size: int64(len(chunk))},
		{Filename: "c.bin", MimeType: "document", Data: chunk, Size: int64(len(chunk))},
		{Filename: "d.bin", MimeType: "document", Data: chunk, Size: int64(len(chunk))},
	}

	if err := store.Prepare(ctx, jobID, payloads); err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}

	results, err := store.Resolve(ctx, jobID)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	// 3 × 9 MB = 27 MB OK, 4th would be 36 MB
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
}

func TestRedisAttachmentStore_ResolveBeforePrepare(t *testing.T) {
	client := testRedisClient(t)
	store := NewRedisAttachmentStore(client)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	jobID := "job-attach-poll"

	payloads := []AttachmentPayload{
		{Filename: "data.csv", MimeType: "text", Data: []byte("a,b,c"), Size: 5},
	}

	go func() {
		time.Sleep(500 * time.Millisecond)
		if err := store.Prepare(context.Background(), jobID, payloads); err != nil {
			t.Errorf("Prepare in goroutine failed: %v", err)
		}
	}()

	results, err := store.Resolve(ctx, jobID)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if len(results) != 1 || results[0].Filename != "data.csv" {
		t.Errorf("unexpected results: %+v", results)
	}
}

func TestRedisAttachmentStore_Cleanup(t *testing.T) {
	client := testRedisClient(t)
	store := NewRedisAttachmentStore(client)
	ctx := context.Background()
	jobID := "job-attach-cleanup"

	payloads := []AttachmentPayload{
		{Filename: "report.pdf", MimeType: "document", Data: []byte("pdf-bytes"), Size: 9},
	}
	if err := store.Prepare(ctx, jobID, payloads); err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	if err := store.Cleanup(ctx, jobID); err != nil {
		t.Fatalf("Cleanup failed: %v", err)
	}

	key := keyPrefix + "jobs:attachments:" + jobID
	exists, err := client.Exists(ctx, key).Result()
	if err != nil {
		t.Fatalf("Exists check failed: %v", err)
	}
	if exists != 0 {
		t.Errorf("key %q still exists after Cleanup", key)
	}
}

func TestRedisAttachmentStore_EmptyPayloads(t *testing.T) {
	client := testRedisClient(t)
	store := NewRedisAttachmentStore(client)
	ctx := context.Background()
	jobID := "job-attach-empty"

	if err := store.Prepare(ctx, jobID, nil); err != nil {
		t.Fatalf("Prepare with nil failed: %v", err)
	}

	results, err := store.Resolve(ctx, jobID)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("got %d results, want 0", len(results))
	}
}

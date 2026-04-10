package logging

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRotator_WritesToDateFile(t *testing.T) {
	dir := t.TempDir()
	r, err := NewRotator(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	_, err = r.Write([]byte("hello\n"))
	if err != nil {
		t.Fatal(err)
	}

	expected := filepath.Join(dir, time.Now().Format("2006-01-02")+".jsonl")
	data, err := os.ReadFile(expected)
	if err != nil {
		t.Fatalf("expected file %s: %v", expected, err)
	}
	if string(data) != "hello\n" {
		t.Errorf("file content = %q, want %q", string(data), "hello\n")
	}
}

func TestRotator_MultipleWrites(t *testing.T) {
	dir := t.TempDir()
	r, err := NewRotator(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	if _, err := r.Write([]byte("line1\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Write([]byte("line2\n")); err != nil {
		t.Fatal(err)
	}

	expected := filepath.Join(dir, time.Now().Format("2006-01-02")+".jsonl")
	data, err := os.ReadFile(expected)
	if err != nil {
		t.Fatalf("expected file %s: %v", expected, err)
	}
	if string(data) != "line1\nline2\n" {
		t.Errorf("file content = %q", string(data))
	}
}

func TestRotator_Cleanup(t *testing.T) {
	dir := t.TempDir()
	r, _ := NewRotator(dir)
	defer r.Close()

	// Create fake old log files.
	oldDate := time.Now().AddDate(0, 0, -31).Format("2006-01-02")
	recentDate := time.Now().AddDate(0, 0, -5).Format("2006-01-02")
	os.WriteFile(filepath.Join(dir, oldDate+".jsonl"), []byte("old"), 0644)
	os.WriteFile(filepath.Join(dir, recentDate+".jsonl"), []byte("recent"), 0644)

	r.Cleanup(30)

	if _, err := os.Stat(filepath.Join(dir, oldDate+".jsonl")); !os.IsNotExist(err) {
		t.Error("old file should have been deleted")
	}
	if _, err := os.Stat(filepath.Join(dir, recentDate+".jsonl")); err != nil {
		t.Error("recent file should still exist")
	}
}

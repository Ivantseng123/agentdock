package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Rotator is an io.Writer that writes to daily-rotated JSONL files.
type Rotator struct {
	mu      sync.Mutex
	dir     string
	current *os.File
	curDate string
}

// NewRotator creates a Rotator that writes to dir/YYYY-MM-DD.jsonl.
func NewRotator(dir string) (*Rotator, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	r := &Rotator{dir: dir}
	if err := r.rotateLocked(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Rotator) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	today := time.Now().Format("2006-01-02")
	if today != r.curDate {
		if err := r.rotateLocked(); err != nil {
			return 0, err
		}
	}
	return r.current.Write(p)
}

func (r *Rotator) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current != nil {
		return r.current.Close()
	}
	return nil
}

func (r *Rotator) rotateLocked() error {
	if r.current != nil {
		r.current.Close()
		r.current = nil
	}
	today := time.Now().Format("2006-01-02")
	path := filepath.Join(r.dir, today+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	r.current = f
	r.curDate = today
	return nil
}

// Cleanup deletes .jsonl files older than retentionDays. Safe to call while
// writing — it only targets files from previous days.
func (r *Rotator) Cleanup(retentionDays int) {
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	today := time.Now().Format("2006-01-02")

	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		dateStr := strings.TrimSuffix(name, ".jsonl")
		if dateStr == today {
			continue
		}
		t, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			continue
		}
		if t.Before(cutoff) {
			os.Remove(filepath.Join(r.dir, name))
		}
	}
}

// StartCleanup runs Cleanup every hour in a background goroutine.
func (r *Rotator) StartCleanup(retentionDays int) {
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			r.Cleanup(retentionDays)
		}
	}()
}

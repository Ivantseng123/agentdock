package skill

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

const debounceDuration = 500 * time.Millisecond

func (l *Loader) StartWatcher(configPath string) (stop func(), err error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	dir := filepath.Dir(configPath)
	if err := watcher.Add(dir); err != nil {
		watcher.Close()
		return nil, err
	}

	absPath, err := filepath.Abs(configPath)
	if err != nil {
		watcher.Close()
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	go l.watchLoop(ctx, watcher, absPath)

	return func() {
		cancel()
		watcher.Close()
	}, nil
}

func (l *Loader) watchLoop(ctx context.Context, watcher *fsnotify.Watcher, configPath string) {
	var debounceTimer *time.Timer

	for {
		select {
		case <-ctx.Done():
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			return

		case event, ok := <-watcher.Events:
			if !ok {
				return
			}

			absEvent, _ := filepath.Abs(event.Name)
			if absEvent != configPath {
				continue
			}

			if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}

			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceTimer = time.AfterFunc(debounceDuration, func() {
				if err := l.ReloadConfig(configPath); err != nil {
					slog.Error("skill.config_reload_failed", "path", configPath, "error", err)
				} else {
					slog.Info("skill.config_reloaded", "path", configPath)
				}
			})

		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			slog.Error("skill.watcher_error", "error", err)
		}
	}
}

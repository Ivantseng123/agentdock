package skill

import (
	"context"
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
					l.logger.Error("Skill 設定重新載入失敗", "phase", "失敗", "path", configPath, "error", err)
				} else {
					l.logger.Info("Skill 設定已重新載入", "phase", "完成", "path", configPath)
				}
			})

		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			l.logger.Error("Skill 監視器錯誤", "phase", "失敗", "error", err)
		}
	}
}

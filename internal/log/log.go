package log

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"tars/internal/config"
)

type RotatingWriter struct {
	dir        string
	name       string
	maxSize    int64
	retainDays int
	maxBackups int
	f          *os.File
	size       int64
}

func NewRotatingWriter(dir, name string, maxSizeMB, retainDays, maxBackups int) (*RotatingWriter, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	w := &RotatingWriter{
		dir:        dir,
		name:       name,
		maxSize:    int64(maxSizeMB) * 1024 * 1024,
		retainDays: retainDays,
		maxBackups: maxBackups,
	}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *RotatingWriter) open() error {
	path := filepath.Join(w.dir, w.name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.f = f
	w.size = info.Size()
	return nil
}

func (w *RotatingWriter) Write(p []byte) (int, error) {
	if w.size+int64(len(p)) > w.maxSize {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *RotatingWriter) rotate() error {
	if err := w.f.Close(); err != nil {
		return err
	}
	idx := w.backupIndices()
	for i := len(idx) - 1; i >= 0; i-- {
		old := filepath.Join(w.dir, w.name+"."+strconv.Itoa(idx[i]))
		neu := filepath.Join(w.dir, w.name+"."+strconv.Itoa(idx[i]+1))
		if err := os.Rename(old, neu); err != nil {
			return err
		}
	}
	base := filepath.Join(w.dir, w.name)
	if err := os.Rename(base, base+".1"); err != nil {
		return err
	}
	w.pruneBackups()
	w.prune()
	return w.open()
}

func (w *RotatingWriter) pruneBackups() {
	if w.maxBackups <= 0 {
		return
	}
	idx := w.backupIndices()
	if len(idx) <= w.maxBackups {
		return
	}
	for _, n := range idx[:len(idx)-w.maxBackups] {
		os.Remove(filepath.Join(w.dir, w.name+"."+strconv.Itoa(n)))
	}
}

func (w *RotatingWriter) backupIndices() []int {
	var idx []int
	entries, _ := os.ReadDir(w.dir)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, w.name+".") {
			continue
		}
		suffix := strings.TrimPrefix(name, w.name+".")
		if n, err := strconv.Atoi(suffix); err == nil {
			idx = append(idx, n)
		}
	}
	sort.Ints(idx)
	return idx
}

func (w *RotatingWriter) prune() {
	if w.retainDays <= 0 {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -w.retainDays)
	entries, _ := os.ReadDir(w.dir)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), w.name+".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(w.dir, e.Name()))
		}
	}
}

func (w *RotatingWriter) Close() error {
	if w.f != nil {
		return w.f.Close()
	}
	return nil
}

func New(cfg config.Log) (*slog.Logger, io.Closer, error) {
	level := parseLevel(cfg.Level)
	w, err := NewRotatingWriter(cfg.Dir, "tars.log", cfg.MaxSizeMB, cfg.RetentionDays, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("init rotating writer: %w", err)
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if cfg.JSON {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(h), w, nil
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

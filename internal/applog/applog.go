package applog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	maxFileSize   = 5 << 20 // 5 MB
	maxValueLen   = 200
	truncSuffix   = "…"
)

var (
	mu   sync.Mutex
	file *os.File
)

// Init opens the log file for appending. Call once at startup.
// If the file exceeds 5 MB, it is rotated (renamed to .log.1) before opening.
// Safe to skip — all log calls become no-ops if not initialized.
func Init(dir string) error {
	path := filepath.Join(dir, "tabsordnung.log")

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// Rotate if too large.
	if info, err := os.Stat(path); err == nil && info.Size() > maxFileSize {
		os.Rename(path, path+".1")
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}

	mu.Lock()
	file = f
	mu.Unlock()
	return nil
}

// Close flushes and closes the log file.
func Close() {
	mu.Lock()
	defer mu.Unlock()
	if file != nil {
		file.Close()
		file = nil
	}
}

// Info logs a structured event line.
//
//	applog.Info("ws.connected", "remote", addr)
//	applog.Info("snapshot.created", "rev", 5, "tabs", 42)
func Info(event string, kv ...any) {
	write("INFO", event, nil, kv)
}

// Error logs an event with an error.
//
//	applog.Error("ws.send", err, "action", "close")
func Error(event string, err error, kv ...any) {
	write("ERROR", event, err, kv)
}

func write(level, event string, err error, kv []any) {
	mu.Lock()
	f := file
	mu.Unlock()
	if f == nil {
		return
	}

	var b strings.Builder
	b.WriteString(time.Now().UTC().Format("2006-01-02T15:04:05.000Z"))
	b.WriteByte(' ')
	b.WriteString(level)
	b.WriteByte(' ')
	b.WriteString(event)

	if err != nil {
		b.WriteString(" err=")
		b.WriteString(quote(err.Error()))
	}

	for i := 0; i+1 < len(kv); i += 2 {
		b.WriteByte(' ')
		b.WriteString(fmt.Sprint(kv[i]))
		b.WriteByte('=')
		b.WriteString(quote(fmt.Sprint(kv[i+1])))
	}
	b.WriteByte('\n')

	mu.Lock()
	defer mu.Unlock()
	if file != nil {
		file.WriteString(b.String())
	}
}

func quote(s string) string {
	if len(s) > maxValueLen {
		s = s[:maxValueLen] + truncSuffix
	}
	if strings.ContainsAny(s, " \t\n\"") {
		return "\"" + strings.ReplaceAll(s, "\"", "\\\"") + "\""
	}
	return s
}

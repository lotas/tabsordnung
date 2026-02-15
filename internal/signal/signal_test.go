package signal

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDetectSource(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://mail.google.com/mail/u/0/#inbox", "gmail"},
		{"https://app.slack.com/client/T123/C456", "slack"},
		{"https://my-company.slack.com/", "slack"},
		{"https://app.element.io/#/room/!abc:matrix.org", "matrix"},
		{"https://matrix.example.com/", "matrix"},
		{"https://github.com/foo/bar", ""},
		{"https://example.com", ""},
	}
	for _, tt := range tests {
		got := DetectSource(tt.url)
		if got != tt.want {
			t.Errorf("DetectSource(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestWriteAndReadSignal(t *testing.T) {
	dir := t.TempDir()
	sig := Signal{
		Source:     "gmail",
		CapturedAt: time.Date(2026, 2, 15, 14, 30, 0, 0, time.UTC),
		Items: []SignalItem{
			{Title: "From: Alice", Preview: "Production DB latency spike"},
			{Title: "From: Bob", Preview: "Weekly sync notes"},
		},
	}

	path, err := WriteSignal(dir, sig)
	if err != nil {
		t.Fatal(err)
	}

	if filepath.Dir(path) != filepath.Join(dir, "gmail") {
		t.Errorf("path dir = %q, want gmail subdir", filepath.Dir(path))
	}

	signals, err := ReadSignals(dir, "gmail")
	if err != nil {
		t.Fatal(err)
	}
	if len(signals) != 1 {
		t.Fatalf("got %d signals, want 1", len(signals))
	}
	if len(signals[0].Items) != 2 {
		t.Errorf("got %d items, want 2", len(signals[0].Items))
	}
}

func TestReadSignalsReverseChronological(t *testing.T) {
	dir := t.TempDir()

	sig1 := Signal{
		Source:     "slack",
		CapturedAt: time.Date(2026, 2, 15, 10, 0, 0, 0, time.UTC),
		Items:      []SignalItem{{Title: "first", Preview: "old"}},
	}
	sig2 := Signal{
		Source:     "slack",
		CapturedAt: time.Date(2026, 2, 15, 14, 0, 0, 0, time.UTC),
		Items:      []SignalItem{{Title: "second", Preview: "new"}},
	}

	WriteSignal(dir, sig1)
	WriteSignal(dir, sig2)

	signals, err := ReadSignals(dir, "slack")
	if err != nil {
		t.Fatal(err)
	}
	if len(signals) != 2 {
		t.Fatalf("got %d signals, want 2", len(signals))
	}
	if signals[0].Items[0].Title != "second" {
		t.Errorf("first signal title = %q, want 'second'", signals[0].Items[0].Title)
	}
}

func TestAppendSignalLog(t *testing.T) {
	dir := t.TempDir()
	sig := Signal{
		Source:     "gmail",
		CapturedAt: time.Date(2026, 2, 15, 14, 30, 0, 0, time.UTC),
		Items: []SignalItem{
			{Title: "Alice", Preview: "hello"},
		},
	}

	AppendSignalLog(dir, sig)

	data, err := os.ReadFile(filepath.Join(dir, "signals.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Error("signals.md is empty")
	}
}

func TestWriteSignalDedup(t *testing.T) {
	dir := t.TempDir()
	sig := Signal{
		Source:     "gmail",
		CapturedAt: time.Date(2026, 2, 15, 14, 30, 0, 0, time.UTC),
		Items: []SignalItem{
			{Title: "Alice", Preview: "hello"},
		},
	}

	// First write succeeds
	path1, err := WriteSignal(dir, sig)
	if err != nil {
		t.Fatal(err)
	}
	if path1 == "" {
		t.Fatal("expected file path on first write")
	}

	// Second write with same items returns empty (deduped)
	sig2 := sig
	sig2.CapturedAt = time.Date(2026, 2, 15, 15, 0, 0, 0, time.UTC)
	path2, err := WriteSignal(dir, sig2)
	if err != nil {
		t.Fatal(err)
	}
	if path2 != "" {
		t.Errorf("expected empty path for duplicate, got %q", path2)
	}

	// Only 1 file should exist
	signals, _ := ReadSignals(dir, "gmail")
	if len(signals) != 1 {
		t.Errorf("got %d signals, want 1", len(signals))
	}

	// Third write with different items succeeds
	sig3 := sig
	sig3.CapturedAt = time.Date(2026, 2, 15, 16, 0, 0, 0, time.UTC)
	sig3.Items = []SignalItem{{Title: "Bob", Preview: "world"}}
	path3, err := WriteSignal(dir, sig3)
	if err != nil {
		t.Fatal(err)
	}
	if path3 == "" {
		t.Fatal("expected file path for different signal")
	}
}

func TestParseItemsJSON(t *testing.T) {
	raw := `[{"title":"Alice","preview":"hello"},{"title":"Bob","preview":"world"}]`
	items, err := ParseItemsJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].Title != "Alice" {
		t.Errorf("items[0].Title = %q", items[0].Title)
	}
}

package summarize

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSummaryPath(t *testing.T) {
	tests := []struct {
		name    string
		outDir  string
		rawURL  string
		title   string
		want    string
	}{
		{
			name:   "normal HTTP URL",
			outDir: "/out",
			rawURL: "https://blog.example.de/post/1",
			title:  "My Post",
			want:   filepath.Join("/out", "blog-example-de", "my-post.md"),
		},
		{
			name:   "URL with www prefix",
			outDir: "/tmp/summaries",
			rawURL: "https://www.golang.org/doc/effective_go",
			title:  "Effective Go",
			want:   filepath.Join("/tmp/summaries", "www-golang-org", "effective-go.md"),
		},
		{
			name:   "empty host falls back to unknown",
			outDir: "/out",
			rawURL: "file:///home/user/doc.html",
			title:  "Local File",
			want:   filepath.Join("/out", "unknown", "local-file.md"),
		},
		{
			name:   "non-HTTP scheme with host",
			outDir: "/out",
			rawURL: "ftp://files.example.com/readme.txt",
			title:  "Readme",
			want:   filepath.Join("/out", "files-example-com", "readme.md"),
		},
		{
			name:   "completely invalid URL",
			outDir: "/out",
			rawURL: "://bad",
			title:  "Bad URL",
			want:   filepath.Join("/out", "unknown", "bad-url.md"),
		},
		{
			name:   "empty URL",
			outDir: "/out",
			rawURL: "",
			title:  "No URL",
			want:   filepath.Join("/out", "unknown", "no-url.md"),
		},
		{
			name:   "about: scheme with no host",
			outDir: "/out",
			rawURL: "about:blank",
			title:  "Blank Tab",
			want:   filepath.Join("/out", "unknown", "blank-tab.md"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SummaryPath(tt.outDir, tt.rawURL, tt.title)
			if got != tt.want {
				t.Errorf("SummaryPath(%q, %q, %q) = %q, want %q",
					tt.outDir, tt.rawURL, tt.title, got, tt.want)
			}
		})
	}
}

func TestReadSummary_WithMarker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.md")

	content := "# My Page\n\n**Source:** https://example.com\n**Summarized:** 2025-01-15\n\n## Summary\n\nThis is the summary text.\nIt has multiple lines.\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadSummary(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "This is the summary text.\nIt has multiple lines.\n"
	if got != want {
		t.Errorf("ReadSummary() = %q, want %q", got, want)
	}
}

func TestReadSummary_WithoutMarker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plain.md")

	content := "Just some plain text without the expected marker.\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadSummary(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got != content {
		t.Errorf("ReadSummary() = %q, want %q", got, content)
	}
}

func TestReadSummary_FileNotFound(t *testing.T) {
	_, err := ReadSummary("/nonexistent/path/to/file.md")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

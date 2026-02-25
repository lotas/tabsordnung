package github

import (
	"testing"
)

func TestExtractFromURL(t *testing.T) {
	tests := []struct {
		url  string
		want *EntityRef
	}{
		{"https://github.com/mozilla/gecko-dev/pull/123", &EntityRef{"mozilla", "gecko-dev", 123, "pull"}},
		{"https://github.com/mozilla/gecko-dev/issues/456", &EntityRef{"mozilla", "gecko-dev", 456, "issue"}},
		{"https://github.com/org/repo/pull/1#discussion_r123", &EntityRef{"org", "repo", 1, "pull"}},
		{"https://mail.google.com/inbox", nil},
		{"https://github.com/org/repo", nil},
		{"", nil},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			got := ExtractFromURL(tt.url)
			if tt.want == nil {
				if got != nil {
					t.Errorf("ExtractFromURL(%q) = %+v, want nil", tt.url, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("ExtractFromURL(%q) = nil, want %+v", tt.url, tt.want)
			}
			if got.Owner != tt.want.Owner || got.Repo != tt.want.Repo || got.Number != tt.want.Number || got.Kind != tt.want.Kind {
				t.Errorf("ExtractFromURL(%q) = %+v, want %+v", tt.url, got, tt.want)
			}
		})
	}
}

func TestExtractFromSignalText(t *testing.T) {
	tests := []struct {
		title, preview, snippet string
		want                    *EntityRef
	}{
		{"dependabot", "[mozilla/gecko-dev] Bump lodash (#1234)", "", &EntityRef{"mozilla", "gecko-dev", 1234, ""}},
		{"alice", "Review requested", "https://github.com/org/repo/pull/42", &EntityRef{"org", "repo", 42, "pull"}},
		{"bob", "Hello world", "", nil},
		{"alice", "Re: [nickel-chromium/tabsordnung] Add feature (#7)", "", &EntityRef{"nickel-chromium", "tabsordnung", 7, ""}},
	}

	for _, tt := range tests {
		t.Run(tt.title+"_"+tt.preview, func(t *testing.T) {
			got := ExtractFromSignalText(tt.title, tt.preview, tt.snippet)
			if tt.want == nil {
				if got != nil {
					t.Errorf("ExtractFromSignalText(%q, %q, %q) = %+v, want nil", tt.title, tt.preview, tt.snippet, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("ExtractFromSignalText(%q, %q, %q) = nil, want %+v", tt.title, tt.preview, tt.snippet, tt.want)
			}
			if got.Owner != tt.want.Owner || got.Repo != tt.want.Repo || got.Number != tt.want.Number || got.Kind != tt.want.Kind {
				t.Errorf("ExtractFromSignalText(%q, %q, %q) = %+v, want %+v", tt.title, tt.preview, tt.snippet, got, tt.want)
			}
		})
	}
}

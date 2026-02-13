package analyzer

import (
	"testing"
)

func TestParseGitHubURL(t *testing.T) {
	tests := []struct {
		name   string
		url    string
		want   *githubRef
		wantOK bool
	}{
		{
			name:   "issue URL",
			url:    "https://github.com/golang/go/issues/1234",
			want:   &githubRef{Owner: "golang", Repo: "go", Kind: "issue", Number: 1234},
			wantOK: true,
		},
		{
			name:   "pull request URL",
			url:    "https://github.com/charmbracelet/bubbletea/pull/567",
			want:   &githubRef{Owner: "charmbracelet", Repo: "bubbletea", Kind: "pr", Number: 567},
			wantOK: true,
		},
		{
			name:   "issue URL with fragment",
			url:    "https://github.com/org/repo/issues/42#issuecomment-123",
			want:   &githubRef{Owner: "org", Repo: "repo", Kind: "issue", Number: 42},
			wantOK: true,
		},
		{
			name:   "issue URL with query params",
			url:    "https://github.com/org/repo/issues/10?q=test",
			want:   &githubRef{Owner: "org", Repo: "repo", Kind: "issue", Number: 10},
			wantOK: true,
		},
		{
			name:   "not a GitHub URL",
			url:    "https://google.com/search",
			wantOK: false,
		},
		{
			name:   "GitHub repo page (not issue/PR)",
			url:    "https://github.com/golang/go",
			wantOK: false,
		},
		{
			name:   "GitHub code page",
			url:    "https://github.com/golang/go/blob/main/README.md",
			wantOK: false,
		},
		{
			name:   "empty URL",
			url:    "",
			wantOK: false,
		},
		{
			name:   "about: URL",
			url:    "about:blank",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseGitHubURL(tt.url)
			if tt.wantOK {
				if got == nil {
					t.Fatalf("expected non-nil ref, got nil")
				}
				if got.Owner != tt.want.Owner {
					t.Errorf("owner: got %q, want %q", got.Owner, tt.want.Owner)
				}
				if got.Repo != tt.want.Repo {
					t.Errorf("repo: got %q, want %q", got.Repo, tt.want.Repo)
				}
				if got.Kind != tt.want.Kind {
					t.Errorf("kind: got %q, want %q", got.Kind, tt.want.Kind)
				}
				if got.Number != tt.want.Number {
					t.Errorf("number: got %d, want %d", got.Number, tt.want.Number)
				}
			} else {
				if got != nil {
					t.Fatalf("expected nil ref, got %+v", got)
				}
			}
		})
	}
}

func TestBuildGraphQLQuery(t *testing.T) {
	refs := []*githubRef{
		{Owner: "org", Repo: "repo", Kind: "issue", Number: 42},
		{Owner: "org", Repo: "repo", Kind: "pr", Number: 99},
		{Owner: "other", Repo: "lib", Kind: "issue", Number: 7},
	}

	query, aliasMap := buildGraphQLQuery(refs)

	// Check query contains expected fragments
	if query == "" {
		t.Fatal("query is empty")
	}

	// Should reference both repos
	if !containsAll(query, "org", "repo", "other", "lib") {
		t.Errorf("query missing expected repo references: %s", query)
	}

	// Should have issue/PR queries
	if !containsAll(query, "issue(number: 42)", "pullRequest(number: 99)", "issue(number: 7)") {
		t.Errorf("query missing expected item queries: %s", query)
	}

	// Alias map should have 3 entries
	if len(aliasMap) != 3 {
		t.Errorf("alias map: got %d entries, want 3", len(aliasMap))
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !contains(s, sub) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && searchString(s, sub)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

package classify

import (
	"strings"
	"testing"
)

func TestBuildPrompt(t *testing.T) {
	prompt := BuildPrompt("Alice", "Urgent: server down", "Production API returning 500...", "")
	if prompt == "" {
		t.Fatal("expected non-empty prompt")
	}
	if !strings.Contains(prompt, "Alice") || !strings.Contains(prompt, "server down") {
		t.Error("prompt should contain sender and subject")
	}
}

func TestBuildPromptWithRules(t *testing.T) {
	rules := "Emails from PagerDuty are always urgent."
	prompt := BuildPrompt("PagerDuty", "Alert", "CPU spike", rules)
	if !strings.Contains(prompt, "PagerDuty are always urgent") {
		t.Error("prompt should contain rules")
	}
}

func TestParseUrgency(t *testing.T) {
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{"urgent", "urgent", true},
		{"  review\n", "review", true},
		{"FYI", "fyi", true},
		{"URGENT", "urgent", true},
		{"something else", "", false},
		{"urgent review", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		got, ok := ParseUrgency(tt.input)
		if ok != tt.ok || got != tt.want {
			t.Errorf("ParseUrgency(%q) = %q, %v; want %q, %v", tt.input, got, ok, tt.want, tt.ok)
		}
	}
}

func TestRulesFilePath(t *testing.T) {
	p := RulesFilePath()
	if p == "" {
		t.Fatal("expected non-empty path")
	}
}

func TestClassifyGmailHeuristic(t *testing.T) {
	tests := []struct {
		name    string
		title   string
		preview string
		snippet string
		want    string
		ok      bool
	}{
		{"bot sender", "renovate[bot]", "Update dependency", "", "fyi", true},
		{"noreply sender", "noreply-spamdigest via group", "Spam report", "", "fyi", true},
		{"digest sender", "dev-platform Digest", "Topic summary", "", "fyi", true},
		{"notification sender", "taskcluster-notifications", "Abridged summary", "", "fyi", true},
		{"snyk sender", "Snyk via taskcluster", "Vulnerability alert", "", "fyi", true},
		{"resolved bug", "bugzilla-daemon", "Bug status", "Status NEW RESOLVED Comment", "fyi", true},
		{"fixed bug", "bugzilla-daemon", "Bug update", "Resolution --- FIXED Status", "fyi", true},
		{"human PR comment", "Matt", "feat(generic-worker): support capacity", "", "", false},
		{"human review", "Johan Lorenzo", "Urls pointing to indexes", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ClassifyGmailHeuristic(tt.title, tt.preview, tt.snippet)
			if ok != tt.ok || got != tt.want {
				t.Errorf("ClassifyGmailHeuristic(%q, %q, %q) = %q, %v; want %q, %v",
					tt.title, tt.preview, tt.snippet, got, ok, tt.want, tt.ok)
			}
		})
	}
}

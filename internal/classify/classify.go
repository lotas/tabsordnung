package classify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const promptTemplate = `Classify this email's urgency as exactly one of: urgent, review, fyi

- urgent: production incidents, security breaches, direct requests marked as time-sensitive
- review: a human is commenting on your code, requesting your review, discussing a bug, or asking you something directly
- fyi: marketing emails, wellness newsletters, mailing list posts, announcements, anything not expecting your personal response

Default to fyi unless there is a clear reason to escalate.

Sender: %s
Subject: %s
Snippet: %s
%s
Respond with ONLY one word: urgent, review, fyi`

// senderHeuristicPatterns are case-insensitive substrings in the sender name
// that indicate automated/bulk email → fyi without LLM.
var senderHeuristicPatterns = []string{
	"[bot]",
	"dependa",
	"renovate",
	"noreply",
	"digest",
	"notification",
	"snyk",
}

// ClassifyGmailHeuristic returns "fyi" for automated/bulk Gmail signals
// based on sender name patterns and content keywords, skipping the LLM.
func ClassifyGmailHeuristic(title, preview, snippet string) (string, bool) {
	lower := strings.ToLower(title)
	for _, pat := range senderHeuristicPatterns {
		if strings.Contains(lower, pat) {
			return "fyi", true
		}
	}
	// Bug tracker status changes already resolved
	combined := preview + " " + snippet
	if strings.Contains(combined, "RESOLVED") || strings.Contains(combined, "FIXED") {
		return "fyi", true
	}
	return "", false
}

// BuildPrompt constructs the classification prompt from signal fields and optional rules.
func BuildPrompt(title, preview, snippet, rules string) string {
	rulesSection := ""
	if rules != "" {
		rulesSection = "\n" + rules + "\n"
	}
	return fmt.Sprintf(promptTemplate, title, preview, snippet, rulesSection)
}

// ParseUrgency parses an LLM response into a valid urgency level.
func ParseUrgency(response string) (string, bool) {
	s := strings.TrimSpace(strings.ToLower(response))
	switch s {
	case "urgent", "review", "fyi":
		return s, true
	default:
		return "", false
	}
}

// RulesFilePath returns the path to the urgency rules file.
func RulesFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "tabsordnung", "urgency-rules.txt")
}

// LoadRules reads the rules file, returning empty string if it doesn't exist.
func LoadRules() string {
	data, err := os.ReadFile(RulesFilePath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

type ollamaRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
}

type ollamaResponse struct {
	Response string `json:"response"`
}

// ClassifySignal sends the signal to Ollama for urgency classification.
func ClassifySignal(ctx context.Context, model, host, title, preview, snippet string) (string, error) {
	rules := LoadRules()
	prompt := BuildPrompt(title, preview, snippet, rules)

	reqBody := ollamaRequest{
		Model:  model,
		Prompt: prompt,
		Stream: false,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, host+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama returned HTTP %d", resp.StatusCode)
	}

	var result ollamaResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	urgency, ok := ParseUrgency(result.Response)
	if !ok {
		return "", fmt.Errorf("unexpected LLM response: %q", result.Response)
	}

	return urgency, nil
}

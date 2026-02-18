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

- urgent: requires immediate action, production incidents, time-sensitive requests
- review: needs attention but can wait for next availability window
- fyi: informational, can be ignored for days

Sender: %s
Subject: %s
Snippet: %s
%s
Respond with ONLY one word: urgent, review, fyi`

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

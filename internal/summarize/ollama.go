package summarize

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

const maxTextLen = 8000

const promptTemplate = `Summarize the following article. Provide a concise summary with key points.

---

%s`

type ollamaRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
}

type ollamaResponse struct {
	Response string `json:"response"`
}

// OllamaSummarize sends text to an Ollama instance and returns the summary.
func OllamaSummarize(ctx context.Context, model, host, text string) (string, error) {
	if len(text) > maxTextLen {
		text = text[:maxTextLen]
	}

	reqBody := ollamaRequest{
		Model:  model,
		Prompt: fmt.Sprintf(promptTemplate, text),
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
		return "", fmt.Errorf("decode ollama response: %w", err)
	}

	return result.Response, nil
}

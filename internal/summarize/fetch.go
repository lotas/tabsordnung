package summarize

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	readability "github.com/go-shiori/go-readability"
)

var skipPrefixes = []string{"about:", "moz-extension:", "file:", "chrome:", "resource:", "data:"}

// FetchReadable fetches a URL and extracts readable text content.
// Returns the article title and extracted text.
// Returns an error for non-HTTP URLs or if extraction fails.
func FetchReadable(url string) (title, text string, err error) {
	for _, prefix := range skipPrefixes {
		if strings.HasPrefix(url, prefix) {
			return "", "", fmt.Errorf("skipping non-HTTP URL: %s", url)
		}
	}

	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", "", fmt.Errorf("fetch %s: %w", url, err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("fetch %s: HTTP %d", url, resp.StatusCode)
	}

	article, err := readability.FromReader(resp.Body, nil)
	if err != nil {
		return "", "", fmt.Errorf("extract readable content from %s: %w", url, err)
	}

	return article.Title, article.TextContent, nil
}

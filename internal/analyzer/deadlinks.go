package analyzer

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nickel-chromium/tabsordnung/internal/types"
)

type DeadLinkResult struct {
	TabIndex int
	IsDead   bool
	Reason   string
}

var skipPrefixes = []string{"about:", "moz-extension:", "file:", "chrome:", "resource:", "data:"}

func shouldSkip(url string) bool {
	for _, prefix := range skipPrefixes {
		if strings.HasPrefix(url, prefix) {
			return true
		}
	}
	return false
}

func AnalyzeDeadLinks(tabs []*types.Tab, results chan<- DeadLinkResult) {
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	sem := make(chan struct{}, 10)
	var wg sync.WaitGroup

	for i, tab := range tabs {
		if shouldSkip(tab.URL) {
			continue
		}

		wg.Add(1)
		go func(idx int, t *types.Tab) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			result := DeadLinkResult{TabIndex: idx}

			req, err := http.NewRequest(http.MethodHead, t.URL, nil)
			if err != nil {
				result.IsDead = true
				result.Reason = "invalid URL"
				t.IsDead = true
				t.DeadReason = result.Reason
				results <- result
				return
			}

			resp, err := client.Do(req)
			if err != nil {
				result.IsDead = true
				result.Reason = "unreachable"
				t.IsDead = true
				t.DeadReason = result.Reason
				results <- result
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode == 404 || resp.StatusCode == 410 {
				result.IsDead = true
				result.Reason = fmt.Sprintf("%d", resp.StatusCode)
				t.IsDead = true
				t.DeadReason = result.Reason
			}

			results <- result
		}(i, tab)
	}

	wg.Wait()
}

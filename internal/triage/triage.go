package triage

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/nickel-chromium/tabsordnung/internal/server"
	"github.com/nickel-chromium/tabsordnung/internal/types"
)

// Category represents a triage bucket for GitHub tabs.
type Category string

const (
	CatNeedsAttention Category = "Needs Attention"
	CatOpenPRs        Category = "Open PRs"
	CatOpenIssues     Category = "Open Issues"
	CatClosedMerged   Category = "Closed / Merged"
)

// Move represents a proposed tab-to-category assignment.
type Move struct {
	Tab      *types.Tab
	Category Category
	Reason   string
}

// Result holds the triage classification output.
type Result struct {
	NeedsAttention []*Move
	OpenPRs        []*Move
	OpenIssues     []*Move
	ClosedMerged   []*Move
	Skipped        int
}

var githubURLPattern = regexp.MustCompile(`^https?://github\.com/([^/]+)/([^/]+)/(issues|pull)/(\d+)`)

// parseKind determines "pr" or "issue" from a GitHub URL.
func parseKind(rawURL string) string {
	matches := githubURLPattern.FindStringSubmatch(rawURL)
	if matches == nil {
		return ""
	}
	if matches[3] == "pull" {
		return "pr"
	}
	return "issue"
}

// needsAttention returns true if the tab requires the user's attention.
func needsAttention(tab *types.Tab) bool {
	if tab.GitHubStatus != "open" {
		return false
	}
	info := tab.GitHubTriage
	if info.ReviewRequested {
		return true
	}
	if info.Assigned {
		return true
	}
	// New activity since last access
	if !info.UpdatedAt.IsZero() && !tab.LastAccessed.IsZero() && info.UpdatedAt.After(tab.LastAccessed) {
		return true
	}
	return false
}

// Classify assigns each tab with GitHubTriage info to a triage category.
func Classify(tabs []*types.Tab) *Result {
	r := &Result{}

	for _, tab := range tabs {
		if tab.GitHubTriage == nil {
			r.Skipped++
			continue
		}

		if needsAttention(tab) {
			reason := buildReason(tab)
			r.NeedsAttention = append(r.NeedsAttention, &Move{
				Tab:      tab,
				Category: CatNeedsAttention,
				Reason:   reason,
			})
			continue
		}

		status := tab.GitHubStatus
		if status == "closed" || status == "merged" {
			r.ClosedMerged = append(r.ClosedMerged, &Move{
				Tab:      tab,
				Category: CatClosedMerged,
				Reason:   status,
			})
			continue
		}

		kind := parseKind(tab.URL)
		if kind == "pr" {
			r.OpenPRs = append(r.OpenPRs, &Move{
				Tab:      tab,
				Category: CatOpenPRs,
				Reason:   "open PR",
			})
		} else {
			r.OpenIssues = append(r.OpenIssues, &Move{
				Tab:      tab,
				Category: CatOpenIssues,
				Reason:   "open issue",
			})
		}
	}

	return r
}

// buildReason constructs a human-readable reason for why a tab needs attention.
func buildReason(tab *types.Tab) string {
	var reasons []string
	info := tab.GitHubTriage
	if info.ReviewRequested {
		reasons = append(reasons, "review requested")
	}
	if info.Assigned {
		reasons = append(reasons, "assigned")
	}
	if !info.UpdatedAt.IsZero() && !tab.LastAccessed.IsZero() && info.UpdatedAt.After(tab.LastAccessed) {
		reasons = append(reasons, "new activity")
	}
	return strings.Join(reasons, ", ")
}

// FormatDryRun returns a human-readable summary of proposed triage moves.
func FormatDryRun(r *Result) string {
	var b strings.Builder

	sections := []struct {
		name  string
		moves []*Move
	}{
		{string(CatNeedsAttention), r.NeedsAttention},
		{string(CatOpenPRs), r.OpenPRs},
		{string(CatOpenIssues), r.OpenIssues},
		{string(CatClosedMerged), r.ClosedMerged},
	}

	for _, sec := range sections {
		if len(sec.moves) == 0 {
			continue
		}
		b.WriteString(fmt.Sprintf("\n%s (%d):\n", sec.name, len(sec.moves)))
		for _, m := range sec.moves {
			b.WriteString(fmt.Sprintf("  - %s (%s)\n", m.Tab.Title, m.Reason))
		}
	}

	if r.Skipped > 0 {
		b.WriteString(fmt.Sprintf("\nSkipped: %d non-GitHub tabs\n", r.Skipped))
	}

	return b.String()
}

// Apply executes triage moves via the live mode WebSocket extension.
func Apply(r *Result, port int) error {
	srv := server.New(port)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		mux := http.NewServeMux()
		mux.Handle("/", srv.Handler())
		httpSrv := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", port), Handler: mux}
		go func() {
			<-ctx.Done()
			httpSrv.Close()
		}()
		httpSrv.ListenAndServe()
	}()

	// Wait for extension to connect
	fmt.Println("Waiting for extension connection...")
	timeout := time.After(60 * time.Second)
	for !srv.Connected() {
		select {
		case <-timeout:
			return fmt.Errorf("timed out waiting for extension connection")
		case <-time.After(100 * time.Millisecond):
		}
	}
	fmt.Println("Extension connected.")

	categories := []struct {
		name  Category
		color string
		moves []*Move
	}{
		{CatNeedsAttention, "red", r.NeedsAttention},
		{CatOpenPRs, "blue", r.OpenPRs},
		{CatOpenIssues, "cyan", r.OpenIssues},
		{CatClosedMerged, "grey", r.ClosedMerged},
	}

	for _, cat := range categories {
		if len(cat.moves) == 0 {
			continue
		}

		// Create tab group
		groupID := fmt.Sprintf("triage-%d", time.Now().UnixNano())
		err := srv.Send(server.OutgoingMsg{
			ID:     groupID,
			Action: "createGroup",
			Name:   string(cat.name),
			Color:  cat.color,
		})
		if err != nil {
			return fmt.Errorf("failed to create group %s: %w", cat.name, err)
		}

		// Wait for group creation response
		var createdGroupID int
		respTimeout := time.After(10 * time.Second)
	waitGroup:
		for {
			select {
			case msg := <-srv.Messages():
				if msg.ID == groupID && msg.GroupID != 0 {
					createdGroupID = msg.GroupID
					break waitGroup
				}
			case <-respTimeout:
				return fmt.Errorf("timed out waiting for group creation: %s", cat.name)
			}
		}

		// Collect tab IDs for this category
		var tabIDs []int
		for _, m := range cat.moves {
			if m.Tab.BrowserID != 0 {
				tabIDs = append(tabIDs, m.Tab.BrowserID)
			}
		}

		if len(tabIDs) > 0 {
			moveID := fmt.Sprintf("move-%d", time.Now().UnixNano())
			err = srv.Send(server.OutgoingMsg{
				ID:      moveID,
				Action:  "moveToGroup",
				TabIDs:  tabIDs,
				GroupID: createdGroupID,
			})
			if err != nil {
				return fmt.Errorf("failed to move tabs to %s: %w", cat.name, err)
			}
		}

		fmt.Printf("  %s: %d tabs grouped\n", cat.name, len(cat.moves))
	}

	return nil
}

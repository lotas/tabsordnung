package github

import (
	"net/url"
	"regexp"
	"strconv"
)

// EntityRef identifies a GitHub PR or issue.
type EntityRef struct {
	Owner  string
	Repo   string
	Number int
	Kind   string // "pull", "issue", or "" (unknown, from signal subject)
}

var (
	// Matches https://github.com/owner/repo/pull/123 or /issues/123
	urlPattern = regexp.MustCompile(`https?://github\.com/([^/]+)/([^/]+)/(issues|pull)/(\d+)`)

	// Matches [owner/repo] ... (#123) in email subjects
	subjectPattern = regexp.MustCompile(`\[([a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+)\].*#(\d+)`)
)

// ExtractFromURL extracts a GitHub entity reference from a tab URL like
// https://github.com/mozilla/gecko-dev/pull/123. Returns nil for non-GitHub URLs.
func ExtractFromURL(rawURL string) *EntityRef {
	if rawURL == "" {
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	// Reconstruct a clean URL (scheme + host + path) to strip fragments/query params
	// before matching, so URLs like .../pull/1#discussion_r123 work correctly.
	clean := u.Scheme + "://" + u.Host + u.Path
	matches := urlPattern.FindStringSubmatch(clean)
	if matches == nil {
		return nil
	}
	num, err := strconv.Atoi(matches[4])
	if err != nil {
		return nil
	}
	kind := "issue"
	if matches[3] == "pull" {
		kind = "pull"
	}
	return &EntityRef{
		Owner:  matches[1],
		Repo:   matches[2],
		Number: num,
		Kind:   kind,
	}
}

// ExtractFromSignalText extracts a GitHub entity reference from signal text fields
// (title, preview, snippet). It first checks preview and title for the email subject
// pattern [owner/repo] ... (#123), then falls back to checking snippet and preview
// for raw GitHub URLs. Returns nil if no match is found.
func ExtractFromSignalText(title, preview, snippet string) *EntityRef {
	// First: check preview and title for subject pattern [owner/repo] ... (#123)
	for _, text := range []string{preview, title} {
		if ref := extractFromSubject(text); ref != nil {
			return ref
		}
	}

	// Second: check snippet and preview for raw GitHub URLs
	for _, text := range []string{snippet, preview} {
		if ref := extractURLFromText(text); ref != nil {
			return ref
		}
	}

	return nil
}

// extractFromSubject tries to extract an EntityRef from a subject-style string
// like "[owner/repo] Bump lodash (#123)".
func extractFromSubject(text string) *EntityRef {
	matches := subjectPattern.FindStringSubmatch(text)
	if matches == nil {
		return nil
	}
	// matches[1] = "owner/repo", matches[2] = "123"
	ownerRepo := matches[1]
	num, err := strconv.Atoi(matches[2])
	if err != nil {
		return nil
	}
	// Split owner/repo
	for i, c := range ownerRepo {
		if c == '/' {
			return &EntityRef{
				Owner:  ownerRepo[:i],
				Repo:   ownerRepo[i+1:],
				Number: num,
				Kind:   "", // unknown from subject alone
			}
		}
	}
	return nil
}

// extractURLFromText finds a GitHub URL embedded in arbitrary text and extracts
// an EntityRef from it.
func extractURLFromText(text string) *EntityRef {
	matches := urlPattern.FindStringSubmatch(text)
	if matches == nil {
		return nil
	}
	num, err := strconv.Atoi(matches[4])
	if err != nil {
		return nil
	}
	kind := "issue"
	if matches[3] == "pull" {
		kind = "pull"
	}
	return &EntityRef{
		Owner:  matches[1],
		Repo:   matches[2],
		Number: num,
		Kind:   kind,
	}
}

package analyzer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lotas/tabsordnung/internal/types"
)

var githubURLPattern = regexp.MustCompile(`^https?://github\.com/([^/]+)/([^/]+)/(issues|pull)/(\d+)`)

type githubRef struct {
	Owner  string
	Repo   string
	Kind   string // "issue" or "pr"
	Number int
	Tab    *types.Tab
}

func parseGitHubURL(rawURL string) *githubRef {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	matches := githubURLPattern.FindStringSubmatch(u.Scheme + "://" + u.Host + u.Path)
	if matches == nil {
		return nil
	}
	num, _ := strconv.Atoi(matches[4])
	kind := "issue"
	if matches[3] == "pull" {
		kind = "pr"
	}
	return &githubRef{
		Owner:  matches[1],
		Repo:   matches[2],
		Kind:   kind,
		Number: num,
	}
}

func resolveGitHubToken() string {
	out, err := exec.Command("gh", "auth", "token").Output()
	if err == nil {
		token := strings.TrimSpace(string(out))
		if token != "" {
			return token
		}
	}
	return ""
}

// repoKey returns "owner/repo" as a map key.
func repoKey(owner, repo string) string {
	return owner + "/" + repo
}

// buildGraphQLQuery constructs a batched query grouping items by repo.
// Returns the query string and a mapping from alias to githubRef.
func buildGraphQLQuery(refs []*githubRef) (string, map[string]*githubRef) {
	aliasMap := make(map[string]*githubRef)

	// Group refs by owner/repo
	type repoGroup struct {
		owner string
		repo  string
		refs  []*githubRef
	}
	repoGroups := make(map[string]*repoGroup)
	var repoOrder []string
	for _, ref := range refs {
		key := repoKey(ref.Owner, ref.Repo)
		if _, ok := repoGroups[key]; !ok {
			repoGroups[key] = &repoGroup{owner: ref.Owner, repo: ref.Repo}
			repoOrder = append(repoOrder, key)
		}
		repoGroups[key].refs = append(repoGroups[key].refs, ref)
	}

	var b strings.Builder
	b.WriteString("query {")

	for ri, key := range repoOrder {
		rg := repoGroups[key]
		repoAlias := fmt.Sprintf("r%d", ri)
		b.WriteString(fmt.Sprintf(" %s: repository(owner: %q, name: %q) {", repoAlias, rg.owner, rg.repo))

		for ii, ref := range rg.refs {
			var itemAlias string
			if ref.Kind == "issue" {
				itemAlias = fmt.Sprintf("i%d", ii)
				b.WriteString(fmt.Sprintf(" %s: issue(number: %d) { state }", itemAlias, ref.Number))
			} else {
				itemAlias = fmt.Sprintf("p%d", ii)
				b.WriteString(fmt.Sprintf(" %s: pullRequest(number: %d) { state }", itemAlias, ref.Number))
			}
			aliasMap[repoAlias+"."+itemAlias] = ref
		}

		b.WriteString(" }")
	}

	b.WriteString(" }")
	return b.String(), aliasMap
}

// graphQLResponse is the top-level response shape.
type graphQLResponse struct {
	Data   map[string]json.RawMessage `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

type stateResponse struct {
	State string `json:"state"`
}

// ResolveGitHubToken is an exported wrapper around the unexported resolveGitHubToken.
func ResolveGitHubToken() string {
	return resolveGitHubToken()
}

// ResolveGitHubUser queries the GitHub GraphQL API for the authenticated user's login.
func ResolveGitHubUser(token string) (string, error) {
	body, _ := json.Marshal(map[string]string{"query": "{ viewer { login } }"})
	req, err := http.NewRequest("POST", "https://api.github.com/graphql", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		Data struct {
			Viewer struct {
				Login string `json:"login"`
			} `json:"viewer"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.Data.Viewer.Login == "" {
		return "", fmt.Errorf("empty login in viewer response")
	}
	return result.Data.Viewer.Login, nil
}

// triageItemResponse is the response shape for triage query items.
type triageItemResponse struct {
	State     string `json:"state"`
	UpdatedAt string `json:"updatedAt"`
	Assignees struct {
		Nodes []struct {
			Login string `json:"login"`
		} `json:"nodes"`
	} `json:"assignees"`
	ReviewRequests *struct {
		Nodes []struct {
			RequestedReviewer struct {
				Login string `json:"login"`
			} `json:"requestedReviewer"`
		} `json:"nodes"`
	} `json:"reviewRequests"`
}

// buildTriageGraphQLQuery constructs a batched query with expanded triage fields.
// Returns the query string and a mapping from alias to githubRef.
func buildTriageGraphQLQuery(refs []*githubRef) (string, map[string]*githubRef) {
	aliasMap := make(map[string]*githubRef)

	// Group refs by owner/repo
	type repoGroup struct {
		owner string
		repo  string
		refs  []*githubRef
	}
	repoGroups := make(map[string]*repoGroup)
	var repoOrder []string
	for _, ref := range refs {
		key := repoKey(ref.Owner, ref.Repo)
		if _, ok := repoGroups[key]; !ok {
			repoGroups[key] = &repoGroup{owner: ref.Owner, repo: ref.Repo}
			repoOrder = append(repoOrder, key)
		}
		repoGroups[key].refs = append(repoGroups[key].refs, ref)
	}

	var b strings.Builder
	b.WriteString("query {")

	for ri, key := range repoOrder {
		rg := repoGroups[key]
		repoAlias := fmt.Sprintf("r%d", ri)
		b.WriteString(fmt.Sprintf(" %s: repository(owner: %q, name: %q) {", repoAlias, rg.owner, rg.repo))

		for ii, ref := range rg.refs {
			var itemAlias string
			if ref.Kind == "issue" {
				itemAlias = fmt.Sprintf("i%d", ii)
				b.WriteString(fmt.Sprintf(" %s: issue(number: %d) { state updatedAt assignees(first: 10) { nodes { login } } }", itemAlias, ref.Number))
			} else {
				itemAlias = fmt.Sprintf("p%d", ii)
				b.WriteString(fmt.Sprintf(" %s: pullRequest(number: %d) { state updatedAt assignees(first: 10) { nodes { login } } reviewRequests(first: 100) { nodes { requestedReviewer { ... on User { login } } } } }", itemAlias, ref.Number))
			}
			aliasMap[repoAlias+"."+itemAlias] = ref
		}

		b.WriteString(" }")
	}

	b.WriteString(" }")
	return b.String(), aliasMap
}

// AnalyzeGitHubTriage fetches extended GitHub metadata for triage classification.
// It sets both GitHubStatus and GitHubTriage on matching tabs.
func AnalyzeGitHubTriage(tabs []*types.Tab, username string) {
	var refs []*githubRef
	for _, tab := range tabs {
		ref := parseGitHubURL(tab.URL)
		if ref == nil {
			continue
		}
		ref.Tab = tab
		refs = append(refs, ref)
	}
	if len(refs) == 0 {
		return
	}

	token := resolveGitHubToken()
	if token == "" {
		return
	}

	query, aliasMap := buildTriageGraphQLQuery(refs)

	body, _ := json.Marshal(map[string]string{"query": query})
	req, err := http.NewRequest("POST", "https://api.github.com/graphql", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	var gqlResp graphQLResponse
	if err := json.NewDecoder(resp.Body).Decode(&gqlResp); err != nil {
		return
	}

	lowerUser := strings.ToLower(username)

	for repoAlias, repoRaw := range gqlResp.Data {
		var items map[string]json.RawMessage
		if err := json.Unmarshal(repoRaw, &items); err != nil {
			continue
		}
		for itemAlias, itemRaw := range items {
			fullAlias := repoAlias + "." + itemAlias
			ref, ok := aliasMap[fullAlias]
			if !ok {
				continue
			}
			var tr triageItemResponse
			if err := json.Unmarshal(itemRaw, &tr); err != nil {
				continue
			}

			// Set status (same as AnalyzeGitHub)
			ref.Tab.GitHubStatus = strings.ToLower(tr.State)

			// Build triage info
			info := &types.GitHubTriageInfo{}

			// Parse updatedAt
			if t, err := time.Parse(time.RFC3339, tr.UpdatedAt); err == nil {
				info.UpdatedAt = t
			}

			// Check if user is assigned
			for _, a := range tr.Assignees.Nodes {
				if strings.ToLower(a.Login) == lowerUser {
					info.Assigned = true
					break
				}
			}

			// Check if user has a review request (PRs only)
			if tr.ReviewRequests != nil {
				for _, rr := range tr.ReviewRequests.Nodes {
					if strings.ToLower(rr.RequestedReviewer.Login) == lowerUser {
						info.ReviewRequested = true
						break
					}
				}
			}

			ref.Tab.GitHubTriage = info
		}
	}
}

func AnalyzeGitHub(tabs []*types.Tab) {
	// Collect GitHub refs
	var refs []*githubRef
	for _, tab := range tabs {
		ref := parseGitHubURL(tab.URL)
		if ref == nil {
			continue
		}
		ref.Tab = tab
		refs = append(refs, ref)
	}
	if len(refs) == 0 {
		return
	}

	token := resolveGitHubToken()
	if token == "" {
		return
	}

	query, aliasMap := buildGraphQLQuery(refs)

	body, _ := json.Marshal(map[string]string{"query": query})
	req, err := http.NewRequest("POST", "https://api.github.com/graphql", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	var gqlResp graphQLResponse
	if err := json.NewDecoder(resp.Body).Decode(&gqlResp); err != nil {
		return
	}

	// Parse nested response: data.r0.i0.state, data.r0.p1.state, etc.
	for repoAlias, repoRaw := range gqlResp.Data {
		var items map[string]json.RawMessage
		if err := json.Unmarshal(repoRaw, &items); err != nil {
			continue
		}
		for itemAlias, itemRaw := range items {
			fullAlias := repoAlias + "." + itemAlias
			ref, ok := aliasMap[fullAlias]
			if !ok {
				continue
			}
			var sr stateResponse
			if err := json.Unmarshal(itemRaw, &sr); err != nil {
				continue
			}
			ref.Tab.GitHubStatus = strings.ToLower(sr.State)
		}
	}
}

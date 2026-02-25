package github

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lotas/tabsordnung/internal/applog"
	"github.com/lotas/tabsordnung/internal/storage"
)

const refreshCooldown = 10 * time.Minute

// EntityRefreshResult holds parsed GraphQL response data for a single entity.
type EntityRefreshResult struct {
	State        string   // "OPEN", "CLOSED", "MERGED"
	Title        string
	Author       string
	UpdatedAt    string   // RFC3339
	Assignees    []string
	ReviewStatus string   // "APPROVED", "CHANGES_REQUESTED", "REVIEW_REQUIRED", ""
	ChecksStatus string   // "SUCCESS", "FAILURE", "PENDING", ""
}

// ToStatusUpdate converts an EntityRefreshResult to a storage.GitHubStatusUpdate.
// It normalizes state to lowercase, maps ReviewStatus and ChecksStatus to
// storage-friendly values, and parses UpdatedAt to time.Time.
func (r EntityRefreshResult) ToStatusUpdate() storage.GitHubStatusUpdate {
	update := storage.GitHubStatusUpdate{
		Title:    r.Title,
		State:    strings.ToLower(r.State),
		Author:   r.Author,
		Assignees: strings.Join(r.Assignees, ","),
	}

	// Map ReviewStatus
	if r.ReviewStatus != "" {
		var mapped string
		switch r.ReviewStatus {
		case "APPROVED":
			mapped = "approved"
		case "CHANGES_REQUESTED":
			mapped = "changes_requested"
		case "REVIEW_REQUIRED":
			mapped = "pending"
		default:
			mapped = strings.ToLower(r.ReviewStatus)
		}
		update.ReviewStatus = &mapped
	}

	// Map ChecksStatus
	if r.ChecksStatus != "" {
		var mapped string
		switch r.ChecksStatus {
		case "SUCCESS":
			mapped = "passing"
		case "FAILURE":
			mapped = "failing"
		case "PENDING":
			mapped = "pending"
		default:
			mapped = strings.ToLower(r.ChecksStatus)
		}
		update.ChecksStatus = &mapped
	}

	// Parse UpdatedAt
	if t, err := time.Parse(time.RFC3339, r.UpdatedAt); err == nil {
		update.GHUpdatedAt = &t
	}

	return update
}

// BuildEntityGraphQLQuery constructs a batched GraphQL query grouping entities
// by repo. Returns the query string and a mapping from alias to index in the
// input refs slice.
func BuildEntityGraphQLQuery(refs []EntityRef) (string, map[string]int) {
	aliasMap := make(map[string]int)

	if len(refs) == 0 {
		return "query { }", aliasMap
	}

	// Group refs by owner/repo, preserving order
	type repoGroup struct {
		owner string
		repo  string
		items []struct {
			ref   EntityRef
			index int
		}
	}
	repoGroups := make(map[string]*repoGroup)
	var repoOrder []string

	for i, ref := range refs {
		key := ref.Owner + "/" + ref.Repo
		if _, ok := repoGroups[key]; !ok {
			repoGroups[key] = &repoGroup{owner: ref.Owner, repo: ref.Repo}
			repoOrder = append(repoOrder, key)
		}
		rg := repoGroups[key]
		rg.items = append(rg.items, struct {
			ref   EntityRef
			index int
		}{ref: ref, index: i})
	}

	var b strings.Builder
	b.WriteString("query {")

	for ri, key := range repoOrder {
		rg := repoGroups[key]
		repoAlias := fmt.Sprintf("r%d", ri)
		b.WriteString(fmt.Sprintf(" %s: repository(owner: %q, name: %q) {", repoAlias, rg.owner, rg.repo))

		for ii, item := range rg.items {
			var itemAlias string
			if item.ref.Kind == "issue" {
				itemAlias = fmt.Sprintf("i%d", ii)
				b.WriteString(fmt.Sprintf(" %s: issue(number: %d) { state title author { login } updatedAt assignees(first: 10) { nodes { login } } }", itemAlias, item.ref.Number))
			} else {
				itemAlias = fmt.Sprintf("p%d", ii)
				b.WriteString(fmt.Sprintf(" %s: pullRequest(number: %d) { state title author { login } updatedAt assignees(first: 10) { nodes { login } } reviewDecision statusCheckRollup { state } }", itemAlias, item.ref.Number))
			}
			aliasMap[repoAlias+"."+itemAlias] = item.index
		}

		b.WriteString(" }")
	}

	b.WriteString(" }")
	return b.String(), aliasMap
}

// refreshItemResponse is the response shape for a single issue or PR from GraphQL.
type refreshItemResponse struct {
	State  string `json:"state"`
	Title  string `json:"title"`
	Author *struct {
		Login string `json:"login"`
	} `json:"author"`
	UpdatedAt  string `json:"updatedAt"`
	Assignees  *struct {
		Nodes []struct {
			Login string `json:"login"`
		} `json:"nodes"`
	} `json:"assignees"`
	ReviewDecision     *string `json:"reviewDecision"`
	StatusCheckRollup  *struct {
		State string `json:"state"`
	} `json:"statusCheckRollup"`
}

// refreshGraphQLResponse is the top-level GraphQL response.
type refreshGraphQLResponse struct {
	Data   map[string]json.RawMessage `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// RefreshEntities queries the GitHub GraphQL API to enrich entities with current state.
// It skips entities that were refreshed within the cooldown period (unless force=true).
// Returns nil without error if token is empty (graceful skip).
func RefreshEntities(db *sql.DB, entities []storage.GitHubEntity, token string, force bool) error {
	if token == "" {
		return nil
	}
	if len(entities) == 0 {
		return nil
	}

	// Filter entities by cooldown
	now := time.Now()
	var filtered []storage.GitHubEntity
	var filteredRefs []EntityRef
	for _, e := range entities {
		if !force && e.LastRefreshedAt != nil && now.Sub(*e.LastRefreshedAt) < refreshCooldown {
			continue
		}
		filtered = append(filtered, e)
		filteredRefs = append(filteredRefs, EntityRef{
			Owner:  e.Owner,
			Repo:   e.Repo,
			Number: e.Number,
			Kind:   e.Kind,
		})
	}

	if len(filteredRefs) == 0 {
		return nil
	}

	applog.Info("github.refresh", "count", len(filteredRefs))

	// Build and execute GraphQL query
	query, aliasMap := BuildEntityGraphQLQuery(filteredRefs)

	body, err := json.Marshal(map[string]string{"query": query})
	if err != nil {
		return fmt.Errorf("marshal graphql query: %w", err)
	}

	req, err := http.NewRequest("POST", "https://api.github.com/graphql", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("graphql request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("graphql response status: %d", resp.StatusCode)
	}

	var gqlResp refreshGraphQLResponse
	if err := json.NewDecoder(resp.Body).Decode(&gqlResp); err != nil {
		return fmt.Errorf("decode graphql response: %w", err)
	}

	if len(gqlResp.Errors) > 0 {
		applog.Info("github.refresh.errors", "count", len(gqlResp.Errors), "first", gqlResp.Errors[0].Message)
	}

	// Parse nested response: data.r0.p0, data.r0.i1, etc.
	results := make(map[int]EntityRefreshResult)
	for repoAlias, repoRaw := range gqlResp.Data {
		var items map[string]json.RawMessage
		if err := json.Unmarshal(repoRaw, &items); err != nil {
			continue
		}
		for itemAlias, itemRaw := range items {
			fullAlias := repoAlias + "." + itemAlias
			idx, ok := aliasMap[fullAlias]
			if !ok {
				continue
			}

			var item refreshItemResponse
			if err := json.Unmarshal(itemRaw, &item); err != nil {
				applog.Error("github.refresh.parse", err, "alias", fullAlias)
				continue
			}

			result := EntityRefreshResult{
				State:     item.State,
				Title:     item.Title,
				UpdatedAt: item.UpdatedAt,
			}

			if item.Author != nil {
				result.Author = item.Author.Login
			}

			if item.Assignees != nil {
				for _, a := range item.Assignees.Nodes {
					result.Assignees = append(result.Assignees, a.Login)
				}
			}

			if item.ReviewDecision != nil {
				result.ReviewStatus = *item.ReviewDecision
			}

			if item.StatusCheckRollup != nil {
				result.ChecksStatus = item.StatusCheckRollup.State
			}

			results[idx] = result
		}
	}

	// Apply updates to storage
	for idx, result := range results {
		entity := filtered[idx]
		update := result.ToStatusUpdate()

		// Detect state change and record event
		oldState := entity.State
		newState := update.State
		if oldState != "" && oldState != newState {
			detail := fmt.Sprintf("%s -> %s", oldState, newState)
			if err := storage.RecordGitHubEvent(db, entity.ID, "status_changed", nil, nil, detail); err != nil {
				applog.Error("github.refresh.event", err, "entity", entity.ID)
			}
		}

		if err := storage.UpdateGitHubEntityStatus(db, entity.ID, update); err != nil {
			applog.Error("github.refresh.update", err, "entity", entity.ID)
		}
	}

	applog.Info("github.refresh.done", "updated", len(results), "total", len(filteredRefs))
	return nil
}

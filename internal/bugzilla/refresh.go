package bugzilla

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/lotas/tabsordnung/internal/applog"
	"github.com/lotas/tabsordnung/internal/storage"
)

const refreshCooldown = 10 * time.Minute

// BugRefreshResult holds data parsed from the Bugzilla REST API.
type BugRefreshResult struct {
	Summary, Status, Resolution, AssignedTo string
}

type bugzillaRESTResponse struct {
	Bugs []struct {
		ID         int    `json:"id"`
		Summary    string `json:"summary"`
		Status     string `json:"status"`
		Resolution string `json:"resolution"`
		AssignedTo string `json:"assigned_to"`
	} `json:"bugs"`
	Error   bool   `json:"error"`
	Message string `json:"message"`
}

// fetchBugFromBase is the testable core — base is like "https://bugzilla.mozilla.org".
func fetchBugFromBase(base string, bugID int) (*BugRefreshResult, error) {
	params := url.Values{}
	params.Set("include_fields", "id,summary,status,resolution,assigned_to")
	apiURL := fmt.Sprintf("%s/rest/bug/%d?%s", base, bugID, params.Encode())

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rest request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rest status %d", resp.StatusCode)
	}
	var bzResp bugzillaRESTResponse
	if err := json.NewDecoder(resp.Body).Decode(&bzResp); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if bzResp.Error {
		return nil, fmt.Errorf("bugzilla: %s", bzResp.Message)
	}
	if len(bzResp.Bugs) == 0 {
		return nil, fmt.Errorf("bug %d not found", bugID)
	}

	b := bzResp.Bugs[0]
	return &BugRefreshResult{
		Summary:    b.Summary,
		Status:     b.Status,
		Resolution: b.Resolution,
		AssignedTo: b.AssignedTo,
	}, nil
}

// FetchBug queries a public Bugzilla REST API. No auth required.
func FetchBug(host string, bugID int) (*BugRefreshResult, error) {
	return fetchBugFromBase("https://"+host, bugID)
}

// RefreshEntities enriches entities from the REST API.
// Skips entities refreshed within the cooldown unless force=true.
func RefreshEntities(db *sql.DB, entities []storage.BugzillaEntity, force bool) error {
	now := time.Now()
	for _, e := range entities {
		if !force && e.LastRefreshedAt != nil && now.Sub(*e.LastRefreshedAt) < refreshCooldown {
			continue
		}
		result, err := FetchBug(e.Host, e.BugID)
		if err != nil {
			applog.Error("bugzilla.refresh.fetch", err, "host", e.Host, "bugID", e.BugID)
			continue
		}
		oldStatus := e.Status
		if oldStatus != "" && oldStatus != result.Status {
			detail := oldStatus + " -> " + result.Status
			storage.RecordBugzillaEvent(db, e.ID, "status_changed", nil, nil, detail)
		}
		update := storage.BugzillaStatusUpdate{
			Title:      result.Summary,
			Status:     result.Status,
			Resolution: result.Resolution,
			Assignee:   result.AssignedTo,
		}
		if err := storage.UpdateBugzillaEntityStatus(db, e.ID, update); err != nil {
			applog.Error("bugzilla.refresh.update", err, "entity", e.ID)
		}
	}
	return nil
}

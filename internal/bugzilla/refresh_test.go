package bugzilla

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lotas/tabsordnung/internal/storage"
)

func TestFetchBugFromBase_ParsesCorrectly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("include_fields") == "" {
			t.Error("expected include_fields param")
		}
		json.NewEncoder(w).Encode(map[string]any{
			"bugs": []map[string]any{{
				"id": 12345, "summary": "Memory leak in parser",
				"status": "RESOLVED", "resolution": "FIXED",
				"assigned_to": "dev@example.com",
			}},
		})
	}))
	defer srv.Close()

	result, err := fetchBugFromBase(srv.URL, 12345)
	if err != nil {
		t.Fatalf("fetchBugFromBase: %v", err)
	}
	if result.Summary != "Memory leak in parser" {
		t.Errorf("Summary wrong: %q", result.Summary)
	}
	if result.Status != "RESOLVED" {
		t.Errorf("Status wrong: %q", result.Status)
	}
	if result.Resolution != "FIXED" {
		t.Errorf("Resolution wrong: %q", result.Resolution)
	}
	if result.AssignedTo != "dev@example.com" {
		t.Errorf("AssignedTo wrong: %q", result.AssignedTo)
	}
}

func TestRefreshEntities_SkipsOnCooldown(t *testing.T) {
	recentTime := time.Now().Add(-5 * time.Minute)
	entities := []storage.BugzillaEntity{
		{ID: 1, Host: "bugzilla.mozilla.org", BugID: 1, LastRefreshedAt: &recentTime},
	}
	// If not skipped, this would panic (nil db). Verify no error = skipped.
	if err := RefreshEntities(nil, entities, false); err != nil {
		t.Fatalf("expected skip on cooldown, got: %v", err)
	}
}

package analyzer

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lotas/tabsordnung/internal/types"
)

func TestAnalyzeDeadLinks(t *testing.T) {
	okServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer okServer.Close()

	notFoundServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer notFoundServer.Close()

	goneServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(410)
	}))
	defer goneServer.Close()

	tabs := []*types.Tab{
		{URL: okServer.URL + "/page"},
		{URL: notFoundServer.URL + "/missing"},
		{URL: goneServer.URL + "/gone"},
		{URL: "about:newtab"},
		{URL: "moz-extension://abc/page"},
	}

	results := make(chan DeadLinkResult, len(tabs))
	AnalyzeDeadLinks(tabs, results)
	close(results)

	for r := range results {
		_ = r
	}

	if tabs[0].IsDead {
		t.Error("200 tab should not be dead")
	}
	if !tabs[1].IsDead {
		t.Error("404 tab should be dead")
	}
	if tabs[1].DeadReason != "404" {
		t.Errorf("expected reason '404', got %q", tabs[1].DeadReason)
	}
	if !tabs[2].IsDead {
		t.Error("410 tab should be dead")
	}
	if tabs[3].IsDead {
		t.Error("about: tab should not be checked")
	}
	if tabs[4].IsDead {
		t.Error("moz-extension: tab should not be checked")
	}
}

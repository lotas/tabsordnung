package summarize

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchReadable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<!DOCTYPE html>
<html><head><title>Test Article</title></head>
<body>
<article>
<h1>Test Article</h1>
<p>This is the main content of the article. It has enough text to be considered readable content by the readability algorithm. The quick brown fox jumps over the lazy dog. This paragraph needs to be long enough for readability to pick it up as meaningful content.</p>
<p>Second paragraph with more meaningful content that helps the readability parser understand this is a real article and not just navigation or boilerplate. We need several sentences here to make this work properly.</p>
</article>
</body></html>`))
	}))
	defer srv.Close()

	title, text, err := FetchReadable(srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if title == "" {
		t.Error("expected non-empty title")
	}
	if text == "" {
		t.Error("expected non-empty text")
	}
}

func TestFetchReadable_SkipsNonHTTP(t *testing.T) {
	urls := []string{
		"about:newtab",
		"moz-extension://abc/page",
		"file:///home/user/doc.html",
		"chrome://settings",
		"resource://gre/modules",
		"data:text/html,hello",
	}
	for _, u := range urls {
		_, _, err := FetchReadable(u)
		if err == nil {
			t.Errorf("expected error for %q, got nil", u)
		}
	}
}

func TestFetchReadable_SendsUserAgent(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<!DOCTYPE html><html><head><title>T</title></head><body><p>text</p></body></html>`))
	}))
	defer srv.Close()

	FetchReadable(srv.URL)
	if gotUA == "" || gotUA == "Go-http-client/1.1" {
		t.Errorf("expected browser-like User-Agent, got %q", gotUA)
	}
}

func TestFetchReadable_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	_, _, err := FetchReadable(srv.URL)
	if err == nil {
		t.Error("expected error for 500 response")
	}
}

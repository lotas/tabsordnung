package summarize

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllamaSummarize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/api/generate" {
			t.Errorf("expected /api/generate, got %s", r.URL.Path)
		}

		var req ollamaRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Model != "llama3.2" {
			t.Errorf("expected model llama3.2, got %s", req.Model)
		}
		if req.Stream {
			t.Error("expected stream=false")
		}

		resp := ollamaResponse{Response: "This is a test summary."}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	result, err := OllamaSummarize(context.Background(), "llama3.2", srv.URL, "Some article text here.")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "This is a test summary." {
		t.Errorf("unexpected result: %q", result)
	}
}

func TestOllamaSummarize_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	_, err := OllamaSummarize(context.Background(), "llama3.2", srv.URL, "text")
	if err == nil {
		t.Error("expected error for 500 response")
	}
}

func TestOllamaSummarize_Cancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := OllamaSummarize(ctx, "llama3.2", srv.URL, "text")
	if err == nil {
		t.Error("expected error for cancelled context")
	}
}

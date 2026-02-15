package signal

import (
	"testing"
)

func TestDetectSource(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://mail.google.com/mail/u/0/#inbox", "gmail"},
		{"https://app.slack.com/client/T123/C456", "slack"},
		{"https://my-company.slack.com/", "slack"},
		{"https://app.element.io/#/room/!abc:matrix.org", "matrix"},
		{"https://matrix.example.com/", "matrix"},
		{"https://github.com/foo/bar", ""},
		{"https://example.com", ""},
	}
	for _, tt := range tests {
		got := DetectSource(tt.url)
		if got != tt.want {
			t.Errorf("DetectSource(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestParseItemsJSONWithTimestamp(t *testing.T) {
	raw := `[{"title":"Alice","preview":"hello","timestamp":"2:30 PM"},{"title":"Bob","preview":"world","timestamp":""}]`
	items, err := ParseItemsJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].Timestamp != "2:30 PM" {
		t.Errorf("items[0].Timestamp = %q, want '2:30 PM'", items[0].Timestamp)
	}
	if items[1].Timestamp != "" {
		t.Errorf("items[1].Timestamp = %q, want empty", items[1].Timestamp)
	}
}

func TestParseItemsJSON(t *testing.T) {
	raw := `[{"title":"Alice","preview":"hello"},{"title":"Bob","preview":"world"}]`
	items, err := ParseItemsJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].Title != "Alice" {
		t.Errorf("items[0].Title = %q", items[0].Title)
	}
}

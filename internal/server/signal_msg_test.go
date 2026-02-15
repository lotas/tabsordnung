package server

import (
	"encoding/json"
	"testing"
)

func TestOutgoingMsgSourceField(t *testing.T) {
	msg := OutgoingMsg{
		ID:     "cmd-1",
		Action: "scrape-activity",
		TabID:  123,
		Source: "gmail",
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]interface{}
	json.Unmarshal(data, &parsed)
	if parsed["source"] != "gmail" {
		t.Errorf("source = %v, want gmail", parsed["source"])
	}
}

func TestIncomingMsgItemsField(t *testing.T) {
	raw := `{"id":"cmd-1","ok":true,"items":"[{\"title\":\"Alice\",\"preview\":\"hello\"}]","source":"gmail"}`
	var msg IncomingMsg
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Items != `[{"title":"Alice","preview":"hello"}]` {
		t.Errorf("items = %q", msg.Items)
	}
	if msg.Source != "gmail" {
		t.Errorf("source = %q, want gmail", msg.Source)
	}
}

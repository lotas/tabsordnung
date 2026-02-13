package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

func TestServerAcceptsConnection(t *testing.T) {
	srv := New(0) // port 0 = pick any free port
	msgs := srv.Messages()

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	// Send a snapshot message
	snap := IncomingMsg{Type: "snapshot"}
	data, _ := json.Marshal(snap)
	err = conn.Write(ctx, websocket.MessageText, data)
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case msg := <-msgs:
		if msg.Type != "snapshot" {
			t.Errorf("got type %q, want snapshot", msg.Type)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for message")
	}
}

func TestServerSendsCommand(t *testing.T) {
	srv := New(0)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	// Give server a moment to register the connection
	time.Sleep(50 * time.Millisecond)

	// Send command from server side
	cmd := OutgoingMsg{ID: "cmd-1", Action: "close", TabIDs: []int{42}}
	srv.Send(cmd)

	// Read it on the client side
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got OutgoingMsg
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != "cmd-1" || got.Action != "close" {
		t.Errorf("got %+v, want cmd-1/close", got)
	}
}

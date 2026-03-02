package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/lotas/tabsordnung/internal/applog"
	"nhooyr.io/websocket"
)

// IncomingMsg is a message from the extension to the TUI.
type IncomingMsg struct {
	Type   string          `json:"type"`
	Tab    json.RawMessage `json:"tab,omitempty"`
	Tabs   json.RawMessage `json:"tabs,omitempty"`
	Groups json.RawMessage `json:"groups,omitempty"`
	TabID  int             `json:"tabId,omitempty"`
	Group  json.RawMessage `json:"group,omitempty"`
	// Command response fields
	ID      string `json:"id,omitempty"`
	OK      *bool  `json:"ok,omitempty"`
	Error   string `json:"error,omitempty"`
	GroupID int    `json:"groupId,omitempty"`
	Content   string `json:"content,omitempty"`
	Items     string `json:"items,omitempty"`
	Source    string `json:"source,omitempty"`
	URL       string `json:"url,omitempty"`
	ChannelID string `json:"channelId,omitempty"`
	ThreadTS  string `json:"threadTs,omitempty"`
}

// TabToOpen specifies a tab to create in the browser.
type TabToOpen struct {
	URL    string `json:"url"`
	Pinned bool   `json:"pinned,omitempty"`
}

// SignalPayload is a single signal item sent to the extension popup.
type SignalPayload struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Preview  string `json:"preview,omitempty"`
	Snippet  string `json:"snippet,omitempty"`
	SourceTS string `json:"sourceTs,omitempty"`
	Active   bool   `json:"active"`
}

// TabInfoPayload is the enriched tab info sent to the extension popup.
type TabInfoPayload struct {
	URL          string          `json:"url"`
	Title        string          `json:"title"`
	LastAccessed string          `json:"lastAccessed"`
	StaleDays    int             `json:"staleDays"`
	IsStale      bool            `json:"isStale"`
	IsDead       bool            `json:"isDead"`
	DeadReason   string          `json:"deadReason,omitempty"`
	IsDuplicate  bool            `json:"isDuplicate"`
	GitHubStatus string          `json:"githubStatus,omitempty"`
	Summary      string          `json:"summary,omitempty"`
	Signals      []SignalPayload `json:"signals,omitempty"`
	SignalSource string          `json:"signalSource,omitempty"`
}

// OutgoingMsg is a command from the TUI to the extension.
type OutgoingMsg struct {
	ID      string      `json:"id"`
	Action  string      `json:"action"`
	TabID   int         `json:"tabId,omitempty"`
	TabIDs  []int       `json:"tabIds,omitempty"`
	GroupID int         `json:"groupId,omitempty"`
	Tabs    []TabToOpen `json:"tabs,omitempty"`
	Name    string      `json:"name,omitempty"`
	Color   string      `json:"color,omitempty"`
	Source  string      `json:"source,omitempty"`
	Title   string      `json:"title,omitempty"`
	// Popup response fields
	TabInfo *TabInfoPayload `json:"tabInfo,omitempty"`
	Summary string          `json:"summary,omitempty"`
	Error   string          `json:"error,omitempty"`
	Status  string          `json:"status,omitempty"`
}

// Server manages the WebSocket connection to the extension.
type Server struct {
	port    int
	msgs    chan IncomingMsg
	mu      sync.Mutex
	conn    *websocket.Conn
	connCtx context.Context
}

// New creates a new Server. Port 0 means the caller manages the listener.
func New(port int) *Server {
	return &Server{
		port: port,
		msgs: make(chan IncomingMsg, 64),
	}
}

// Port returns the configured port.
func (s *Server) Port() int {
	return s.port
}

// Messages returns the channel of incoming messages from the extension.
func (s *Server) Messages() <-chan IncomingMsg {
	return s.msgs
}

// Connected reports whether an extension is connected.
func (s *Server) Connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn != nil
}

// Send sends a command to the connected extension.
func (s *Server) Send(msg OutgoingMsg) error {
	s.mu.Lock()
	conn := s.conn
	ctx := s.connCtx
	s.mu.Unlock()

	if conn == nil {
		return nil
	}

	applog.Info("ws.send", "action", msg.Action, "id", msg.ID)
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, data)
}

// Handler returns an http.Handler that accepts WebSocket upgrades.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		})
		if err != nil {
			log.Printf("websocket accept: %v", err)
			applog.Error("ws.accept", err)
			return
		}

		conn.SetReadLimit(16 << 20) // 16 MB — snapshots with many tabs can be large

		ctx := r.Context()
		s.mu.Lock()
		if s.conn != nil {
			applog.Info("ws.replaced")
			s.conn.CloseNow()
		}
		s.conn = conn
		s.connCtx = ctx
		s.mu.Unlock()

		applog.Info("ws.connected", "remote", r.RemoteAddr)

		defer func() {
			s.mu.Lock()
			if s.conn == conn {
				s.conn = nil
				s.connCtx = nil
			}
			s.mu.Unlock()
			conn.CloseNow()
			applog.Info("ws.disconnected")
		}()

		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var msg IncomingMsg
			if err := json.Unmarshal(data, &msg); err != nil {
				applog.Error("ws.parse", err)
				continue
			}
			applog.Info("ws.recv", "type", msg.Type)
			select {
			case s.msgs <- msg:
			default:
			}
		}
	})
}

// ListenAndServe starts the WebSocket server on the configured port.
func (s *Server) ListenAndServe(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.Handle("/", s.Handler())

	addr := fmt.Sprintf("127.0.0.1:%d", s.port)
	applog.Info("server.start", "addr", addr)
	srv := &http.Server{Addr: addr, Handler: mux}

	go func() {
		<-ctx.Done()
		srv.Close()
	}()

	return srv.ListenAndServe()
}

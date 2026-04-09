package tunnel

import (
	"bytes"
	"container-hub/internal/models"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Hub manages reverse-tunnel connections from agents.
type Hub struct {
	mu      sync.RWMutex
	tunnels map[string]*AgentConn // envID -> connection
	onJoin  func(envID string)
	onLeave func(envID string)
}

// NewHub creates a tunnel hub.
func NewHub(onJoin, onLeave func(string)) *Hub {
	return &Hub{
		tunnels: make(map[string]*AgentConn),
		onJoin:  onJoin,
		onLeave: onLeave,
	}
}

// IsOnline returns true if an agent is connected for the given environment.
func (h *Hub) IsOnline(envID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.tunnels[envID]
	return ok
}

// Transport returns an http.RoundTripper that proxies through the agent tunnel.
func (h *Hub) Transport(envID string) http.RoundTripper {
	return &tunnelTransport{hub: h, envID: envID}
}

// HandleConnect is the WebSocket handler agents connect to.
// Expected query params: env_id, env_name, env_type, token (shared secret).
func (h *Hub) HandleConnect(w http.ResponseWriter, r *http.Request) {
	envID := r.URL.Query().Get("env_id")
	envName := r.URL.Query().Get("env_name")
	envType := r.URL.Query().Get("env_type")
	if envID == "" || envName == "" || envType == "" {
		http.Error(w, "env_id, env_name and env_type required", http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("tunnel upgrade error: %v", err)
		return
	}

	ac := &AgentConn{
		conn:    conn,
		pending: make(map[string]chan *models.TunnelResponse),
		envID:   envID,
		envName: envName,
		envType: models.EnvType(envType),
	}

	h.mu.Lock()
	// Close previous connection if exists
	if old, ok := h.tunnels[envID]; ok {
		old.conn.Close()
	}
	h.tunnels[envID] = ac
	h.mu.Unlock()

	log.Printf("agent connected: %s (%s)", envName, envID)
	if h.onJoin != nil {
		h.onJoin(envID)
	}

	// Read loop – receives responses from agent
	ac.readLoop()

	h.mu.Lock()
	if h.tunnels[envID] == ac {
		delete(h.tunnels, envID)
	}
	h.mu.Unlock()

	log.Printf("agent disconnected: %s (%s)", envName, envID)
	if h.onLeave != nil {
		h.onLeave(envID)
	}
}

// get returns the agent connection for an environment.
func (h *Hub) get(envID string) (*AgentConn, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	ac, ok := h.tunnels[envID]
	return ac, ok
}

// AgentConn wraps a WebSocket connection to an agent.
type AgentConn struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[string]chan *models.TunnelResponse
	envID   string
	envName string
	envType models.EnvType
}

func (ac *AgentConn) readLoop() {
	defer ac.conn.Close()
	for {
		_, msg, err := ac.conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("tunnel read error (%s): %v", ac.envID, err)
			}
			return
		}
		var resp models.TunnelResponse
		if err := json.Unmarshal(msg, &resp); err != nil {
			log.Printf("tunnel unmarshal error: %v", err)
			continue
		}
		ac.mu.Lock()
		ch, ok := ac.pending[resp.ID]
		if ok {
			delete(ac.pending, resp.ID)
		}
		ac.mu.Unlock()
		if ok {
			ch <- &resp
		}
	}
}

// RoundTrip sends an HTTP request through the tunnel and waits for a response.
func (ac *AgentConn) RoundTrip(req *models.TunnelRequest, timeout time.Duration) (*models.TunnelResponse, error) {
	ch := make(chan *models.TunnelResponse, 1)
	ac.mu.Lock()
	ac.pending[req.ID] = ch
	ac.mu.Unlock()

	data, _ := json.Marshal(req)
	ac.writeMu.Lock()
	err := ac.conn.WriteMessage(websocket.TextMessage, data)
	ac.writeMu.Unlock()
	if err != nil {
		ac.mu.Lock()
		delete(ac.pending, req.ID)
		ac.mu.Unlock()
		return nil, fmt.Errorf("tunnel write: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		ac.mu.Lock()
		delete(ac.pending, req.ID)
		ac.mu.Unlock()
		return nil, fmt.Errorf("tunnel request timeout")
	}
}

// tunnelTransport implements http.RoundTripper over the WebSocket tunnel.
type tunnelTransport struct {
	hub   *Hub
	envID string
}

func (t *tunnelTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ac, ok := t.hub.get(t.envID)
	if !ok {
		return nil, fmt.Errorf("no tunnel for environment %s", t.envID)
	}

	body, _ := io.ReadAll(req.Body)

	id := newID()
	treq := &models.TunnelRequest{
		ID:      id,
		Method:  req.Method,
		URL:     req.URL.String(),
		Headers: req.Header,
		Body:    body,
	}

	resp, err := ac.RoundTrip(treq, 30*time.Second)
	if err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("agent error: %s", resp.Error)
	}

	httpResp := &http.Response{
		StatusCode: resp.StatusCode,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(resp.Body)),
	}
	for k, v := range resp.Headers {
		httpResp.Header[k] = v
	}
	return httpResp, nil
}

func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

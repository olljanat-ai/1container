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
	tunnels map[string]*AgentConn // clusterID -> connection
	onJoin  func(id, name string, ctype models.ClusterType)
	onLeave func(id string)
}

// NewHub creates a tunnel hub.
func NewHub(onJoin func(string, string, models.ClusterType), onLeave func(string)) *Hub {
	return &Hub{
		tunnels: make(map[string]*AgentConn),
		onJoin:  onJoin,
		onLeave: onLeave,
	}
}

// IsOnline returns true if an agent is connected for the given cluster.
func (h *Hub) IsOnline(clusterID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.tunnels[clusterID]
	return ok
}

// Transport returns an http.RoundTripper that proxies through the agent tunnel.
func (h *Hub) Transport(clusterID string) http.RoundTripper {
	return &tunnelTransport{hub: h, clusterID: clusterID}
}

// HandleConnect is the WebSocket handler agents connect to.
// Query params: cluster_id, cluster_name, cluster_type.
func (h *Hub) HandleConnect(w http.ResponseWriter, r *http.Request) {
	clusterID := r.URL.Query().Get("cluster_id")
	clusterName := r.URL.Query().Get("cluster_name")
	clusterType := r.URL.Query().Get("cluster_type")
	if clusterID == "" || clusterName == "" || clusterType == "" {
		http.Error(w, "cluster_id, cluster_name and cluster_type required", http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("tunnel upgrade error: %v", err)
		return
	}

	ac := &AgentConn{
		conn:        conn,
		pending:     make(map[string]chan *models.TunnelResponse),
		clusterID:   clusterID,
		clusterName: clusterName,
		clusterType: models.ClusterType(clusterType),
	}

	h.mu.Lock()
	if old, ok := h.tunnels[clusterID]; ok {
		old.conn.Close()
	}
	h.tunnels[clusterID] = ac
	h.mu.Unlock()

	log.Printf("agent connected: %s (%s) type=%s", clusterName, clusterID, clusterType)
	if h.onJoin != nil {
		h.onJoin(clusterID, clusterName, models.ClusterType(clusterType))
	}

	ac.readLoop()

	h.mu.Lock()
	if h.tunnels[clusterID] == ac {
		delete(h.tunnels, clusterID)
	}
	h.mu.Unlock()

	log.Printf("agent disconnected: %s (%s)", clusterName, clusterID)
	if h.onLeave != nil {
		h.onLeave(clusterID)
	}
}

func (h *Hub) get(clusterID string) (*AgentConn, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	ac, ok := h.tunnels[clusterID]
	return ac, ok
}

// AgentConn wraps a WebSocket connection to an agent.
type AgentConn struct {
	conn        *websocket.Conn
	writeMu     sync.Mutex
	mu          sync.Mutex
	pending     map[string]chan *models.TunnelResponse
	clusterID   string
	clusterName string
	clusterType models.ClusterType
}

func (ac *AgentConn) readLoop() {
	defer ac.conn.Close()
	for {
		_, msg, err := ac.conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("tunnel read error (%s): %v", ac.clusterID, err)
			}
			ac.mu.Lock()
			for id, ch := range ac.pending {
				close(ch)
				delete(ac.pending, id)
			}
			ac.mu.Unlock()
			return
		}
		var resp models.TunnelResponse
		if err := json.Unmarshal(msg, &resp); err != nil {
			log.Printf("tunnel unmarshal error: %v", err)
			continue
		}
		ac.mu.Lock()
		ch, ok := ac.pending[resp.ID]
		if ok && (!resp.Chunk || resp.Done) {
			delete(ac.pending, resp.ID)
		}
		ac.mu.Unlock()
		if ok {
			ch <- &resp
			if resp.Done || (!resp.Chunk && resp.Error == "") {
				close(ch)
			}
		}
	}
}

func (ac *AgentConn) sendCancel(reqID string) {
	msg := models.TunnelCancel{ID: reqID, Cancel: true}
	data, _ := json.Marshal(msg)
	ac.writeMu.Lock()
	ac.conn.WriteMessage(websocket.TextMessage, data)
	ac.writeMu.Unlock()
}

// RoundTrip sends a request and waits for a single buffered response.
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
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("tunnel connection closed")
		}
		return resp, nil
	case <-ctx.Done():
		ac.mu.Lock()
		delete(ac.pending, req.ID)
		ac.mu.Unlock()
		return nil, fmt.Errorf("tunnel request timeout")
	}
}

// RoundTripStream sends a streaming request and returns a piped reader.
func (ac *AgentConn) RoundTripStream(req *models.TunnelRequest) (io.ReadCloser, int, error) {
	ch := make(chan *models.TunnelResponse, 128)
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
		return nil, 0, fmt.Errorf("tunnel write: %w", err)
	}

	first, ok := <-ch
	if !ok {
		return nil, 0, fmt.Errorf("tunnel connection closed")
	}
	if first.Error != "" {
		return nil, 0, fmt.Errorf("agent error: %s", first.Error)
	}

	pr, pw := io.Pipe()
	reqID := req.ID

	go func() {
		defer pw.Close()
		if len(first.Body) > 0 {
			pw.Write(first.Body)
		}
		if first.Done || !first.Chunk {
			return
		}
		for resp := range ch {
			if resp.Error != "" {
				pw.CloseWithError(fmt.Errorf("agent error: %s", resp.Error))
				return
			}
			if len(resp.Body) > 0 {
				if _, err := pw.Write(resp.Body); err != nil {
					ac.sendCancel(reqID)
					return
				}
			}
			if resp.Done {
				return
			}
		}
	}()

	return pr, first.StatusCode, nil
}

// --- tunnelTransport implements http.RoundTripper ---

type tunnelTransport struct {
	hub       *Hub
	clusterID string
}

func (t *tunnelTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ac, ok := t.hub.get(t.clusterID)
	if !ok {
		return nil, fmt.Errorf("no tunnel for cluster %s", t.clusterID)
	}

	body, _ := io.ReadAll(req.Body)
	urlPath := req.URL.Path
	if req.URL.RawQuery != "" {
		urlPath += "?" + req.URL.RawQuery
	}

	id := newID()
	isStream := req.URL.Query().Get("follow") == "1" ||
		req.URL.Query().Get("follow") == "true"

	treq := &models.TunnelRequest{
		ID:      id,
		Method:  req.Method,
		URL:     urlPath,
		Headers: req.Header,
		Body:    body,
		Stream:  isStream,
	}

	if isStream {
		reader, statusCode, err := ac.RoundTripStream(treq)
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: statusCode,
			Header:     make(http.Header),
			Body:       reader,
		}, nil
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

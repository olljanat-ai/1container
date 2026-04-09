package tunnel

import (
	"container-hub/internal/models"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// AgentClient runs on the remote side, connecting back to the hub.
type AgentClient struct {
	ServerURL     string
	EnvID         string
	EnvName       string
	EnvType       string
	LocalEndpoint string
	AgentToken    string
	SkipTLS       bool
	httpClient    *http.Client
	streamClient  *http.Client // no timeout, for streaming requests

	// Track active streams so we can cancel them
	streamsMu sync.Mutex
	streams   map[string]func() // reqID -> cancel func
}

// NewAgentClient creates a new agent.
func NewAgentClient(serverURL, envID, envName, envType, localEndpoint, agentToken string, skipTLS bool) *AgentClient {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: skipTLS},
	}
	streamTr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: skipTLS},
	}
	return &AgentClient{
		ServerURL:     serverURL,
		EnvID:         envID,
		EnvName:       envName,
		EnvType:       envType,
		LocalEndpoint: strings.TrimRight(localEndpoint, "/"),
		AgentToken:    agentToken,
		SkipTLS:       skipTLS,
		httpClient:    &http.Client{Transport: tr, Timeout: 30 * time.Second},
		streamClient:  &http.Client{Transport: streamTr}, // no timeout for streaming
		streams:       make(map[string]func()),
	}
}

// Run connects to the server and processes requests. Reconnects on failure.
func (a *AgentClient) Run() {
	for {
		if err := a.connect(); err != nil {
			log.Printf("connection error: %v – retrying in 5s", err)
		}
		time.Sleep(5 * time.Second)
	}
}

func (a *AgentClient) connect() error {
	u, err := url.Parse(a.ServerURL)
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("env_id", a.EnvID)
	q.Set("env_name", a.EnvName)
	q.Set("env_type", a.EnvType)
	if a.AgentToken != "" {
		q.Set("token", a.AgentToken)
	}
	u.RawQuery = q.Encode()

	dialer := websocket.Dialer{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: a.SkipTLS},
	}
	conn, _, err := dialer.Dial(u.String(), nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	log.Printf("connected to hub: %s", a.ServerURL)

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		// Try to decode as cancel message first
		var cancel models.TunnelCancel
		if json.Unmarshal(msg, &cancel) == nil && cancel.Cancel {
			a.cancelStream(cancel.ID)
			continue
		}

		var req models.TunnelRequest
		if err := json.Unmarshal(msg, &req); err != nil {
			log.Printf("unmarshal error: %v", err)
			continue
		}

		if req.Stream {
			go a.handleStreamRequest(conn, &req)
		} else {
			go a.handleRequest(conn, &req)
		}
	}
}

func (a *AgentClient) cancelStream(reqID string) {
	a.streamsMu.Lock()
	if cancel, ok := a.streams[reqID]; ok {
		cancel()
		delete(a.streams, reqID)
	}
	a.streamsMu.Unlock()
}

func (a *AgentClient) sendMsg(conn *websocket.Conn, v interface{}) error {
	data, _ := json.Marshal(v)
	return conn.WriteMessage(websocket.TextMessage, data)
}

// handleRequest processes a single request-response.
func (a *AgentClient) handleRequest(conn *websocket.Conn, req *models.TunnelRequest) {
	resp := a.doHTTP(req)
	a.sendMsg(conn, resp)
}

// handleStreamRequest processes a streaming request, sending chunks back.
func (a *AgentClient) handleStreamRequest(conn *websocket.Conn, req *models.TunnelRequest) {
	targetURL := a.LocalEndpoint + req.URL

	var body io.Reader
	if len(req.Body) > 0 {
		body = strings.NewReader(string(req.Body))
	}

	httpReq, err := http.NewRequest(req.Method, targetURL, body)
	if err != nil {
		a.sendMsg(conn, &models.TunnelResponse{ID: req.ID, Error: err.Error()})
		return
	}
	for k, vals := range req.Headers {
		for _, v := range vals {
			httpReq.Header.Add(k, v)
		}
	}

	// Register cancel function
	ctx, cancelFunc := newCancellableContext()
	httpReq = httpReq.WithContext(ctx)

	a.streamsMu.Lock()
	a.streams[req.ID] = cancelFunc
	a.streamsMu.Unlock()

	defer func() {
		cancelFunc()
		a.streamsMu.Lock()
		delete(a.streams, req.ID)
		a.streamsMu.Unlock()
	}()

	httpResp, err := a.streamClient.Do(httpReq)
	if err != nil {
		a.sendMsg(conn, &models.TunnelResponse{ID: req.ID, Error: err.Error()})
		return
	}
	defer httpResp.Body.Close()

	// Send initial response with status code and headers
	headers := make(map[string][]string)
	for k, v := range httpResp.Header {
		headers[k] = v
	}
	a.sendMsg(conn, &models.TunnelResponse{
		ID:         req.ID,
		StatusCode: httpResp.StatusCode,
		Headers:    headers,
		Chunk:      true,
	})

	// Stream body in chunks
	buf := make([]byte, 8192)
	for {
		n, readErr := httpResp.Body.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			if sendErr := a.sendMsg(conn, &models.TunnelResponse{
				ID:    req.ID,
				Body:  chunk,
				Chunk: true,
			}); sendErr != nil {
				return
			}
		}
		if readErr != nil {
			break
		}
	}

	// Signal end
	a.sendMsg(conn, &models.TunnelResponse{ID: req.ID, Done: true})
}

func (a *AgentClient) doHTTP(req *models.TunnelRequest) *models.TunnelResponse {
	targetURL := a.LocalEndpoint + req.URL

	var body io.Reader
	if len(req.Body) > 0 {
		body = strings.NewReader(string(req.Body))
	}

	httpReq, err := http.NewRequest(req.Method, targetURL, body)
	if err != nil {
		return &models.TunnelResponse{ID: req.ID, Error: err.Error()}
	}
	for k, vals := range req.Headers {
		for _, v := range vals {
			httpReq.Header.Add(k, v)
		}
	}

	httpResp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return &models.TunnelResponse{ID: req.ID, Error: err.Error()}
	}
	defer httpResp.Body.Close()

	respBody, _ := io.ReadAll(httpResp.Body)
	headers := make(map[string][]string)
	for k, v := range httpResp.Header {
		headers[k] = v
	}

	return &models.TunnelResponse{
		ID:         req.ID,
		StatusCode: httpResp.StatusCode,
		Headers:    headers,
		Body:       respBody,
	}
}

func newCancellableContext() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}

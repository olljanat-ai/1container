package tunnel

import (
	"bytes"
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

// AgentConfig holds the settings needed to create an AgentClient.
type AgentConfig struct {
	ServerURL     string // ws(s)://hub/ws/tunnel
	ClusterID     string
	ClusterName   string
	ClusterType   string // docker-swarm, kubernetes, nomad
	LocalEndpoint string // http(s)://orchestrator-api
	AuthToken     string // injected into requests to local endpoint
	SkipTLS       bool
}

// AgentClient runs inside the target network, tunnelling back to the hub.
type AgentClient struct {
	AgentConfig
	httpClient   *http.Client
	streamClient *http.Client // no timeout for streaming
	streamsMu    sync.Mutex
	streams      map[string]func()
	writeMu      sync.Mutex // protects concurrent WebSocket writes
}

// NewAgentClient creates a new agent.
func NewAgentClient(cfg AgentConfig) *AgentClient {
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.SkipTLS}}
	streamTr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.SkipTLS}}
	cfg.LocalEndpoint = strings.TrimRight(cfg.LocalEndpoint, "/")
	return &AgentClient{
		AgentConfig:  cfg,
		httpClient:   &http.Client{Transport: tr, Timeout: 30 * time.Second},
		streamClient: &http.Client{Transport: streamTr},
		streams:      make(map[string]func()),
	}
}

// Run connects to the server and processes requests. Reconnects on failure
// with exponential backoff (5s, 10s, 20s, 40s, capped at 60s).
func (a *AgentClient) Run() {
	backoff := 5 * time.Second
	const maxBackoff = 60 * time.Second
	for {
		err := a.connect()
		if err != nil {
			log.Printf("connection error: %v – retrying in %s", err, backoff)
			time.Sleep(backoff)
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		} else {
			// Successful connection that later disconnected; reset backoff
			backoff = 5 * time.Second
			log.Printf("disconnected from hub – reconnecting in %s", backoff)
			time.Sleep(backoff)
		}
	}
}

func (a *AgentClient) connect() error {
	u, err := url.Parse(a.ServerURL)
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("cluster_id", a.ClusterID)
	q.Set("cluster_name", a.ClusterName)
	q.Set("cluster_type", a.ClusterType)
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

// authHeader returns the header name and value to inject based on cluster type.
func (a *AgentClient) authHeader() (string, string) {
	if a.AuthToken == "" {
		return "", ""
	}
	switch a.ClusterType {
	case "nomad":
		return "X-Nomad-Token", a.AuthToken
	default: // docker-swarm, kubernetes
		return "Authorization", "Bearer " + a.AuthToken
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
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return conn.WriteMessage(websocket.TextMessage, data)
}

func (a *AgentClient) handleRequest(conn *websocket.Conn, req *models.TunnelRequest) {
	resp := a.doHTTP(req)
	a.sendMsg(conn, resp)
}

func (a *AgentClient) handleStreamRequest(conn *websocket.Conn, req *models.TunnelRequest) {
	targetURL := a.LocalEndpoint + req.URL
	httpReq, err := http.NewRequest(req.Method, targetURL, nil)
	if err != nil {
		a.sendMsg(conn, &models.TunnelResponse{ID: req.ID, Error: err.Error()})
		return
	}
	for k, vals := range req.Headers {
		for _, v := range vals {
			httpReq.Header.Add(k, v)
		}
	}
	if hdr, val := a.authHeader(); hdr != "" {
		httpReq.Header.Set(hdr, val)
	}

	ctx, cancelFunc := context.WithCancel(context.Background())
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

	headers := make(map[string][]string)
	for k, v := range httpResp.Header {
		headers[k] = v
	}
	a.sendMsg(conn, &models.TunnelResponse{
		ID: req.ID, StatusCode: httpResp.StatusCode, Headers: headers, Chunk: true,
	})

	buf := make([]byte, 8192)
	for {
		n, readErr := httpResp.Body.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			if sendErr := a.sendMsg(conn, &models.TunnelResponse{
				ID: req.ID, Body: chunk, Chunk: true,
			}); sendErr != nil {
				return
			}
		}
		if readErr != nil {
			break
		}
	}
	a.sendMsg(conn, &models.TunnelResponse{ID: req.ID, Done: true})
}

func (a *AgentClient) doHTTP(req *models.TunnelRequest) *models.TunnelResponse {
	targetURL := a.LocalEndpoint + req.URL
	var body io.Reader
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
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
	if hdr, val := a.authHeader(); hdr != "" {
		httpReq.Header.Set(hdr, val)
	}

	httpResp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return &models.TunnelResponse{ID: req.ID, Error: err.Error()}
	}
	defer httpResp.Body.Close()
	const maxResponseBody = 64 << 20 // 64 MB
	respBody, _ := io.ReadAll(io.LimitReader(httpResp.Body, maxResponseBody))
	headers := make(map[string][]string)
	for k, v := range httpResp.Header {
		headers[k] = v
	}
	return &models.TunnelResponse{
		ID: req.ID, StatusCode: httpResp.StatusCode, Headers: headers, Body: respBody,
	}
}

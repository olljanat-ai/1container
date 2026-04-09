package tunnel

import (
	"container-hub/internal/models"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// AgentClient runs on the remote side, connecting back to the hub.
type AgentClient struct {
	ServerURL     string // ws(s)://hub-server/ws/tunnel
	EnvID         string
	EnvName       string
	EnvType       string
	LocalEndpoint string // http(s)://localhost:port – the local orchestrator API
	AgentToken    string // shared secret
	SkipTLS       bool
	httpClient    *http.Client
}

// NewAgentClient creates a new agent.
func NewAgentClient(serverURL, envID, envName, envType, localEndpoint, agentToken string, skipTLS bool) *AgentClient {
	tr := &http.Transport{
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

		var req models.TunnelRequest
		if err := json.Unmarshal(msg, &req); err != nil {
			log.Printf("unmarshal error: %v", err)
			continue
		}

		go a.handleRequest(conn, &req)
	}
}

func (a *AgentClient) handleRequest(conn *websocket.Conn, req *models.TunnelRequest) {
	resp := a.doHTTP(req)
	data, _ := json.Marshal(resp)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		log.Printf("write error: %v", err)
	}
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

package api

import (
	"container-hub/internal/models"
	"container-hub/internal/provider"
	"container-hub/internal/tunnel"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Server is the central API server.
type Server struct {
	mu   sync.RWMutex
	envs map[string]*models.Environment
	hub  *tunnel.Hub
	mux  *http.ServeMux
}

// NewServer creates and wires the API server.
func NewServer(hub *tunnel.Hub) *Server {
	s := &Server{
		envs: make(map[string]*models.Environment),
		hub:  hub,
	}
	mux := http.NewServeMux()

	// Environment CRUD
	mux.HandleFunc("GET /api/environments", s.listEnvironments)
	mux.HandleFunc("POST /api/environments", s.addEnvironment)
	mux.HandleFunc("DELETE /api/environments/{id}", s.removeEnvironment)

	// Containers
	mux.HandleFunc("GET /api/containers", s.listContainers)
	mux.HandleFunc("GET /api/containers/{envID}/{containerID...}", s.inspectOrAction)

	// Exec (HTTP)
	mux.HandleFunc("POST /api/containers/{envID}/{containerID...}/exec", s.execContainer)

	// Logs (HTTP – buffered)
	mux.HandleFunc("GET /api/containers/{envID}/{containerID...}/logs", s.containerLogs)

	// WebSocket: streaming logs
	mux.HandleFunc("/ws/logs/{envID}/{containerID...}", s.wsLogs)

	// WebSocket: interactive shell
	mux.HandleFunc("/ws/shell/{envID}/{containerID...}", s.wsShell)

	// Tunnel endpoint
	mux.HandleFunc("/ws/tunnel", hub.HandleConnect)

	// UI static files
	mux.Handle("/", http.FileServer(http.Dir("ui")))

	s.mux = mux
	return s
}

// RegisterEnvironment adds an environment.
func (s *Server) RegisterEnvironment(env *models.Environment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.envs[env.ID] = env
}

// SetOnline marks a tunnel environment as online/offline.
func (s *Server) SetOnline(envID string, online bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if env, ok := s.envs[envID]; ok {
		env.Online = online
	}
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	return corsMiddleware(s.mux)
}

// --- Environment handlers ---

func (s *Server) listEnvironments(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	list := make([]*models.Environment, 0, len(s.envs))
	for _, env := range s.envs {
		e := *env
		if e.Tunnel {
			e.Online = s.hub.IsOnline(e.ID)
		} else {
			e.Online = true
		}
		e.Token = ""
		e.CACert = ""
		list = append(list, &e)
	}
	s.mu.RUnlock()
	writeJSON(w, 200, list)
}

func (s *Server) addEnvironment(w http.ResponseWriter, r *http.Request) {
	var env models.Environment
	if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if env.Name == "" || env.Type == "" {
		writeErr(w, 400, "name and type required")
		return
	}
	if env.ID == "" {
		env.ID = shortID()
	}
	s.mu.Lock()
	s.envs[env.ID] = &env
	s.mu.Unlock()
	log.Printf("environment registered: %s (%s) type=%s tunnel=%v", env.Name, env.ID, env.Type, env.Tunnel)
	writeJSON(w, 201, env)
}

func (s *Server) removeEnvironment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	_, ok := s.envs[id]
	delete(s.envs, id)
	s.mu.Unlock()
	if !ok {
		writeErr(w, 404, "environment not found")
		return
	}
	writeJSON(w, 200, map[string]string{"status": "deleted"})
}

// --- Container handlers ---

func (s *Server) listContainers(w http.ResponseWriter, r *http.Request) {
	envFilter := r.URL.Query().Get("env")

	s.mu.RLock()
	envsCopy := make([]*models.Environment, 0, len(s.envs))
	for _, env := range s.envs {
		if envFilter != "" && env.ID != envFilter {
			continue
		}
		envsCopy = append(envsCopy, env)
	}
	s.mu.RUnlock()

	type envResult struct {
		containers []models.Container
		err        error
		envName    string
	}

	ch := make(chan envResult, len(envsCopy))
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	for _, env := range envsCopy {
		go func(e *models.Environment) {
			if e.Tunnel && !s.hub.IsOnline(e.ID) {
				ch <- envResult{envName: e.Name, err: fmt.Errorf("agent offline")}
				return
			}
			p := s.providerFor(e)
			containers, err := p.ListContainers(ctx)
			ch <- envResult{containers: containers, err: err, envName: e.Name}
		}(env)
	}

	var all []models.Container
	var errors []string
	for range envsCopy {
		res := <-ch
		if res.err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", res.envName, res.err))
			continue
		}
		all = append(all, res.containers...)
	}

	resp := map[string]interface{}{
		"containers": all,
		"total":      len(all),
	}
	if len(errors) > 0 {
		resp["errors"] = errors
	}
	writeJSON(w, 200, resp)
}

func (s *Server) inspectOrAction(w http.ResponseWriter, r *http.Request) {
	envID := r.PathValue("envID")
	containerID := r.PathValue("containerID")

	if strings.HasSuffix(containerID, "/logs") {
		s.containerLogs(w, r)
		return
	}
	if strings.HasSuffix(containerID, "/exec") {
		s.execContainer(w, r)
		return
	}

	env := s.getEnv(envID)
	if env == nil {
		writeErr(w, 404, "environment not found")
		return
	}

	p := s.providerFor(env)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	detail, err := p.InspectContainer(ctx, containerID)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, detail)
}

func (s *Server) containerLogs(w http.ResponseWriter, r *http.Request) {
	envID := r.PathValue("envID")
	containerID := r.PathValue("containerID")
	containerID = strings.TrimSuffix(containerID, "/logs")

	tail := 200
	if t := r.URL.Query().Get("tail"); t != "" {
		fmt.Sscanf(t, "%d", &tail)
	}

	env := s.getEnv(envID)
	if env == nil {
		writeErr(w, 404, "environment not found")
		return
	}

	p := s.providerFor(env)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	rc, err := p.ContainerLogs(ctx, containerID, tail, false)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(200)
	io.Copy(w, rc)
}

func (s *Server) execContainer(w http.ResponseWriter, r *http.Request) {
	envID := r.PathValue("envID")
	containerID := r.PathValue("containerID")
	containerID = strings.TrimSuffix(containerID, "/exec")

	var er models.ExecRequest
	if err := json.NewDecoder(r.Body).Decode(&er); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	if len(er.Cmd) == 0 {
		writeErr(w, 400, "cmd required")
		return
	}

	env := s.getEnv(envID)
	if env == nil {
		writeErr(w, 404, "environment not found")
		return
	}

	p := s.providerFor(env)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	result, err := p.ExecContainer(ctx, containerID, er.Cmd)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, result)
}

// --- WebSocket: streaming logs ---

func (s *Server) wsLogs(w http.ResponseWriter, r *http.Request) {
	envID := r.PathValue("envID")
	containerID := r.PathValue("containerID")

	tail := 100
	if t := r.URL.Query().Get("tail"); t != "" {
		fmt.Sscanf(t, "%d", &tail)
	}

	env := s.getEnv(envID)
	if env == nil {
		http.Error(w, "environment not found", 404)
		return
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws logs upgrade error: %v", err)
		return
	}
	defer conn.Close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Read loop to detect client disconnect
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				cancel()
				return
			}
		}
	}()

	p := s.providerFor(env)
	rc, err := p.ContainerLogs(ctx, containerID, tail, true)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("Error: "+err.Error()))
		return
	}
	defer rc.Close()

	buf := make([]byte, 4096)
	for {
		n, readErr := rc.Read(buf)
		if n > 0 {
			if writeErr := conn.WriteMessage(websocket.TextMessage, buf[:n]); writeErr != nil {
				return
			}
		}
		if readErr != nil {
			return
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// --- WebSocket: interactive shell ---

func (s *Server) wsShell(w http.ResponseWriter, r *http.Request) {
	envID := r.PathValue("envID")
	containerID := r.PathValue("containerID")

	env := s.getEnv(envID)
	if env == nil {
		http.Error(w, "environment not found", 404)
		return
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws shell upgrade error: %v", err)
		return
	}
	defer conn.Close()

	p := s.providerFor(env)

	// Send welcome message
	conn.WriteJSON(map[string]string{
		"type":   "output",
		"output": fmt.Sprintf("Connected to %s (%s)\nType commands below.\n", env.Name, containerID),
	})

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}

		var req struct {
			Cmd string `json:"cmd"`
		}
		if err := json.Unmarshal(msg, &req); err != nil || req.Cmd == "" {
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		result, execErr := p.ExecContainer(ctx, containerID, []string{"sh", "-c", req.Cmd})
		cancel()

		resp := map[string]interface{}{
			"type": "output",
		}
		if execErr != nil {
			resp["output"] = "Error: " + execErr.Error()
			resp["exit_code"] = -1
		} else {
			resp["output"] = result.Output
			resp["exit_code"] = result.ExitCode
		}

		if err := conn.WriteJSON(resp); err != nil {
			return
		}
	}
}

// --- Helpers ---

func (s *Server) getEnv(id string) *models.Environment {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.envs[id]
}

func (s *Server) providerFor(env *models.Environment) provider.Provider {
	var transport http.RoundTripper
	if env.Tunnel {
		transport = s.hub.Transport(env.ID)
	}
	return provider.New(env, transport)
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func shortID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

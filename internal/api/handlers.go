package api

import (
	"container-hub/internal/auth"
	"container-hub/internal/models"
	"container-hub/internal/provider"
	"container-hub/internal/tunnel"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type contextKey string

const userContextKey contextKey = "user"

// Server is the central API server.
type Server struct {
	mu          sync.RWMutex
	clusters    map[string]*models.Cluster
	envs        map[string]*models.Environment
	hub         *tunnel.Hub
	mux         *http.ServeMux
	auth        *auth.Manager
	agentSecret string // shared secret for agent tunnel authentication
}

// NewServer creates and wires the API server.
func NewServer(hub *tunnel.Hub, authMgr *auth.Manager, agentSecret string) *Server {
	s := &Server{
		clusters:    make(map[string]*models.Cluster),
		envs:        make(map[string]*models.Environment),
		hub:         hub,
		auth:        authMgr,
		agentSecret: agentSecret,
	}
	mux := http.NewServeMux()

	// Public endpoints (no auth required)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.logout)
	mux.HandleFunc("GET /api/auth/check", s.authCheck)
	mux.HandleFunc("GET /healthz", s.healthCheck)

	// Clusters (read-only, auto-registered by agents)
	mux.HandleFunc("GET /api/clusters", s.requireAuth(s.listClusters))

	// Environments (CRUD)
	mux.HandleFunc("GET /api/environments", s.requireAuth(s.listEnvironments))
	mux.HandleFunc("POST /api/environments", s.requireAuth(s.addEnvironment))
	mux.HandleFunc("DELETE /api/environments/{id}", s.requireAuth(s.removeEnvironment))

	// Containers
	mux.HandleFunc("GET /api/containers", s.requireAuth(s.listContainers))
	mux.HandleFunc("GET /api/containers/{envID}/{containerID...}", s.requireAuth(s.inspectOrAction))
	mux.HandleFunc("POST /api/containers/{envID}/{containerID...}", s.requireAuth(s.execContainer))

	// WebSocket endpoints (auth checked inside)
	mux.HandleFunc("/ws/logs/{envID}/{containerID...}", s.wsLogs)
	mux.HandleFunc("/ws/shell/{envID}/{containerID...}", s.wsShell)
	mux.HandleFunc("/ws/tunnel", s.requireAgentAuth(hub.HandleConnect))

	// UI
	mux.Handle("/", http.FileServer(http.Dir("ui")))

	s.mux = mux
	return s
}

// ClusterJoined is called by the hub when an agent connects.
func (s *Server) ClusterJoined(id, name string, ctype models.ClusterType) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clusters[id] = &models.Cluster{ID: id, Name: name, Type: ctype, Online: true}
	log.Printf("cluster registered: %s (%s) type=%s", name, id, ctype)
}

// ClusterLeft is called by the hub when an agent disconnects.
func (s *Server) ClusterLeft(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.clusters[id]; ok {
		c.Online = false
	}
}

// RegisterEnvironment adds a pre-configured environment.
func (s *Server) RegisterEnvironment(env *models.Environment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.envs[env.ID] = env
}

// RemoveDiscoveredEnvironment removes a discovered environment by ID.
func (s *Server) RemoveDiscoveredEnvironment(envID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Only remove auto-discovered environments (prefixed with "auto-")
	if strings.HasPrefix(envID, "auto-") {
		delete(s.envs, envID)
	}
}

// GetClusters returns a copy of all known clusters.
func (s *Server) GetClusters() []*models.Cluster {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := make([]*models.Cluster, 0, len(s.clusters))
	for _, c := range s.clusters {
		cc := *c
		list = append(list, &cc)
	}
	return list
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	return corsMiddleware(s.mux)
}

// --- Auth middleware ---------------------------------------------------------

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := s.auth.UserFromRequest(r)
		if err != nil {
			writeErr(w, 401, "unauthorized")
			return
		}
		ctx := context.WithValue(r.Context(), userContextKey, user)
		next(w, r.WithContext(ctx))
	}
}

func (s *Server) requireAgentAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.agentSecret != "" {
			token := r.URL.Query().Get("secret")
			if subtle.ConstantTimeCompare([]byte(token), []byte(s.agentSecret)) != 1 {
				http.Error(w, "unauthorized: invalid agent secret", 401)
				return
			}
		}
		next(w, r)
	}
}

func userFromContext(ctx context.Context) *auth.User {
	u, _ := ctx.Value(userContextKey).(*auth.User)
	return u
}

// --- Auth endpoints ---------------------------------------------------------

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	token, err := s.auth.Authenticate(req.Username, req.Password)
	if err != nil {
		writeErr(w, 401, "invalid credentials")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "token",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   86400,
	})
	user, _ := s.auth.ValidateToken(token)
	isAdmin := user != nil && user.Admin
	writeJSON(w, 200, map[string]interface{}{"token": token, "username": req.Username, "admin": isAdmin})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "token",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
	writeJSON(w, 200, map[string]string{"status": "logged out"})
}

func (s *Server) authCheck(w http.ResponseWriter, r *http.Request) {
	user, err := s.auth.UserFromRequest(r)
	if err != nil {
		writeErr(w, 401, "unauthorized")
		return
	}
	writeJSON(w, 200, map[string]interface{}{
		"username": user.Username,
		"admin":    user.Admin,
	})
}

// --- Health check -----------------------------------------------------------

func (s *Server) healthCheck(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	totalClusters := len(s.clusters)
	onlineClusters := 0
	for _, c := range s.clusters {
		if s.hub.IsOnline(c.ID) {
			onlineClusters++
		}
	}
	totalEnvs := len(s.envs)
	s.mu.RUnlock()

	writeJSON(w, 200, map[string]interface{}{
		"status":           "ok",
		"clusters_total":   totalClusters,
		"clusters_online":  onlineClusters,
		"environments":     totalEnvs,
	})
}

// --- Cluster handlers --------------------------------------------------------

func (s *Server) listClusters(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	list := make([]*models.Cluster, 0, len(s.clusters))
	for _, c := range s.clusters {
		cc := *c
		cc.Online = s.hub.IsOnline(c.ID)
		list = append(list, &cc)
	}
	s.mu.RUnlock()
	writeJSON(w, 200, list)
}

// --- Environment handlers ----------------------------------------------------

func (s *Server) listEnvironments(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())

	s.mu.RLock()
	list := make([]*models.Environment, 0, len(s.envs))
	for _, env := range s.envs {
		if !s.auth.CanAccessEnvironment(user, env.Name) {
			continue
		}
		e := *env
		if c, ok := s.clusters[e.ClusterID]; ok {
			e.ClusterName = c.Name
			e.ClusterType = c.Type
			e.Online = s.hub.IsOnline(c.ID)
		}
		list = append(list, &e)
	}
	s.mu.RUnlock()
	writeJSON(w, 200, list)
}

func (s *Server) addEnvironment(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB
	user := userFromContext(r.Context())
	if !user.Admin {
		writeErr(w, 403, "admin access required")
		return
	}

	var env models.Environment
	if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if env.Name == "" || env.ClusterID == "" {
		writeErr(w, 400, "name and cluster_id required")
		return
	}

	s.mu.RLock()
	cluster, ok := s.clusters[env.ClusterID]
	s.mu.RUnlock()
	if !ok {
		writeErr(w, 400, "cluster not found: "+env.ClusterID)
		return
	}

	if cluster.Type == models.ClusterDockerSwarm {
		env.Namespace = ""
	}

	if env.ID == "" {
		env.ID = shortID()
	}

	s.mu.Lock()
	s.envs[env.ID] = &env
	s.mu.Unlock()

	env.ClusterName = cluster.Name
	env.ClusterType = cluster.Type
	log.Printf("environment created: %s → cluster=%s namespace=%q", env.Name, env.ClusterID, env.Namespace)
	writeJSON(w, 201, env)
}

func (s *Server) removeEnvironment(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if !user.Admin {
		writeErr(w, 403, "admin access required")
		return
	}

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

// --- Container handlers ------------------------------------------------------

func (s *Server) listContainers(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	envFilter := r.URL.Query().Get("env")

	s.mu.RLock()
	envsCopy := make([]*models.Environment, 0)
	for _, env := range s.envs {
		if envFilter != "" && env.ID != envFilter {
			continue
		}
		if !s.auth.CanAccessEnvironment(user, env.Name) {
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
			if !s.hub.IsOnline(e.ClusterID) {
				ch <- envResult{envName: e.Name, err: fmt.Errorf("agent offline")}
				return
			}
			p := s.providerFor(e)
			if p == nil {
				ch <- envResult{envName: e.Name, err: fmt.Errorf("cluster not found")}
				return
			}
			containers, err := p.ListContainers(ctx)
			ch <- envResult{containers: containers, err: err, envName: e.Name}
		}(env)
	}

	all := make([]models.Container, 0)
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

	user := userFromContext(r.Context())
	if !s.auth.CanAccessEnvironment(user, env.Name) {
		writeErr(w, 403, "access denied")
		return
	}

	p := s.providerFor(env)
	if p == nil {
		writeErr(w, 502, "cluster not available")
		return
	}

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
	containerID := strings.TrimSuffix(r.PathValue("containerID"), "/logs")

	tail := parseTail(r.URL.Query().Get("tail"), 200)

	env := s.getEnv(envID)
	if env == nil {
		writeErr(w, 404, "environment not found")
		return
	}

	user := userFromContext(r.Context())
	if !s.auth.CanAccessEnvironment(user, env.Name) {
		writeErr(w, 403, "access denied")
		return
	}

	p := s.providerFor(env)
	if p == nil {
		writeErr(w, 502, "cluster not available")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	rc, err := p.ContainerLogs(ctx, containerID, tail, false)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(200)
	io.Copy(w, rc)
}

func (s *Server) execContainer(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB
	envID := r.PathValue("envID")
	containerID := strings.TrimSuffix(r.PathValue("containerID"), "/exec")

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

	user := userFromContext(r.Context())
	if !s.auth.CanAccessEnvironment(user, env.Name) {
		writeErr(w, 403, "access denied")
		return
	}

	p := s.providerFor(env)
	if p == nil {
		writeErr(w, 502, "cluster not available")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	result, err := p.ExecContainer(ctx, containerID, er.Cmd)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, result)
}

// --- WebSocket: streaming logs -----------------------------------------------

func (s *Server) wsLogs(w http.ResponseWriter, r *http.Request) {
	// Auth check for WebSocket
	user, err := s.auth.UserFromRequest(r)
	if err != nil {
		// Try query parameter token for WebSocket
		if t := r.URL.Query().Get("token"); t != "" {
			user, err = s.auth.ValidateToken(t)
		}
		if err != nil {
			http.Error(w, "unauthorized", 401)
			return
		}
	}

	envID := r.PathValue("envID")
	containerID := r.PathValue("containerID")

	tail := parseTail(r.URL.Query().Get("tail"), 100)

	env := s.getEnv(envID)
	if env == nil {
		http.Error(w, "environment not found", 404)
		return
	}
	if !s.auth.CanAccessEnvironment(user, env.Name) {
		http.Error(w, "access denied", 403)
		return
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				cancel()
				return
			}
		}
	}()

	p := s.providerFor(env)
	if p == nil {
		conn.WriteMessage(websocket.TextMessage, []byte("Error: cluster not available"))
		return
	}

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
		if readErr != nil || ctx.Err() != nil {
			return
		}
	}
}

// --- WebSocket: interactive shell --------------------------------------------

func (s *Server) wsShell(w http.ResponseWriter, r *http.Request) {
	// Auth check for WebSocket
	user, err := s.auth.UserFromRequest(r)
	if err != nil {
		if t := r.URL.Query().Get("token"); t != "" {
			user, err = s.auth.ValidateToken(t)
		}
		if err != nil {
			http.Error(w, "unauthorized", 401)
			return
		}
	}

	envID := r.PathValue("envID")
	containerID := r.PathValue("containerID")

	env := s.getEnv(envID)
	if env == nil {
		http.Error(w, "environment not found", 404)
		return
	}
	if !s.auth.CanAccessEnvironment(user, env.Name) {
		http.Error(w, "access denied", 403)
		return
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	p := s.providerFor(env)
	if p == nil {
		conn.WriteJSON(map[string]string{"type": "output", "output": "Error: cluster not available"})
		return
	}

	conn.WriteJSON(map[string]string{
		"type":   "output",
		"output": fmt.Sprintf("Connected to %s [%s]\nType commands below.\n", env.Name, containerID),
	})

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var req struct {
			Cmd string `json:"cmd"`
		}
		if json.Unmarshal(msg, &req) != nil || req.Cmd == "" {
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		result, execErr := p.ExecContainer(ctx, containerID, []string{"sh", "-c", req.Cmd})
		cancel()

		resp := map[string]interface{}{"type": "output"}
		if execErr != nil {
			resp["output"] = "Error: " + execErr.Error()
			resp["exit_code"] = -1
		} else {
			resp["output"] = result.Output
			resp["exit_code"] = result.ExitCode
		}
		if conn.WriteJSON(resp) != nil {
			return
		}
	}
}

// --- Helpers -----------------------------------------------------------------

func (s *Server) getEnv(id string) *models.Environment {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.envs[id]
}

func (s *Server) providerFor(env *models.Environment) provider.Provider {
	s.mu.RLock()
	cluster, ok := s.clusters[env.ClusterID]
	s.mu.RUnlock()
	if !ok {
		return nil
	}

	transport := s.hub.Transport(env.ClusterID)
	cfg := provider.Config{
		ClusterType: cluster.Type,
		Namespace:   env.Namespace,
		EnvID:       env.ID,
		EnvName:     env.Name,
	}
	client := &http.Client{Transport: transport}
	return provider.New(cfg, client)
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Security headers
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")

		// CORS
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		}
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

// parseTail parses a tail line count from a query parameter, clamping to [1, 10000].
func parseTail(s string, defaultVal int) int {
	if s == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return defaultVal
	}
	if n > 10000 {
		return 10000
	}
	return n
}

func shortID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

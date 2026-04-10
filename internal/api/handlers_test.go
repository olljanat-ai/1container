package api

import (
	"container-hub/internal/auth"
	"container-hub/internal/config"
	"container-hub/internal/tunnel"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestServer() *Server {
	hash := "$2a$10$q9skWaD54hzRWB2p1gCt.O2OLfqZ3gAznsmWihXiZsbwSimUrP2We" // "admin"
	cfg := &config.Config{
		JWTSecret: "test-secret",
		Users: []config.UserConfig{
			{Username: "admin", Password: hash, Admin: true},
		},
	}
	authMgr := auth.NewManager(cfg)
	hub := tunnel.NewHub(nil, nil)
	return NewServer(hub, authMgr, "my-agent-secret")
}

func TestRequireAgentAuthValid(t *testing.T) {
	s := newTestServer()
	called := false
	handler := s.requireAgentAuth(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	})

	req := httptest.NewRequest("GET", "/ws/tunnel?secret=my-agent-secret", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if !called {
		t.Error("handler was not called with valid secret")
	}
	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestRequireAgentAuthInvalid(t *testing.T) {
	s := newTestServer()
	called := false
	handler := s.requireAgentAuth(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	req := httptest.NewRequest("GET", "/ws/tunnel?secret=wrong-secret", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if called {
		t.Error("handler should not be called with invalid secret")
	}
	if w.Code != 401 {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestRequireAgentAuthMissing(t *testing.T) {
	s := newTestServer()
	called := false
	handler := s.requireAgentAuth(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	req := httptest.NewRequest("GET", "/ws/tunnel", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if called {
		t.Error("handler should not be called with missing secret")
	}
	if w.Code != 401 {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestRequireAgentAuthNoSecretConfigured(t *testing.T) {
	hash := "$2a$10$q9skWaD54hzRWB2p1gCt.O2OLfqZ3gAznsmWihXiZsbwSimUrP2We"
	cfg := &config.Config{
		JWTSecret: "test-secret",
		Users:     []config.UserConfig{{Username: "admin", Password: hash, Admin: true}},
	}
	authMgr := auth.NewManager(cfg)
	hub := tunnel.NewHub(nil, nil)
	s := NewServer(hub, authMgr, "") // no agent secret

	called := false
	handler := s.requireAgentAuth(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	})

	req := httptest.NewRequest("GET", "/ws/tunnel", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if !called {
		t.Error("handler should be called when no agent secret is configured")
	}
}

func TestCorsMiddleware(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	handler := corsMiddleware(inner)

	// Test OPTIONS preflight
	req := httptest.NewRequest("OPTIONS", "/api/test", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != 204 {
		t.Errorf("OPTIONS status = %d, want 204", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Errorf("ACAO = %q, want %q", got, "http://localhost:3000")
	}

	// Test normal request with origin
	req2 := httptest.NewRequest("GET", "/api/test", nil)
	req2.Header.Set("Origin", "http://example.com")
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)

	if w2.Code != 200 {
		t.Errorf("GET status = %d, want 200", w2.Code)
	}
	if got := w2.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("credentials = %q, want %q", got, "true")
	}
}

func TestWriteJSON(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, 200, map[string]string{"key": "value"})

	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/json")
	}
	if w.Body.String() == "" {
		t.Error("body is empty")
	}
}

func TestWriteErr(t *testing.T) {
	w := httptest.NewRecorder()
	writeErr(w, 404, "not found")

	if w.Code != 404 {
		t.Errorf("status = %d, want 404", w.Code)
	}
	body := w.Body.String()
	if body == "" || !contains(body, "not found") {
		t.Errorf("body = %q, want to contain 'not found'", body)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsImpl(s, substr))
}

func containsImpl(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

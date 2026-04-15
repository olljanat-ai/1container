package tunnel

import (
	"container-hub/internal/audit"
	"container-hub/internal/models"
	"net/http"
	"testing"
)

func testAuditLogger() *audit.Logger {
	l, _ := audit.New(false, "")
	return l
}

func TestNewHub(t *testing.T) {
	joinCalled := false
	leaveCalled := false
	hub := NewHub(
		func(id, name string, ctype models.ClusterType) { joinCalled = true },
		func(id string) { leaveCalled = true },
		testAuditLogger(),
	)
	if hub == nil {
		t.Fatal("NewHub returned nil")
	}
	if hub.tunnels == nil {
		t.Error("tunnels map is nil")
	}
	// Callbacks not called until agents connect
	if joinCalled || leaveCalled {
		t.Error("callbacks should not be called on construction")
	}
}

func TestNewHubNilCallbacks(t *testing.T) {
	hub := NewHub(nil, nil, testAuditLogger())
	if hub == nil {
		t.Fatal("NewHub(nil, nil, testAuditLogger()) returned nil")
	}
}

func TestIsOnlineNoAgents(t *testing.T) {
	hub := NewHub(nil, nil, testAuditLogger())
	if hub.IsOnline("nonexistent") {
		t.Error("IsOnline should be false for nonexistent cluster")
	}
}

func TestTransportReturnsRoundTripper(t *testing.T) {
	hub := NewHub(nil, nil, testAuditLogger())
	transport := hub.Transport("cluster-1")
	if transport == nil {
		t.Fatal("Transport returned nil")
	}
}

func TestTransportRoundTripOfflineCluster(t *testing.T) {
	hub := NewHub(nil, nil, testAuditLogger())
	transport := hub.Transport("offline-cluster")

	// RoundTrip should fail because no agent is connected
	req, _ := http.NewRequest("GET", "http://api/test", nil)
	_, err := transport.RoundTrip(req)
	if err == nil {
		t.Fatal("RoundTrip should fail for offline cluster")
	}
}

func TestNewIDUniqueness(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id := newID()
		if seen[id] {
			t.Fatalf("duplicate ID generated: %s", id)
		}
		seen[id] = true
	}
}

func TestNewIDLength(t *testing.T) {
	id := newID()
	// 16 bytes hex-encoded = 32 characters
	if len(id) != 32 {
		t.Errorf("ID length = %d, want 32", len(id))
	}
}

func TestHubGet(t *testing.T) {
	hub := NewHub(nil, nil, testAuditLogger())

	// No agents connected
	_, ok := hub.get("nonexistent")
	if ok {
		t.Error("get should return false for nonexistent cluster")
	}
}

func TestNewAgentClient(t *testing.T) {
	cfg := AgentConfig{
		ServerURL:     "ws://localhost:8080/ws/tunnel",
		ClusterID:     "test-cluster",
		ClusterName:   "Test Cluster",
		ClusterType:   "kubernetes",
		LocalEndpoint: "https://k8s.local:6443/",
		AuthToken:     "my-token",
		SkipTLS:       true,
	}
	agent := NewAgentClient(cfg)
	if agent == nil {
		t.Fatal("NewAgentClient returned nil")
	}
	// Trailing slash should be trimmed from LocalEndpoint
	if agent.LocalEndpoint != "https://k8s.local:6443" {
		t.Errorf("LocalEndpoint = %q, want trailing slash trimmed", agent.LocalEndpoint)
	}
	if agent.httpClient == nil {
		t.Error("httpClient is nil")
	}
	if agent.streamClient == nil {
		t.Error("streamClient is nil")
	}
}

func TestAgentAuthHeader(t *testing.T) {
	tests := []struct {
		clusterType string
		authToken   string
		wantHeader  string
		wantValue   string
	}{
		{"kubernetes", "k8s-token", "Authorization", "Bearer k8s-token"},
		{"docker-swarm", "docker-token", "Authorization", "Bearer docker-token"},
		{"nomad", "nomad-token", "X-Nomad-Token", "nomad-token"},
		{"kubernetes", "", "", ""},
	}

	for _, tt := range tests {
		agent := NewAgentClient(AgentConfig{
			ClusterType:   tt.clusterType,
			AuthToken:     tt.authToken,
			LocalEndpoint: "http://localhost",
		})
		header, value := agent.authHeader()
		if header != tt.wantHeader || value != tt.wantValue {
			t.Errorf("authHeader(%s, %q) = (%q, %q), want (%q, %q)",
				tt.clusterType, tt.authToken, header, value, tt.wantHeader, tt.wantValue)
		}
	}
}


package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadValidConfig(t *testing.T) {
	yaml := `
listen_addr: ":9090"
jwt_secret: "test-secret-key"
agent_secret: "agent-key"
users:
  - username: alice
    password: "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"
    admin: true
    groups: ["ops"]
  - username: bob
    password: "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"
    groups: ["dev"]
groups:
  - name: ops
    environments: ["*"]
  - name: dev
    environments: ["Staging/*"]
environments:
  - id: env1
    name: "Production"
    cluster_id: k8s-prod
    namespace: default
`
	path := writeTempFile(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.ListenAddr != ":9090" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":9090")
	}
	if cfg.JWTSecret != "test-secret-key" {
		t.Errorf("JWTSecret = %q, want %q", cfg.JWTSecret, "test-secret-key")
	}
	if cfg.AgentSecret != "agent-key" {
		t.Errorf("AgentSecret = %q, want %q", cfg.AgentSecret, "agent-key")
	}
	if len(cfg.Users) != 2 {
		t.Errorf("len(Users) = %d, want 2", len(cfg.Users))
	}
	if len(cfg.Groups) != 2 {
		t.Errorf("len(Groups) = %d, want 2", len(cfg.Groups))
	}
	if len(cfg.Environments) != 1 {
		t.Errorf("len(Environments) = %d, want 1", len(cfg.Environments))
	}
	if cfg.Users[0].Username != "alice" || !cfg.Users[0].Admin {
		t.Errorf("Users[0] = %+v, want alice/admin", cfg.Users[0])
	}
}

func TestLoadDefaultListenAddr(t *testing.T) {
	yaml := `jwt_secret: "secret"`
	path := writeTempFile(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.ListenAddr != ":8080" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":8080")
	}
}

func TestLoadAutoGeneratesJWTSecret(t *testing.T) {
	yaml := `listen_addr: ":8080"`
	path := writeTempFile(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.JWTSecret == "" {
		t.Error("JWTSecret should be auto-generated, got empty")
	}
	if len(cfg.JWTSecret) < 16 {
		t.Errorf("JWTSecret too short: %d chars", len(cfg.JWTSecret))
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("Load() should fail for missing file")
	}
	if !strings.Contains(err.Error(), "read config") {
		t.Errorf("error = %v, want 'read config' prefix", err)
	}
}

func TestLoadInvalidYAML(t *testing.T) {
	path := writeTempFile(t, `{invalid yaml: [`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() should fail for invalid YAML")
	}
	if !strings.Contains(err.Error(), "parse config") {
		t.Errorf("error = %v, want 'parse config' prefix", err)
	}
}

func TestGenerateRandomSecret(t *testing.T) {
	s1 := GenerateRandomSecret()
	s2 := GenerateRandomSecret()

	if s1 == "" || s2 == "" {
		t.Fatal("GenerateRandomSecret returned empty string")
	}
	if s1 == s2 {
		t.Error("GenerateRandomSecret returned same value twice")
	}
	// 32 bytes hex-encoded = 64 characters
	if len(s1) != 64 {
		t.Errorf("secret length = %d, want 64", len(s1))
	}
}

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}

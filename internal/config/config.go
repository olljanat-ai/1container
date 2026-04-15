package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration loaded from config.yaml.
type Config struct {
	ListenAddr   string              `yaml:"listen_addr"`
	JWTSecret    string              `yaml:"jwt_secret"`
	AgentSecret  string              `yaml:"agent_secret"`
	Users        []UserConfig        `yaml:"users"`
	Groups       []GroupConfig       `yaml:"groups"`
	Environments []EnvironmentConfig `yaml:"environments"`
	Audit        AuditConfig         `yaml:"audit"`
}

// AuditConfig controls audit logging for security-sensitive operations.
type AuditConfig struct {
	Enabled bool   `yaml:"enabled"`
	LogFile string `yaml:"log_file"` // empty = stdout (same as app logs), or path to separate file
}

// UserConfig defines a user with hashed password and group memberships.
type UserConfig struct {
	Username string   `yaml:"username"`
	Password string   `yaml:"password"` // bcrypt hash
	Groups   []string `yaml:"groups"`
	Admin    bool     `yaml:"admin"`
}

// GroupConfig defines a group with environment assignments.
type GroupConfig struct {
	Name         string   `yaml:"name"`
	Environments []string `yaml:"environments"` // environment name patterns (supports "*" wildcard)
}

// EnvironmentConfig defines a manually configured environment.
type EnvironmentConfig struct {
	ID        string `yaml:"id"`
	Name      string `yaml:"name"`
	ClusterID string `yaml:"cluster_id"`
	Namespace string `yaml:"namespace"`
}

// Load reads and parses a YAML config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8080"
	}
	if cfg.JWTSecret == "" {
		cfg.JWTSecret = GenerateRandomSecret()
		log.Printf("WARNING: No jwt_secret configured, using auto-generated secret. Tokens will not survive restarts.")
	} else if strings.Contains(cfg.JWTSecret, "change-me") {
		log.Printf("WARNING: jwt_secret appears to be a placeholder. Set a strong random secret for production use.")
	}
	return &cfg, nil
}

// GenerateRandomSecret returns a cryptographically random hex-encoded secret.
func GenerateRandomSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "fallback-" + fmt.Sprintf("%d", os.Getpid())
	}
	return hex.EncodeToString(b)
}

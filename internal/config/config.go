package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration loaded from config.yaml.
type Config struct {
	ListenAddr string            `yaml:"listen_addr"`
	JWTSecret  string            `yaml:"jwt_secret"`
	Clusters   []ClusterConfig   `yaml:"clusters"`
	Users      []UserConfig      `yaml:"users"`
	Groups     []GroupConfig     `yaml:"groups"`
	Environments []EnvironmentConfig `yaml:"environments"`
}

// ClusterConfig defines a cluster the server connects to via agents.
type ClusterConfig struct {
	ID   string `yaml:"id"`
	Name string `yaml:"name"`
	Type string `yaml:"type"` // kubernetes, nomad, docker-swarm
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
		cfg.JWTSecret = "change-me-in-production"
	}
	return &cfg, nil
}

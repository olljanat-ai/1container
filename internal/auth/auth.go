package auth

import (
	"container-hub/internal/config"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// User represents an authenticated user with resolved group/environment access.
type User struct {
	Username     string
	Admin        bool
	Groups       []string
	Environments map[string]bool // set of allowed environment names/patterns
}

// Manager handles user authentication and authorization.
type Manager struct {
	mu     sync.RWMutex
	users  map[string]*userEntry // username -> entry
	groups map[string]*groupEntry
	secret []byte
}

type userEntry struct {
	passwordHash string
	admin        bool
	groups       []string
}

type groupEntry struct {
	environments []string
}

// NewManager creates an auth manager from config.
func NewManager(cfg *config.Config) *Manager {
	m := &Manager{
		users:  make(map[string]*userEntry),
		groups: make(map[string]*groupEntry),
		secret: []byte(cfg.JWTSecret),
	}
	for _, u := range cfg.Users {
		m.users[u.Username] = &userEntry{
			passwordHash: u.Password,
			admin:        u.Admin,
			groups:       u.Groups,
		}
	}
	for _, g := range cfg.Groups {
		m.groups[g.Name] = &groupEntry{
			environments: g.Environments,
		}
	}
	return m
}

// Authenticate verifies username/password and returns a JWT token.
func (m *Manager) Authenticate(username, password string) (string, error) {
	m.mu.RLock()
	entry, ok := m.users[username]
	m.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("invalid credentials")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(entry.passwordHash), []byte(password)); err != nil {
		return "", fmt.Errorf("invalid credentials")
	}
	return m.createToken(username, entry.admin)
}

// ValidateToken parses a JWT token and returns the user info.
func (m *Manager) ValidateToken(tokenStr string) (*User, error) {
	parts := strings.Split(tokenStr, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid token format")
	}

	// Verify signature
	message := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("invalid token signature encoding")
	}
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(message))
	expected := mac.Sum(nil)
	if !hmac.Equal(sig, expected) {
		return nil, fmt.Errorf("invalid token signature")
	}

	// Decode payload
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid token payload")
	}
	var claims struct {
		Sub   string `json:"sub"`
		Admin bool   `json:"admin"`
		Exp   int64  `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("invalid token claims")
	}
	if time.Now().Unix() > claims.Exp {
		return nil, fmt.Errorf("token expired")
	}

	m.mu.RLock()
	entry, ok := m.users[claims.Sub]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("user not found")
	}

	user := &User{
		Username:     claims.Sub,
		Admin:        claims.Admin,
		Groups:       entry.groups,
		Environments: make(map[string]bool),
	}

	// Resolve environment access from groups
	m.mu.RLock()
	for _, gName := range entry.groups {
		if g, ok := m.groups[gName]; ok {
			for _, envPattern := range g.environments {
				user.Environments[envPattern] = true
			}
		}
	}
	m.mu.RUnlock()

	return user, nil
}

// UserFromRequest extracts the authenticated user from an HTTP request.
func (m *Manager) UserFromRequest(r *http.Request) (*User, error) {
	// Check Authorization header
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return m.ValidateToken(strings.TrimPrefix(auth, "Bearer "))
	}
	// Check cookie
	cookie, err := r.Cookie("token")
	if err == nil && cookie.Value != "" {
		return m.ValidateToken(cookie.Value)
	}
	return nil, fmt.Errorf("no authentication token")
}

// CanAccessEnvironment checks if a user can access a given environment name.
func (m *Manager) CanAccessEnvironment(user *User, envName string) bool {
	if user.Admin {
		return true
	}
	for pattern := range user.Environments {
		if matchPattern(pattern, envName) {
			return true
		}
	}
	return false
}

// matchPattern supports simple wildcard matching with "*".
func matchPattern(pattern, name string) bool {
	if pattern == "*" {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return pattern == name
	}
	// Simple prefix/suffix wildcard
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(name, strings.TrimSuffix(pattern, "*"))
	}
	if strings.HasPrefix(pattern, "*") {
		return strings.HasSuffix(name, strings.TrimPrefix(pattern, "*"))
	}
	// Contains pattern: *middle*
	parts := strings.SplitN(pattern, "*", 2)
	return strings.HasPrefix(name, parts[0]) && strings.HasSuffix(name, parts[1])
}

// createToken generates a simple HMAC-SHA256 JWT.
func (m *Manager) createToken(username string, admin bool) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claims := map[string]interface{}{
		"sub":   username,
		"admin": admin,
		"exp":   time.Now().Add(24 * time.Hour).Unix(),
		"iat":   time.Now().Unix(),
	}
	claimsJSON, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(claimsJSON)

	message := header + "." + payload
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(message))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	return message + "." + sig, nil
}

// HashPassword generates a bcrypt hash for a plaintext password.
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

package auth

import (
	"container-hub/internal/config"
	"net/http"
	"testing"
)

func newTestManager() *Manager {
	// password "admin" bcrypt hash
	hash := "$2a$10$q9skWaD54hzRWB2p1gCt.O2OLfqZ3gAznsmWihXiZsbwSimUrP2We"
	cfg := &config.Config{
		JWTSecret: "test-secret-for-jwt",
		Users: []config.UserConfig{
			{Username: "admin", Password: hash, Admin: true, Groups: []string{"ops"}},
			{Username: "viewer", Password: hash, Admin: false, Groups: []string{"dev"}},
		},
		Groups: []config.GroupConfig{
			{Name: "ops", Environments: []string{"*"}},
			{Name: "dev", Environments: []string{"Staging/*", "Dev K8s"}},
		},
	}
	return NewManager(cfg)
}

func TestAuthenticateSuccess(t *testing.T) {
	m := newTestManager()
	token, err := m.Authenticate("admin", "admin")
	if err != nil {
		t.Fatalf("Authenticate() error: %v", err)
	}
	if token == "" {
		t.Fatal("Authenticate() returned empty token")
	}
}

func TestAuthenticateWrongPassword(t *testing.T) {
	m := newTestManager()
	_, err := m.Authenticate("admin", "wrong-password")
	if err == nil {
		t.Fatal("Authenticate() should fail with wrong password")
	}
}

func TestAuthenticateUnknownUser(t *testing.T) {
	m := newTestManager()
	_, err := m.Authenticate("nonexistent", "admin")
	if err == nil {
		t.Fatal("Authenticate() should fail with unknown user")
	}
}

func TestValidateToken(t *testing.T) {
	m := newTestManager()
	token, err := m.Authenticate("admin", "admin")
	if err != nil {
		t.Fatalf("Authenticate() error: %v", err)
	}

	user, err := m.ValidateToken(token)
	if err != nil {
		t.Fatalf("ValidateToken() error: %v", err)
	}
	if user.Username != "admin" {
		t.Errorf("Username = %q, want %q", user.Username, "admin")
	}
	if !user.Admin {
		t.Error("Admin = false, want true")
	}
}

func TestValidateTokenInvalidFormat(t *testing.T) {
	m := newTestManager()
	_, err := m.ValidateToken("not-a-jwt")
	if err == nil {
		t.Fatal("ValidateToken() should fail with invalid format")
	}
}

func TestValidateTokenBadSignature(t *testing.T) {
	m := newTestManager()
	token, err := m.Authenticate("admin", "admin")
	if err != nil {
		t.Fatalf("Authenticate() error: %v", err)
	}

	// Tamper with the signature (replace last few chars)
	runes := []rune(token)
	for i := len(runes) - 4; i < len(runes); i++ {
		runes[i] = 'X'
	}
	tampered := string(runes)
	_, err = m.ValidateToken(tampered)
	if err == nil {
		t.Fatal("ValidateToken() should fail with bad signature")
	}
}

func TestValidateTokenResolvesGroups(t *testing.T) {
	m := newTestManager()
	token, _ := m.Authenticate("viewer", "admin")
	user, err := m.ValidateToken(token)
	if err != nil {
		t.Fatalf("ValidateToken() error: %v", err)
	}

	if user.Admin {
		t.Error("viewer should not be admin")
	}
	if len(user.Environments) != 2 {
		t.Errorf("len(Environments) = %d, want 2", len(user.Environments))
	}
	if !user.Environments["Staging/*"] {
		t.Error("missing 'Staging/*' in Environments")
	}
	if !user.Environments["Dev K8s"] {
		t.Error("missing 'Dev K8s' in Environments")
	}
}

func TestCanAccessEnvironmentAdmin(t *testing.T) {
	m := newTestManager()
	adminUser := &User{Username: "admin", Admin: true}
	if !m.CanAccessEnvironment(adminUser, "anything") {
		t.Error("admin should access any environment")
	}
}

func TestCanAccessEnvironmentNonAdmin(t *testing.T) {
	m := newTestManager()
	user := &User{
		Username:     "viewer",
		Admin:        false,
		Environments: map[string]bool{"Staging/*": true, "Dev K8s": true},
	}

	tests := []struct {
		envName string
		want    bool
	}{
		{"Staging/default", true},
		{"Staging/monitoring", true},
		{"Dev K8s", true},
		{"Production/default", false},
		{"Random", false},
	}

	for _, tt := range tests {
		got := m.CanAccessEnvironment(user, tt.envName)
		if got != tt.want {
			t.Errorf("CanAccessEnvironment(%q) = %v, want %v", tt.envName, got, tt.want)
		}
	}
}

func TestMatchPattern(t *testing.T) {
	tests := []struct {
		pattern string
		name    string
		want    bool
	}{
		{"*", "anything", true},
		{"*", "", true},
		{"exact", "exact", true},
		{"exact", "other", false},
		{"Staging/*", "Staging/default", true},
		{"Staging/*", "Staging/", true},
		{"Staging/*", "Production/default", false},
		{"*/default", "Staging/default", true},
		{"*/default", "Production/default", true},
		{"*/default", "Production/monitoring", false},
		{"Prod*K8s", "Production K8s", true},
		{"Prod*K8s", "Prod K8s", true},
		{"Prod*K8s", "Prod Nomad", false},
	}

	for _, tt := range tests {
		got := matchPattern(tt.pattern, tt.name)
		if got != tt.want {
			t.Errorf("matchPattern(%q, %q) = %v, want %v", tt.pattern, tt.name, got, tt.want)
		}
	}
}

func TestUserFromRequestBearer(t *testing.T) {
	m := newTestManager()
	token, _ := m.Authenticate("admin", "admin")

	req, _ := http.NewRequest("GET", "/api/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	user, err := m.UserFromRequest(req)
	if err != nil {
		t.Fatalf("UserFromRequest() error: %v", err)
	}
	if user.Username != "admin" {
		t.Errorf("Username = %q, want %q", user.Username, "admin")
	}
}

func TestUserFromRequestCookie(t *testing.T) {
	m := newTestManager()
	token, _ := m.Authenticate("admin", "admin")

	req, _ := http.NewRequest("GET", "/api/test", nil)
	req.AddCookie(&http.Cookie{Name: "token", Value: token})

	user, err := m.UserFromRequest(req)
	if err != nil {
		t.Fatalf("UserFromRequest() error: %v", err)
	}
	if user.Username != "admin" {
		t.Errorf("Username = %q, want %q", user.Username, "admin")
	}
}

func TestUserFromRequestNoAuth(t *testing.T) {
	m := newTestManager()
	req, _ := http.NewRequest("GET", "/api/test", nil)

	_, err := m.UserFromRequest(req)
	if err == nil {
		t.Fatal("UserFromRequest() should fail with no auth")
	}
}

func TestHashPassword(t *testing.T) {
	hash, err := HashPassword("mypassword")
	if err != nil {
		t.Fatalf("HashPassword() error: %v", err)
	}
	if hash == "" {
		t.Fatal("HashPassword() returned empty hash")
	}
	if hash == "mypassword" {
		t.Fatal("HashPassword() returned plaintext")
	}
}

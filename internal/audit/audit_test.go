package audit

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestLoggerDisabled(t *testing.T) {
	l, err := New(false, "")
	if err != nil {
		t.Fatalf("New(false, \"\") error: %v", err)
	}
	defer l.Close()
	// Should not panic when disabled.
	l.Log(EventAuthLoginSuccess, SeverityInfo, "alice", "10.0.0.1", nil)
}

func TestLoggerStdout(t *testing.T) {
	// Capture stdout by writing to a temp file and redirecting.
	l, err := New(true, "")
	if err != nil {
		t.Fatalf("New(true, \"\") error: %v", err)
	}
	defer l.Close()
	// Just verify it doesn't panic; actual stdout output is hard to capture in test.
	l.Log(EventAuthLoginSuccess, SeverityInfo, "alice", "10.0.0.1", nil)
}

func TestLoggerToFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")

	l, err := New(true, path)
	if err != nil {
		t.Fatalf("New(true, %q) error: %v", path, err)
	}

	l.Log(EventAuthLoginSuccess, SeverityInfo, "alice", "10.0.0.1", nil)
	l.Log(EventAuthLoginFailure, SeverityWarn, "bob", "10.0.0.2", map[string]interface{}{
		"reason": "invalid credentials",
	})
	l.Log(EventContainerExec, SeverityWarn, "alice", "10.0.0.1", map[string]interface{}{
		"env_id":       "prod-1",
		"container_id": "abc123",
		"command":      []string{"sh", "-c", "ls"},
	})
	l.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}

	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if len(lines) != 3 {
		t.Fatalf("expected 3 log lines, got %d", len(lines))
	}

	// Verify first entry structure.
	var entry Entry
	if err := json.Unmarshal(lines[0], &entry); err != nil {
		t.Fatalf("Unmarshal line 0: %v", err)
	}
	if entry.Logger != "audit" {
		t.Errorf("Logger = %q, want %q", entry.Logger, "audit")
	}
	if entry.Event != EventAuthLoginSuccess {
		t.Errorf("Event = %q, want %q", entry.Event, EventAuthLoginSuccess)
	}
	if entry.Severity != SeverityInfo {
		t.Errorf("Severity = %q, want %q", entry.Severity, SeverityInfo)
	}
	if entry.Actor != "alice" {
		t.Errorf("Actor = %q, want %q", entry.Actor, "alice")
	}
	if entry.SourceIP != "10.0.0.1" {
		t.Errorf("SourceIP = %q, want %q", entry.SourceIP, "10.0.0.1")
	}
	if entry.Timestamp == "" {
		t.Error("Timestamp should not be empty")
	}

	// Verify second entry has fields.
	var entry2 Entry
	if err := json.Unmarshal(lines[1], &entry2); err != nil {
		t.Fatalf("Unmarshal line 1: %v", err)
	}
	if entry2.Event != EventAuthLoginFailure {
		t.Errorf("Event = %q, want %q", entry2.Event, EventAuthLoginFailure)
	}
	if entry2.Fields == nil {
		t.Fatal("Fields should not be nil for login failure")
	}
	if entry2.Fields["reason"] != "invalid credentials" {
		t.Errorf("Fields[reason] = %v, want %q", entry2.Fields["reason"], "invalid credentials")
	}
}

func TestSourceIP(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		headers    map[string]string
		want       string
	}{
		{
			name:       "RemoteAddr with port",
			remoteAddr: "192.168.1.1:54321",
			want:       "192.168.1.1",
		},
		{
			name:       "RemoteAddr without port",
			remoteAddr: "192.168.1.1",
			want:       "192.168.1.1",
		},
		{
			name:       "X-Forwarded-For single",
			remoteAddr: "127.0.0.1:1234",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.50"},
			want:       "203.0.113.50",
		},
		{
			name:       "X-Forwarded-For multiple",
			remoteAddr: "127.0.0.1:1234",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.50, 70.41.3.18, 150.172.238.178"},
			want:       "203.0.113.50",
		},
		{
			name:       "X-Real-IP",
			remoteAddr: "127.0.0.1:1234",
			headers:    map[string]string{"X-Real-IP": "203.0.113.99"},
			want:       "203.0.113.99",
		},
		{
			name:       "X-Forwarded-For takes precedence over X-Real-IP",
			remoteAddr: "127.0.0.1:1234",
			headers: map[string]string{
				"X-Forwarded-For": "203.0.113.50",
				"X-Real-IP":       "203.0.113.99",
			},
			want: "203.0.113.50",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := http.NewRequest("GET", "/", nil)
			r.RemoteAddr = tt.remoteAddr
			for k, v := range tt.headers {
				r.Header.Set(k, v)
			}
			got := SourceIP(r)
			if got != tt.want {
				t.Errorf("SourceIP() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLoggerFilePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")

	l, err := New(true, path)
	if err != nil {
		t.Fatalf("New error: %v", err)
	}
	l.Log(EventAuthLogout, SeverityInfo, "alice", "10.0.0.1", nil)
	l.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat error: %v", err)
	}
	perm := info.Mode().Perm()
	if perm&0o077 != 0 {
		t.Errorf("audit log file permissions = %o, want no group/other access (0600)", perm)
	}
}

func TestNewLoggerInvalidPath(t *testing.T) {
	_, err := New(true, "/nonexistent/dir/audit.log")
	if err == nil {
		t.Fatal("New() should fail for invalid path")
	}
}

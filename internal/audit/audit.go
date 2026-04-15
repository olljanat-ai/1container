package audit

import (
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

// Event types for audit logging.
const (
	EventAuthLoginSuccess       = "auth.login.success"
	EventAuthLoginFailure       = "auth.login.failure"
	EventAuthLogout             = "auth.logout"
	EventAdminEnvironmentCreate = "admin.environment.create"
	EventAdminEnvironmentDelete = "admin.environment.delete"
	EventContainerExec          = "container.exec"
	EventContainerShellOpen     = "container.shell.open"
	EventTunnelAgentConnected   = "tunnel.agent.connected"
	EventTunnelAgentDisconnected = "tunnel.agent.disconnected"
)

// Severity levels for audit events.
const (
	SeverityInfo = "INFO"
	SeverityWarn = "WARN"
)

// Entry represents a single audit log entry.
type Entry struct {
	Timestamp string                 `json:"timestamp"`
	Logger    string                 `json:"logger"`
	Severity  string                 `json:"severity"`
	Event     string                 `json:"event"`
	Actor     string                 `json:"actor"`
	SourceIP  string                 `json:"source_ip,omitempty"`
	Fields    map[string]interface{} `json:"fields,omitempty"`
}

// Logger is the audit logger that writes structured JSON audit entries.
type Logger struct {
	mu      sync.Mutex
	encoder *json.Encoder
	enabled bool
	closer  io.Closer // non-nil when writing to a file
}

// New creates a new audit Logger. If logFile is empty, logs go to stdout.
// If enabled is false, all Log calls are no-ops.
func New(enabled bool, logFile string) (*Logger, error) {
	l := &Logger{enabled: enabled}
	if !enabled {
		return l, nil
	}

	var w io.Writer
	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		w = f
		l.closer = f
	} else {
		w = os.Stdout
	}

	l.encoder = json.NewEncoder(w)
	return l, nil
}

// Close releases any resources held by the logger.
func (l *Logger) Close() error {
	if l.closer != nil {
		return l.closer.Close()
	}
	return nil
}

// Log writes an audit entry. It is safe for concurrent use.
func (l *Logger) Log(event, severity, actor, sourceIP string, fields map[string]interface{}) {
	if !l.enabled {
		return
	}

	entry := Entry{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Logger:    "audit",
		Severity:  severity,
		Event:     event,
		Actor:     actor,
		SourceIP:  sourceIP,
		Fields:    fields,
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.encoder.Encode(entry); err != nil {
		log.Printf("audit: failed to write entry: %v", err)
	}
}

// SourceIP extracts the client IP from an HTTP request.
// It checks X-Forwarded-For and X-Real-IP headers first (for reverse proxies),
// then falls back to r.RemoteAddr.
func SourceIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For may contain multiple IPs; take the first (client).
		for i := 0; i < len(xff); i++ {
			if xff[i] == ',' {
				return xff[:i]
			}
		}
		return xff
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

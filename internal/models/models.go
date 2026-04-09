package models

// EnvType identifies the container orchestrator.
type EnvType string

const (
	EnvDockerSwarm EnvType = "docker-swarm"
	EnvKubernetes  EnvType = "kubernetes"
	EnvNomad       EnvType = "nomad"
)

// Environment represents a registered container platform.
type Environment struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Type     EnvType `json:"type"`
	Endpoint string  `json:"endpoint"`           // base URL of orchestrator API
	Token    string  `json:"token,omitempty"`     // bearer token passed through
	CACert   string  `json:"ca_cert,omitempty"`   // optional CA certificate PEM
	SkipTLS  bool    `json:"skip_tls,omitempty"`  // skip TLS verification
	Tunnel   bool    `json:"tunnel"`              // true when agent provides connectivity
	Online   bool    `json:"online"`              // tunnel agent connected
}

// Container is the unified view of a running workload.
type Container struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Image     string            `json:"image"`
	Status    string            `json:"status"`
	State     string            `json:"state"` // running, stopped, pending …
	EnvID     string            `json:"env_id"`
	EnvName   string            `json:"env_name"`
	EnvType   EnvType           `json:"env_type"`
	Node      string            `json:"node"`
	CreatedAt string            `json:"created_at"`
	Labels    map[string]string `json:"labels,omitempty"`
	Extra     map[string]string `json:"extra,omitempty"` // orchestrator-specific
}

// ContainerDetail extends Container with deeper inspection data.
type ContainerDetail struct {
	Container
	Ports        []PortMapping `json:"ports,omitempty"`
	Mounts       []Mount       `json:"mounts,omitempty"`
	Env          []string      `json:"env,omitempty"`
	Command      string        `json:"command"`
	RestartCount int           `json:"restart_count"`
}

// PortMapping describes a port binding.
type PortMapping struct {
	ContainerPort int    `json:"container_port"`
	HostPort      int    `json:"host_port,omitempty"`
	Protocol      string `json:"protocol"`
}

// Mount describes a volume/bind mount.
type Mount struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Type   string `json:"type"`
}

// TunnelRequest is sent from the server to the agent through the WebSocket.
type TunnelRequest struct {
	ID      string              `json:"id"`
	Method  string              `json:"method"`
	URL     string              `json:"url"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    []byte              `json:"body,omitempty"`
	Stream  bool                `json:"stream,omitempty"` // request chunked streaming response
}

// TunnelResponse is returned by the agent.
type TunnelResponse struct {
	ID         string              `json:"id"`
	StatusCode int                 `json:"status_code"`
	Headers    map[string][]string `json:"headers,omitempty"`
	Body       []byte              `json:"body,omitempty"`
	Error      string              `json:"error,omitempty"`
	Chunk      bool                `json:"chunk,omitempty"` // true for streaming data chunks
	Done       bool                `json:"done,omitempty"`  // true for final stream message
}

// TunnelCancel tells the agent to stop a streaming request.
type TunnelCancel struct {
	ID     string `json:"id"`
	Cancel bool   `json:"cancel"`
}

// ExecRequest is the body of the exec endpoint.
type ExecRequest struct {
	Cmd []string `json:"cmd"`
}

// ExecResponse is the response of the exec endpoint.
type ExecResponse struct {
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
}

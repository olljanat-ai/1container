package models

// ClusterType identifies the container orchestrator.
type ClusterType string

const (
	ClusterDockerSwarm ClusterType = "docker-swarm"
	ClusterKubernetes  ClusterType = "kubernetes"
	ClusterNomad       ClusterType = "nomad"
)

// ValidClusterType reports whether ct is a supported cluster type.
func ValidClusterType(ct ClusterType) bool {
	switch ct {
	case ClusterDockerSwarm, ClusterKubernetes, ClusterNomad:
		return true
	default:
		return false
	}
}

// Cluster represents a container platform connected via an agent.
type Cluster struct {
	ID     string      `json:"id"`
	Name   string      `json:"name"`
	Type   ClusterType `json:"type"`
	Online bool        `json:"online"`
}

// Environment is a tenant: a namespace-scoped view into a cluster.
// Docker Swarm has no namespace support so its namespace is always empty.
// Kubernetes and Nomad map to their native namespace concept.
type Environment struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	ClusterID   string      `json:"cluster_id"`
	ClusterName string      `json:"cluster_name,omitempty"` // populated on read
	ClusterType ClusterType `json:"cluster_type,omitempty"` // populated on read
	Namespace   string      `json:"namespace"`              // k8s/nomad namespace, empty for docker
	Online      bool        `json:"online,omitempty"`       // cluster agent connected
}

// Container is the unified view of a running workload.
type Container struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Image       string            `json:"image"`
	Status      string            `json:"status"`
	State       string            `json:"state"` // running, stopped, pending …
	EnvID       string            `json:"env_id"`
	EnvName     string            `json:"env_name"`
	ClusterType ClusterType       `json:"cluster_type"`
	Namespace   string            `json:"namespace"`
	Node        string            `json:"node"`
	CreatedAt   string            `json:"created_at"`
	Labels      map[string]string `json:"labels,omitempty"`
	Extra       map[string]string `json:"extra,omitempty"`
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

// --- Tunnel protocol ---------------------------------------------------------

// TunnelRequest is sent from the hub to the agent through the WebSocket.
type TunnelRequest struct {
	ID      string              `json:"id"`
	Method  string              `json:"method"`
	URL     string              `json:"url"` // path + query only; agent prepends local endpoint
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
	Chunk      bool                `json:"chunk,omitempty"` // streaming data chunk
	Done       bool                `json:"done,omitempty"`  // final stream message
}

// TunnelCancel tells the agent to stop a streaming request.
type TunnelCancel struct {
	ID     string `json:"id"`
	Cancel bool   `json:"cancel"`
}

// --- API request/response bodies --------------------------------------------

// ExecRequest is the body of the exec endpoint.
type ExecRequest struct {
	Cmd []string `json:"cmd"`
}

// ExecResponse is the response of the exec endpoint.
type ExecResponse struct {
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
}

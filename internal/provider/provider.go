package provider

import (
	"container-hub/internal/models"
	"context"
	"io"
	"net/http"
)

// Provider abstracts a container orchestrator API.
type Provider interface {
	ListContainers(ctx context.Context) ([]models.Container, error)
	InspectContainer(ctx context.Context, containerID string) (*models.ContainerDetail, error)
	ContainerLogs(ctx context.Context, containerID string, tail int, follow bool) (io.ReadCloser, error)
	ExecContainer(ctx context.Context, containerID string, cmd []string) (*models.ExecResponse, error)

	// Lifecycle operations
	StopContainer(ctx context.Context, containerID string) error
	RestartContainer(ctx context.Context, containerID string) error
	DeleteContainer(ctx context.Context, containerID string) error
}

// Config holds provider configuration. Auth is handled by the agent, not here.
type Config struct {
	ClusterType models.ClusterType
	Namespace   string // k8s/nomad namespace; empty for docker
	EnvID       string // environment ID to tag on containers
	EnvName     string // environment name to tag on containers
}

// New creates a provider for the given config using the supplied HTTP client.
// The client's transport tunnels requests through the agent.
func New(cfg Config, client *http.Client) Provider {
	switch cfg.ClusterType {
	case models.ClusterDockerSwarm:
		return &DockerProvider{client: client, cfg: cfg}
	case models.ClusterKubernetes:
		return &KubeProvider{client: client, cfg: cfg}
	case models.ClusterNomad:
		return &NomadProvider{client: client, cfg: cfg}
	default:
		return nil
	}
}

// truncateID safely shortens an ID string to maxLen characters.
func truncateID(id string, maxLen int) string {
	if len(id) <= maxLen {
		return id
	}
	return id[:maxLen]
}

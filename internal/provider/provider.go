package provider

import (
	"container-hub/internal/models"
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"strings"
)

// Provider abstracts a container orchestrator API.
type Provider interface {
	ListContainers(ctx context.Context) ([]models.Container, error)
	InspectContainer(ctx context.Context, containerID string) (*models.ContainerDetail, error)
	ContainerLogs(ctx context.Context, containerID string, tail int) (io.ReadCloser, error)
	ExecContainer(ctx context.Context, containerID string, cmd []string) (*models.ExecResponse, error)
}

// New creates a Provider for the given environment using the supplied HTTP transport.
func New(env *models.Environment, transport http.RoundTripper) Provider {
	if transport == nil {
		transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: env.SkipTLS},
		}
	}
	client := &http.Client{Transport: transport}
	base := strings.TrimRight(env.Endpoint, "/")

	switch env.Type {
	case models.EnvDockerSwarm:
		return &DockerProvider{client: client, base: base, token: env.Token, env: env}
	case models.EnvKubernetes:
		return &KubeProvider{client: client, base: base, token: env.Token, env: env}
	case models.EnvNomad:
		return &NomadProvider{client: client, base: base, token: env.Token, env: env}
	default:
		return &DockerProvider{client: client, base: base, token: env.Token, env: env}
	}
}

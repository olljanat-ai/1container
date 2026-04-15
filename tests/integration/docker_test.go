//go:build integration

package integration

import (
	"container-hub/internal/models"
	"container-hub/internal/provider"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestDockerProvider exercises the Docker provider against a real Docker-in-Docker
// daemon. It starts a DinD container, creates a test workload, and verifies all
// four Provider interface methods.
//
// Set DOCKER_INTEGRATION_ENDPOINT to an existing Docker API endpoint to skip
// automatic DinD setup.
func TestDockerProvider(t *testing.T) {
	skipIfNoDocker(t)

	endpoint := os.Getenv("DOCKER_INTEGRATION_ENDPOINT")
	if endpoint == "" {
		endpoint = setupDinD(t)
	}

	client := newTestClient(endpoint)
	p := provider.New(provider.Config{
		ClusterType: models.ClusterDockerSwarm,
		EnvID:       "test-docker",
		EnvName:     "Integration Docker",
	}, client)

	ctx := context.Background()
	var containerID string

	t.Run("ListContainers", func(t *testing.T) {
		containers, err := p.ListContainers(ctx)
		if err != nil {
			t.Fatalf("ListContainers: %v", err)
		}
		if len(containers) == 0 {
			t.Fatal("expected at least one container")
		}

		for _, c := range containers {
			if c.Name == "integration-test" {
				containerID = c.ID
				if c.State != "running" {
					t.Errorf("state = %q, want running", c.State)
				}
				if !strings.Contains(c.Image, "alpine") {
					t.Errorf("image = %q, want to contain alpine", c.Image)
				}
				if c.ClusterType != models.ClusterDockerSwarm {
					t.Errorf("cluster_type = %q, want %q", c.ClusterType, models.ClusterDockerSwarm)
				}
				if c.EnvID != "test-docker" {
					t.Errorf("env_id = %q, want test-docker", c.EnvID)
				}
				if c.EnvName != "Integration Docker" {
					t.Errorf("env_name = %q, want Integration Docker", c.EnvName)
				}
				if c.Node == "" {
					t.Error("node should not be empty")
				}
				if c.CreatedAt == "" {
					t.Error("created_at should not be empty")
				}
				return
			}
		}
		t.Error("container 'integration-test' not found in list")
		for _, c := range containers {
			t.Logf("  found: %s (%s) state=%s", c.Name, c.ID, c.State)
		}
	})

	t.Run("InspectContainer", func(t *testing.T) {
		if containerID == "" {
			t.Skip("no container ID from ListContainers")
		}
		detail, err := p.InspectContainer(ctx, containerID)
		if err != nil {
			t.Fatalf("InspectContainer(%s): %v", containerID, err)
		}
		if detail.Name != "integration-test" {
			t.Errorf("name = %q, want integration-test", detail.Name)
		}
		if detail.State != "running" {
			t.Errorf("state = %q, want running", detail.State)
		}
		if !strings.Contains(detail.Image, "alpine") {
			t.Errorf("image = %q, want to contain alpine", detail.Image)
		}
		if detail.Command == "" {
			t.Error("command should not be empty")
		}
		if detail.ClusterType != models.ClusterDockerSwarm {
			t.Errorf("cluster_type = %q, want %q", detail.ClusterType, models.ClusterDockerSwarm)
		}
	})

	t.Run("ContainerLogs", func(t *testing.T) {
		if containerID == "" {
			t.Skip("no container ID from ListContainers")
		}
		rc, err := p.ContainerLogs(ctx, containerID, 100, false)
		if err != nil {
			t.Fatalf("ContainerLogs(%s): %v", containerID, err)
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read logs: %v", err)
		}
		if len(data) == 0 {
			t.Fatal("expected non-empty log output")
		}
		// Docker returns multiplexed stream; the text is embedded in it.
		if !strings.Contains(string(data), "hello-integration") {
			t.Errorf("logs should contain 'hello-integration', got %d bytes", len(data))
		}
	})

	t.Run("ExecContainer", func(t *testing.T) {
		if containerID == "" {
			t.Skip("no container ID from ListContainers")
		}
		resp, err := p.ExecContainer(ctx, containerID, []string{"echo", "exec-works"})
		if err != nil {
			t.Fatalf("ExecContainer(%s): %v", containerID, err)
		}
		if !strings.Contains(resp.Output, "exec-works") {
			t.Errorf("output = %q, want to contain 'exec-works'", resp.Output)
		}
		if resp.ExitCode != 0 {
			t.Errorf("exit_code = %d, want 0", resp.ExitCode)
		}
	})
}

// TestDockerLifecycle exercises the Docker provider's lifecycle operations
// (stop, restart, delete) against a real Docker-in-Docker daemon.
func TestDockerLifecycle(t *testing.T) {
	skipIfNoDocker(t)

	endpoint := os.Getenv("DOCKER_INTEGRATION_ENDPOINT")
	if endpoint == "" {
		endpoint = setupDinD(t)
	}

	client := newTestClient(endpoint)
	p := provider.New(provider.Config{
		ClusterType: models.ClusterDockerSwarm,
		EnvID:       "test-docker",
		EnvName:     "Integration Docker",
	}, client)

	ctx := context.Background()

	// Find the integration-test container.
	containers, err := p.ListContainers(ctx)
	if err != nil {
		t.Fatalf("ListContainers: %v", err)
	}
	var containerID string
	for _, c := range containers {
		if c.Name == "integration-test" {
			containerID = c.ID
			break
		}
	}
	if containerID == "" {
		t.Fatal("integration-test container not found")
	}

	t.Run("StopContainer", func(t *testing.T) {
		if err := p.StopContainer(ctx, containerID); err != nil {
			t.Fatalf("StopContainer(%s): %v", containerID, err)
		}
		// Verify container is stopped.
		detail, err := p.InspectContainer(ctx, containerID)
		if err != nil {
			t.Fatalf("InspectContainer after stop: %v", err)
		}
		if detail.State != "exited" {
			t.Errorf("state after stop = %q, want exited", detail.State)
		}
	})

	t.Run("RestartContainer", func(t *testing.T) {
		if err := p.RestartContainer(ctx, containerID); err != nil {
			t.Fatalf("RestartContainer(%s): %v", containerID, err)
		}
		// Verify container is running again.
		detail, err := p.InspectContainer(ctx, containerID)
		if err != nil {
			t.Fatalf("InspectContainer after restart: %v", err)
		}
		if detail.State != "running" {
			t.Errorf("state after restart = %q, want running", detail.State)
		}
	})

	t.Run("DeleteContainer", func(t *testing.T) {
		if err := p.DeleteContainer(ctx, containerID); err != nil {
			t.Fatalf("DeleteContainer(%s): %v", containerID, err)
		}
		// Verify container is gone.
		_, err := p.InspectContainer(ctx, containerID)
		if err == nil {
			t.Error("expected error inspecting deleted container, got nil")
		}
	})
}

// setupDinD starts a Docker-in-Docker container, creates a test workload inside
// it, and returns the DinD API endpoint.
func setupDinD(t *testing.T) string {
	t.Helper()

	id := dockerRun(t,
		"--privileged",
		"-e", "DOCKER_TLS_CERTDIR=",
		"-p", "0:2375",
		"docker:27-dind",
	)
	port := dockerPort(t, id, "2375")
	endpoint := fmt.Sprintf("http://127.0.0.1:%s", port)

	// Wait for the DinD daemon to be ready.
	waitForHTTP(t, endpoint+"/version", 60*time.Second)

	dh := fmt.Sprintf("tcp://127.0.0.1:%s", port)

	// Pull a small test image inside DinD.
	if out, err := exec.Command("docker", "-H", dh, "pull", "alpine:latest").CombinedOutput(); err != nil {
		t.Fatalf("pull alpine inside DinD: %v\n%s", err, out)
	}

	// Start a test container inside DinD.
	if out, err := exec.Command("docker", "-H", dh,
		"run", "-d", "--name", "integration-test",
		"alpine:latest", "sh", "-c", "echo hello-integration && sleep 3600",
	).CombinedOutput(); err != nil {
		t.Fatalf("run test container inside DinD: %v\n%s", err, out)
	}

	// Give the container a moment to start and produce output.
	time.Sleep(3 * time.Second)
	return endpoint
}

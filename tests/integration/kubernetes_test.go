//go:build integration

package integration

import (
	"container-hub/internal/models"
	"container-hub/internal/provider"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// TestKubernetesProvider exercises the Kubernetes provider against a real K3s
// cluster running inside Docker (the same engine K3d wraps). It starts K3s,
// creates a service account with cluster-admin privileges, deploys a test pod,
// and verifies ListContainers, InspectContainer, and ContainerLogs.
//
// Set K8S_INTEGRATION_ENDPOINT and K8S_INTEGRATION_TOKEN to an existing
// Kubernetes API to skip automatic K3s setup.
func TestKubernetesProvider(t *testing.T) {
	skipIfNoDocker(t)

	endpoint := os.Getenv("K8S_INTEGRATION_ENDPOINT")
	token := os.Getenv("K8S_INTEGRATION_TOKEN")

	if endpoint == "" {
		endpoint, token = setupK3s(t)
	}

	client := newTestClient(endpoint, withAuth("Authorization", "Bearer "+token))
	p := provider.New(provider.Config{
		ClusterType: models.ClusterKubernetes,
		Namespace:   "default",
		EnvID:       "test-k8s",
		EnvName:     "Integration Kubernetes",
	}, client)

	ctx := context.Background()

	t.Run("ListContainers", func(t *testing.T) {
		containers, err := p.ListContainers(ctx)
		if err != nil {
			t.Fatalf("ListContainers: %v", err)
		}
		if len(containers) == 0 {
			t.Fatal("expected at least one container (pod)")
		}

		var found bool
		for _, c := range containers {
			if c.Name == "integration-test" {
				found = true
				if c.State != "running" {
					t.Errorf("state = %q, want running", c.State)
				}
				if !strings.Contains(c.Image, "busybox") {
					t.Errorf("image = %q, want to contain busybox", c.Image)
				}
				if c.ClusterType != models.ClusterKubernetes {
					t.Errorf("cluster_type = %q, want %q", c.ClusterType, models.ClusterKubernetes)
				}
				if c.Namespace != "default" {
					t.Errorf("namespace = %q, want default", c.Namespace)
				}
				if c.EnvID != "test-k8s" {
					t.Errorf("env_id = %q, want test-k8s", c.EnvID)
				}
				if c.EnvName != "Integration Kubernetes" {
					t.Errorf("env_name = %q, want Integration Kubernetes", c.EnvName)
				}
				if c.Node == "" {
					t.Error("node should not be empty")
				}
				if c.CreatedAt == "" {
					t.Error("created_at should not be empty")
				}
				break
			}
		}
		if !found {
			t.Error("pod 'integration-test' not found in list")
			for _, c := range containers {
				t.Logf("  found: %s (%s) state=%s", c.Name, c.ID, c.State)
			}
		}
	})

	t.Run("InspectContainer", func(t *testing.T) {
		detail, err := p.InspectContainer(ctx, "integration-test")
		if err != nil {
			t.Fatalf("InspectContainer(integration-test): %v", err)
		}
		if detail.Name != "integration-test" {
			t.Errorf("name = %q, want integration-test", detail.Name)
		}
		if detail.State != "running" {
			t.Errorf("state = %q, want running", detail.State)
		}
		if !strings.Contains(detail.Image, "busybox") {
			t.Errorf("image = %q, want to contain busybox", detail.Image)
		}
		if detail.ClusterType != models.ClusterKubernetes {
			t.Errorf("cluster_type = %q, want %q", detail.ClusterType, models.ClusterKubernetes)
		}
		if detail.Namespace != "default" {
			t.Errorf("namespace = %q, want default", detail.Namespace)
		}
		if detail.Command == "" {
			t.Error("command should not be empty")
		}
	})

	t.Run("ContainerLogs", func(t *testing.T) {
		rc, err := p.ContainerLogs(ctx, "integration-test", 100, false)
		if err != nil {
			t.Fatalf("ContainerLogs(integration-test): %v", err)
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read logs: %v", err)
		}
		if len(data) == 0 {
			t.Fatal("expected non-empty log output")
		}
		if !strings.Contains(string(data), "hello-integration") {
			t.Errorf("logs should contain 'hello-integration', got %q", string(data))
		}
	})
}

// setupK3s starts a K3s cluster in Docker, creates an admin service account,
// deploys a test pod, and returns the API endpoint and bearer token.
func setupK3s(t *testing.T) (endpoint, token string) {
	t.Helper()

	id := dockerRun(t,
		"--privileged",
		"--tmpfs", "/run",
		"--tmpfs", "/var/run",
		"-p", "0:6443",
		"-e", "K3S_KUBECONFIG_MODE=644",
		"rancher/k3s:latest",
		"server",
		"--disable=traefik",
		"--disable=metrics-server",
	)
	port := dockerPort(t, id, "6443")
	endpoint = fmt.Sprintf("https://127.0.0.1:%s", port)

	// Wait for the K3s API server to be ready.
	waitForHTTP(t, endpoint+"/readyz", 120*time.Second)

	// Wait for the node to reach Ready condition.
	waitForShell(t, 120*time.Second,
		"docker", "exec", id,
		"k3s", "kubectl", "wait", "--for=condition=Ready", "node", "--all", "--timeout=90s",
	)

	// Create a service account with cluster-admin privileges for the tests.
	dockerExec(t, id,
		"k3s", "kubectl", "create", "serviceaccount", "integration-test", "-n", "default",
	)
	dockerExec(t, id,
		"k3s", "kubectl", "create", "clusterrolebinding", "integration-test",
		"--clusterrole=cluster-admin",
		"--serviceaccount=default:integration-test",
	)
	token = dockerExec(t, id,
		"k3s", "kubectl", "create", "token", "integration-test", "-n", "default", "--duration=1h",
	)
	if token == "" {
		t.Fatal("failed to create service account token")
	}

	// Deploy a test pod.
	dockerExec(t, id,
		"k3s", "kubectl", "run", "integration-test",
		"--image=busybox:stable",
		"--restart=Never",
		"--labels=app=integration-test",
		"--command", "--",
		"sh", "-c", "echo hello-integration && sleep 3600",
	)

	// Wait for the pod to be ready.
	waitForShell(t, 120*time.Second,
		"docker", "exec", id,
		"k3s", "kubectl", "wait", "--for=condition=Ready", "pod/integration-test",
		"-n", "default", "--timeout=90s",
	)

	// Give the container a moment to produce log output.
	time.Sleep(3 * time.Second)

	return endpoint, token
}

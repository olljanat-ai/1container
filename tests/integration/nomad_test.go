//go:build integration

package integration

import (
	"bytes"
	"container-hub/internal/models"
	"container-hub/internal/provider"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestNomadProvider exercises the Nomad provider against a real Nomad agent
// running in dev mode inside Docker. It starts the agent, submits a raw_exec
// job, and verifies ListContainers, InspectContainer, and ContainerLogs.
//
// Set NOMAD_INTEGRATION_ENDPOINT to an existing Nomad HTTP API endpoint to
// skip automatic setup.
func TestNomadProvider(t *testing.T) {
	skipIfNoDocker(t)

	endpoint := os.Getenv("NOMAD_INTEGRATION_ENDPOINT")
	if endpoint == "" {
		endpoint = setupNomad(t)
	}

	client := newTestClient(endpoint)
	p := provider.New(provider.Config{
		ClusterType: models.ClusterNomad,
		EnvID:       "test-nomad",
		EnvName:     "Integration Nomad",
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
			if strings.Contains(c.Name, "integration-test") {
				containerID = c.ID
				if c.State == "" {
					t.Error("state should not be empty")
				}
				if c.ClusterType != models.ClusterNomad {
					t.Errorf("cluster_type = %q, want %q", c.ClusterType, models.ClusterNomad)
				}
				if c.EnvID != "test-nomad" {
					t.Errorf("env_id = %q, want test-nomad", c.EnvID)
				}
				if c.EnvName != "Integration Nomad" {
					t.Errorf("env_name = %q, want Integration Nomad", c.EnvName)
				}
				if c.Extra == nil {
					t.Error("extra metadata should not be nil")
				} else {
					if c.Extra["alloc_id"] == "" {
						t.Error("extra.alloc_id should not be empty")
					}
					if c.Extra["job_id"] != "integration-test" {
						t.Errorf("extra.job_id = %q, want integration-test", c.Extra["job_id"])
					}
					if c.Extra["task"] != "main" {
						t.Errorf("extra.task = %q, want main", c.Extra["task"])
					}
					if c.Extra["task_group"] != "test" {
						t.Errorf("extra.task_group = %q, want test", c.Extra["task_group"])
					}
				}
				if c.Node == "" {
					t.Error("node should not be empty")
				}
				return
			}
		}
		t.Error("integration-test container not found")
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
		if !strings.Contains(detail.Name, "integration-test") {
			t.Errorf("name = %q, want to contain integration-test", detail.Name)
		}
		if detail.ClusterType != models.ClusterNomad {
			t.Errorf("cluster_type = %q, want %q", detail.ClusterType, models.ClusterNomad)
		}
		// The Nomad provider sets Command to the driver name.
		if detail.Command != "raw_exec" {
			t.Errorf("command = %q, want raw_exec", detail.Command)
		}
		if detail.Extra == nil || detail.Extra["job_id"] != "integration-test" {
			t.Errorf("extra.job_id = %q, want integration-test", detail.Extra["job_id"])
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
		// The echo output should appear in stdout (provider falls back from
		// stderr to stdout automatically).
		if !strings.Contains(string(data), "hello-integration") {
			t.Errorf("logs should contain 'hello-integration', got %q", string(data))
		}
	})
}

// setupNomad starts a Nomad dev agent in Docker, submits a test job, waits for
// the allocation to be running, and returns the Nomad HTTP API endpoint.
func setupNomad(t *testing.T) string {
	t.Helper()

	id := dockerRun(t,
		"--privileged",
		"-p", "0:4646",
		"hashicorp/nomad:latest",
		"agent", "-dev", "-bind=0.0.0.0",
	)
	port := dockerPort(t, id, "4646")
	endpoint := fmt.Sprintf("http://127.0.0.1:%s", port)

	// Wait for the Nomad API to respond and a leader to be elected.
	waitForHTTP(t, endpoint+"/v1/status/leader", 60*time.Second)

	// Wait for the client node to be ready.
	waitForNomadNode(t, endpoint, 60*time.Second)

	// Submit the test job.
	submitNomadJob(t, endpoint)

	// Wait for the allocation to reach running state.
	waitForNomadAlloc(t, endpoint, "integration-test", 60*time.Second)

	return endpoint
}

// waitForNomadNode polls Nomad until at least one client node is ready.
func waitForNomadNode(t *testing.T, endpoint string, timeout time.Duration) {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(endpoint + "/v1/nodes")
		if err == nil {
			var nodes []struct {
				Status string `json:"Status"`
			}
			json.NewDecoder(resp.Body).Decode(&nodes)
			resp.Body.Close()
			for _, n := range nodes {
				if n.Status == "ready" {
					return
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatal("timeout waiting for Nomad client node to be ready")
}

// submitNomadJob registers a simple raw_exec job.
func submitNomadJob(t *testing.T, endpoint string) {
	t.Helper()
	job := map[string]interface{}{
		"Job": map[string]interface{}{
			"ID":          "integration-test",
			"Name":        "integration-test",
			"Type":        "service",
			"Datacenters": []string{"dc1"},
			"TaskGroups": []map[string]interface{}{
				{
					"Name":  "test",
					"Count": 1,
					"Tasks": []map[string]interface{}{
						{
							"Name":   "main",
							"Driver": "raw_exec",
							"Config": map[string]interface{}{
								"command": "/bin/sh",
								"args":    []string{"-c", "echo hello-integration && sleep 3600"},
							},
							"Resources": map[string]interface{}{
								"CPU":      100,
								"MemoryMB": 64,
							},
						},
					},
				},
			},
		},
	}
	body, _ := json.Marshal(job)
	resp, err := http.Post(endpoint+"/v1/jobs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("submit nomad job: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("submit nomad job: status %d – %s", resp.StatusCode, b)
	}
}

// waitForNomadAlloc polls Nomad until a running allocation exists for the job.
func waitForNomadAlloc(t *testing.T, endpoint, jobID string, timeout time.Duration) {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(endpoint + "/v1/job/" + jobID + "/allocations")
		if err == nil {
			var allocs []struct {
				ClientStatus string `json:"ClientStatus"`
			}
			json.NewDecoder(resp.Body).Decode(&allocs)
			resp.Body.Close()
			for _, a := range allocs {
				if a.ClientStatus == "running" {
					// Give the task a moment to produce output.
					time.Sleep(3 * time.Second)
					return
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatal("timeout waiting for Nomad allocation to be running")
}

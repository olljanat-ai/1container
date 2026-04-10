package discovery

import (
	"container-hub/internal/models"
	"container-hub/internal/tunnel"
	"context"
	"sync"
	"testing"
	"time"
)

func TestNewDiscovery(t *testing.T) {
	hub := tunnel.NewHub(nil, nil)
	d := New(hub, func(env *models.Environment) {}, func(id string) {}, 30*time.Second)
	if d == nil {
		t.Fatal("New() returned nil")
	}
	if d.interval != 30*time.Second {
		t.Errorf("interval = %v, want 30s", d.interval)
	}
}

func TestDiscoverOfflineClusters(t *testing.T) {
	hub := tunnel.NewHub(nil, nil)
	var registered []*models.Environment

	d := New(hub, func(env *models.Environment) {
		registered = append(registered, env)
	}, func(id string) {}, 60*time.Second)

	getClusters := func() []*models.Cluster {
		return []*models.Cluster{
			{ID: "k8s-1", Name: "K8s Prod", Type: models.ClusterKubernetes},
			{ID: "swarm-1", Name: "Docker Staging", Type: models.ClusterDockerSwarm},
		}
	}

	d.discover(getClusters)

	// No agents are connected, so IsOnline is false for all clusters
	// No environments should be registered
	if len(registered) != 0 {
		t.Errorf("registered %d environments, want 0 (clusters are offline)", len(registered))
	}
}

func TestDiscoverRemovesStaleEnvironments(t *testing.T) {
	hub := tunnel.NewHub(nil, nil)
	var mu sync.Mutex
	var removed []string

	d := New(hub, func(env *models.Environment) {}, func(id string) {
		mu.Lock()
		removed = append(removed, id)
		mu.Unlock()
	}, 60*time.Second)

	// Simulate previously discovered environments
	d.mu.Lock()
	d.discovered = map[string]bool{
		"auto-cluster-1":   true,
		"auto-cluster-2":   true,
		"auto-cluster-3":   true,
	}
	d.mu.Unlock()

	// Now discover with no clusters (all offline) - all should be removed
	d.discover(func() []*models.Cluster { return nil })

	mu.Lock()
	defer mu.Unlock()
	if len(removed) != 3 {
		t.Errorf("removed %d environments, want 3", len(removed))
	}
}

func TestDiscoverPartialRemoval(t *testing.T) {
	hub := tunnel.NewHub(nil, nil)
	var mu sync.Mutex
	var removed []string

	d := New(hub, func(env *models.Environment) {}, func(id string) {
		mu.Lock()
		removed = append(removed, id)
		mu.Unlock()
	}, 60*time.Second)

	// Simulate 3 previously discovered envs
	d.mu.Lock()
	d.discovered = map[string]bool{
		"auto-a": true,
		"auto-b": true,
		"auto-c": true,
	}
	d.mu.Unlock()

	// No clusters online, so nothing is re-discovered, all are removed
	d.discover(func() []*models.Cluster { return nil })

	mu.Lock()
	defer mu.Unlock()
	if len(removed) != 3 {
		t.Errorf("removed %d environments, want 3", len(removed))
	}

	// Verify discovered map is now empty
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.discovered) != 0 {
		t.Errorf("discovered has %d entries, want 0", len(d.discovered))
	}
}

func TestRunRespectsContextCancellation(t *testing.T) {
	hub := tunnel.NewHub(nil, nil)
	d := New(hub, func(env *models.Environment) {}, func(id string) {}, 100*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.Run(ctx, func() []*models.Cluster { return nil })
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// Run exited as expected
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit after context cancellation")
	}
}

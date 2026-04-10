package discovery

import (
	"container-hub/internal/models"
	"container-hub/internal/tunnel"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// EnvRegistrar is called when an environment is discovered.
type EnvRegistrar func(env *models.Environment)

// EnvRemover is called when an environment is no longer discovered.
type EnvRemover func(envID string)

// Discovery periodically discovers environments from connected clusters.
type Discovery struct {
	hub        *tunnel.Hub
	registrar  EnvRegistrar
	remover    EnvRemover
	interval   time.Duration
	mu         sync.Mutex
	discovered map[string]bool // set of environment IDs we discovered
}

// New creates a new Discovery instance.
func New(hub *tunnel.Hub, registrar EnvRegistrar, remover EnvRemover, interval time.Duration) *Discovery {
	return &Discovery{
		hub:        hub,
		registrar:  registrar,
		remover:    remover,
		interval:   interval,
		discovered: make(map[string]bool),
	}
}

// Run starts the periodic discovery loop. Blocks forever.
func (d *Discovery) Run(ctx context.Context, getClusters func() []*models.Cluster) {
	d.discover(getClusters)
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.discover(getClusters)
		}
	}
}

func (d *Discovery) discover(getClusters func() []*models.Cluster) {
	clusters := getClusters()
	newDiscovered := make(map[string]bool)

	for _, cluster := range clusters {
		if !d.hub.IsOnline(cluster.ID) {
			continue
		}
		var envs []*models.Environment
		var err error
		switch cluster.Type {
		case models.ClusterKubernetes:
			envs, err = d.discoverK8sNamespaces(cluster)
		case models.ClusterNomad:
			envs, err = d.discoverNomadNamespaces(cluster)
		default:
			// Docker Swarm doesn't have namespaces; create one env per cluster
			envID := "auto-" + cluster.ID
			envs = []*models.Environment{{
				ID:        envID,
				Name:      cluster.Name,
				ClusterID: cluster.ID,
				Namespace: "",
			}}
		}
		if err != nil {
			log.Printf("discovery error for cluster %s: %v", cluster.Name, err)
			continue
		}
		for _, env := range envs {
			newDiscovered[env.ID] = true
			d.registrar(env)
		}
	}

	// Remove environments that are no longer discovered
	d.mu.Lock()
	for id := range d.discovered {
		if !newDiscovered[id] {
			d.remover(id)
		}
	}
	d.discovered = newDiscovered
	d.mu.Unlock()
}

func (d *Discovery) discoverK8sNamespaces(cluster *models.Cluster) ([]*models.Environment, error) {
	transport := d.hub.Transport(cluster.ID)
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}

	req, err := http.NewRequestWithContext(context.Background(), "GET", "http://api/api/v1/namespaces", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("k8s namespace list: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("k8s namespace list: %s – %s", resp.Status, string(body))
	}

	var nsList struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&nsList); err != nil {
		return nil, fmt.Errorf("k8s namespace decode: %w", err)
	}

	var envs []*models.Environment
	for _, ns := range nsList.Items {
		envID := "auto-" + cluster.ID + "-" + ns.Metadata.Name
		envs = append(envs, &models.Environment{
			ID:        envID,
			Name:      cluster.Name + "/" + ns.Metadata.Name,
			ClusterID: cluster.ID,
			Namespace: ns.Metadata.Name,
		})
	}
	return envs, nil
}

func (d *Discovery) discoverNomadNamespaces(cluster *models.Cluster) ([]*models.Environment, error) {
	transport := d.hub.Transport(cluster.ID)
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}

	req, err := http.NewRequestWithContext(context.Background(), "GET", "http://api/v1/namespaces", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("nomad namespace list: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("nomad namespace list: %s – %s", resp.Status, string(body))
	}

	var namespaces []struct {
		Name string `json:"Name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&namespaces); err != nil {
		return nil, fmt.Errorf("nomad namespace decode: %w", err)
	}

	var envs []*models.Environment
	for _, ns := range namespaces {
		envID := "auto-" + cluster.ID + "-" + ns.Name
		envs = append(envs, &models.Environment{
			ID:        envID,
			Name:      cluster.Name + "/" + ns.Name,
			ClusterID: cluster.ID,
			Namespace: ns.Name,
		})
	}
	return envs, nil
}

package main

import (
	"container-hub/internal/tunnel"
	"flag"
	"log"
	"os"
	"strings"
)

func main() {
	serverURL := flag.String("server", "", "hub server WebSocket URL (ws://host:port/ws/tunnel)")
	clusterID := flag.String("cluster-id", "", "unique cluster ID")
	clusterName := flag.String("cluster-name", "", "human-friendly cluster name")
	clusterType := flag.String("cluster-type", "", "cluster type: docker-swarm, kubernetes, nomad")
	localEndpoint := flag.String("endpoint", "", "local orchestrator API endpoint")
	authToken := flag.String("token", "", "auth token for local orchestrator API")
	skipTLS := flag.Bool("skip-tls", false, "skip TLS verification for local endpoint and hub")
	flag.Parse()

	// Env var overrides
	if v := os.Getenv("HUB_SERVER"); v != "" {
		*serverURL = v
	}
	if v := os.Getenv("CLUSTER_ID"); v != "" {
		*clusterID = v
	}
	if v := os.Getenv("CLUSTER_NAME"); v != "" {
		*clusterName = v
	}
	if v := os.Getenv("CLUSTER_TYPE"); v != "" {
		*clusterType = v
	}
	if v := os.Getenv("LOCAL_ENDPOINT"); v != "" {
		*localEndpoint = v
	}
	if v := os.Getenv("AUTH_TOKEN"); v != "" {
		*authToken = v
	}
	if strings.EqualFold(os.Getenv("SKIP_TLS"), "true") {
		*skipTLS = true
	}

	if *serverURL == "" || *clusterID == "" || *clusterName == "" || *clusterType == "" || *localEndpoint == "" {
		log.Fatal("required: -server, -cluster-id, -cluster-name, -cluster-type, -endpoint")
	}

	log.Printf("container-hub agent starting")
	log.Printf("  hub:      %s", *serverURL)
	log.Printf("  cluster:  %s (%s) type=%s", *clusterName, *clusterID, *clusterType)
	log.Printf("  endpoint: %s", *localEndpoint)

	agent := tunnel.NewAgentClient(tunnel.AgentConfig{
		ServerURL:     *serverURL,
		ClusterID:     *clusterID,
		ClusterName:   *clusterName,
		ClusterType:   *clusterType,
		LocalEndpoint: *localEndpoint,
		AuthToken:     *authToken,
		SkipTLS:       *skipTLS,
	})
	agent.Run()
}

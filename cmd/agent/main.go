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
	envID := flag.String("env-id", "", "unique environment ID")
	envName := flag.String("env-name", "", "human-friendly environment name")
	envType := flag.String("env-type", "", "environment type: docker-swarm, kubernetes, nomad")
	localEndpoint := flag.String("endpoint", "", "local orchestrator API endpoint")
	agentToken := flag.String("token", "", "shared authentication token")
	skipTLS := flag.Bool("skip-tls", false, "skip TLS verification for local endpoint")
	flag.Parse()

	// Allow env var overrides
	if v := os.Getenv("HUB_SERVER"); v != "" {
		*serverURL = v
	}
	if v := os.Getenv("ENV_ID"); v != "" {
		*envID = v
	}
	if v := os.Getenv("ENV_NAME"); v != "" {
		*envName = v
	}
	if v := os.Getenv("ENV_TYPE"); v != "" {
		*envType = v
	}
	if v := os.Getenv("LOCAL_ENDPOINT"); v != "" {
		*localEndpoint = v
	}
	if v := os.Getenv("AGENT_TOKEN"); v != "" {
		*agentToken = v
	}
	if strings.EqualFold(os.Getenv("SKIP_TLS"), "true") {
		*skipTLS = true
	}

	if *serverURL == "" || *envID == "" || *envName == "" || *envType == "" || *localEndpoint == "" {
		log.Fatal("required flags: -server, -env-id, -env-name, -env-type, -endpoint (or corresponding env vars)")
	}

	log.Printf("container-hub agent starting")
	log.Printf("  server:   %s", *serverURL)
	log.Printf("  env:      %s (%s)", *envName, *envID)
	log.Printf("  type:     %s", *envType)
	log.Printf("  endpoint: %s", *localEndpoint)

	agent := tunnel.NewAgentClient(*serverURL, *envID, *envName, *envType, *localEndpoint, *agentToken, *skipTLS)
	agent.Run() // blocks, reconnects on failure
}

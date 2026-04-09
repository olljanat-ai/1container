package main

import (
	"container-hub/internal/api"
	"container-hub/internal/models"
	"container-hub/internal/tunnel"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	configFile := flag.String("config", "", "path to environments JSON config file")
	flag.Parse()

	if v := os.Getenv("LISTEN_ADDR"); v != "" {
		*addr = v
	}

	var srv *api.Server
	hub := tunnel.NewHub(
		func(id, name string, ctype models.ClusterType) { srv.ClusterJoined(id, name, ctype) },
		func(id string) { srv.ClusterLeft(id) },
	)
	srv = api.NewServer(hub)

	// Load environments from config file
	if *configFile != "" {
		loadConfig(srv, *configFile)
	}
	// Load from ENVIRONMENTS env var
	if envJSON := os.Getenv("ENVIRONMENTS"); envJSON != "" {
		var envs []models.Environment
		if err := json.Unmarshal([]byte(envJSON), &envs); err != nil {
			log.Fatalf("failed to parse ENVIRONMENTS: %v", err)
		}
		for i := range envs {
			srv.RegisterEnvironment(&envs[i])
			log.Printf("loaded environment: %s (cluster=%s ns=%s)", envs[i].Name, envs[i].ClusterID, envs[i].Namespace)
		}
	}

	log.Printf("container-hub server listening on %s", *addr)
	if err := http.ListenAndServe(*addr, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}

func loadConfig(srv *api.Server, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("failed to read config: %v", err)
	}
	var envs []models.Environment
	if err := json.Unmarshal(data, &envs); err != nil {
		log.Fatalf("failed to parse config: %v", err)
	}
	for i := range envs {
		srv.RegisterEnvironment(&envs[i])
		log.Printf("loaded environment: %s (cluster=%s ns=%s)", envs[i].Name, envs[i].ClusterID, envs[i].Namespace)
	}
}

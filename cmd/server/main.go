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
	"strings"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	configFile := flag.String("config", "", "path to environments JSON config file")
	flag.Parse()

	// Allow env var override
	if v := os.Getenv("LISTEN_ADDR"); v != "" {
		*addr = v
	}

	var srv *api.Server

	hub := tunnel.NewHub(
		func(envID string) { srv.SetOnline(envID, true) },
		func(envID string) { srv.SetOnline(envID, false) },
	)
	srv = api.NewServer(hub)

	// Load initial environments from config file
	if *configFile != "" {
		loadConfig(srv, *configFile)
	}
	// Also load from env var: ENVIRONMENTS='[{"id":"...","name":"...","type":"...","endpoint":"..."}]'
	if envJSON := os.Getenv("ENVIRONMENTS"); envJSON != "" {
		var envs []models.Environment
		if err := json.Unmarshal([]byte(envJSON), &envs); err != nil {
			log.Fatalf("failed to parse ENVIRONMENTS: %v", err)
		}
		for i := range envs {
			srv.RegisterEnvironment(&envs[i])
			log.Printf("loaded environment: %s (%s)", envs[i].Name, envs[i].Type)
		}
	}
	// Shorthand single env from env vars
	if name := os.Getenv("ENV_NAME"); name != "" {
		env := models.Environment{
			ID:       os.Getenv("ENV_ID"),
			Name:     name,
			Type:     models.EnvType(os.Getenv("ENV_TYPE")),
			Endpoint: os.Getenv("ENV_ENDPOINT"),
			Token:    os.Getenv("ENV_TOKEN"),
			SkipTLS:  strings.EqualFold(os.Getenv("ENV_SKIP_TLS"), "true"),
		}
		if env.ID == "" {
			env.ID = "default"
		}
		srv.RegisterEnvironment(&env)
		log.Printf("loaded environment from env: %s (%s)", env.Name, env.Type)
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
		log.Printf("loaded environment: %s (%s) tunnel=%v", envs[i].Name, envs[i].Type, envs[i].Tunnel)
	}
}

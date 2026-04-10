package main

import (
	"container-hub/internal/api"
	"container-hub/internal/auth"
	"container-hub/internal/config"
	"container-hub/internal/discovery"
	"container-hub/internal/models"
	"container-hub/internal/tunnel"
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	configFile := flag.String("config", "", "path to config YAML file")
	legacyConfig := flag.String("legacy-config", "", "path to legacy environments JSON config file")
	flag.Parse()

	if v := os.Getenv("LISTEN_ADDR"); v != "" {
		*addr = v
	}
	if v := os.Getenv("CONFIG_FILE"); v != "" && *configFile == "" {
		*configFile = v
	}

	// Load config
	var cfg *config.Config
	if *configFile != "" {
		var err error
		cfg, err = config.Load(*configFile)
		if err != nil {
			log.Fatalf("failed to load config: %v", err)
		}
		if cfg.ListenAddr != "" && os.Getenv("LISTEN_ADDR") == "" {
			*addr = cfg.ListenAddr
		}
	} else {
		// Default config with a single admin user (password: admin)
		cfg = &config.Config{
			Users: []config.UserConfig{
				{
					Username: "admin",
					Password: "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy", // admin
					Admin:    true,
				},
			},
		}
		// Generate a random JWT secret since no config file is provided
		cfg.JWTSecret = config.GenerateRandomSecret()
		log.Printf("WARNING: No config file provided, using auto-generated JWT secret. Tokens will not survive restarts.")
	}

	authMgr := auth.NewManager(cfg)

	var srv *api.Server
	hub := tunnel.NewHub(
		func(id, name string, ctype models.ClusterType) { srv.ClusterJoined(id, name, ctype) },
		func(id string) { srv.ClusterLeft(id) },
	)
	srv = api.NewServer(hub, authMgr, cfg.AgentSecret)

	// Load manually configured environments from YAML config
	for _, envCfg := range cfg.Environments {
		env := &models.Environment{
			ID:        envCfg.ID,
			Name:      envCfg.Name,
			ClusterID: envCfg.ClusterID,
			Namespace: envCfg.Namespace,
		}
		if env.ID == "" {
			env.ID = envCfg.ClusterID + "-" + envCfg.Namespace
		}
		srv.RegisterEnvironment(env)
		log.Printf("loaded environment: %s (cluster=%s ns=%s)", env.Name, env.ClusterID, env.Namespace)
	}

	// Legacy JSON config support
	if *legacyConfig != "" {
		loadLegacyConfig(srv, *legacyConfig)
	}
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

	// Start environment auto-discovery (every 60 seconds)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	disc := discovery.New(hub, func(env *models.Environment) {
		srv.RegisterEnvironment(env)
	}, func(envID string) {
		srv.RemoveDiscoveredEnvironment(envID)
	}, 60*time.Second)
	go disc.Run(ctx, srv.GetClusters)

	httpSrv := &http.Server{
		Addr:    *addr,
		Handler: srv.Handler(),
	}

	go func() {
		<-ctx.Done()
		log.Printf("shutting down gracefully (10s timeout)...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown error: %v", err)
		}
	}()

	log.Printf("container-hub server listening on %s", *addr)
	if err := httpSrv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal(err)
	}
	log.Printf("server stopped")
}

func loadLegacyConfig(srv *api.Server, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("failed to read legacy config: %v", err)
	}
	var envs []models.Environment
	if err := json.Unmarshal(data, &envs); err != nil {
		log.Fatalf("failed to parse legacy config: %v", err)
	}
	for i := range envs {
		srv.RegisterEnvironment(&envs[i])
		log.Printf("loaded environment: %s (cluster=%s ns=%s)", envs[i].Name, envs[i].ClusterID, envs[i].Namespace)
	}
}

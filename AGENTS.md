# AGENTS.md — Guide for AI Agent Contributors

This document explains the codebase to AI coding agents working on `1container`.

## What This Project Does

Container Hub provides a unified web UI for viewing and troubleshooting containers across Docker Swarm, Kubernetes, and HashiCorp Nomad clusters. All clusters connect through reverse WebSocket tunnels via lightweight agents deployed in each target network. The server stores no credentials and no persistent state.

## Architecture

```
Browser ──HTTP/WS──▶ Server ──WebSocket tunnel──▶ Agent ──HTTP──▶ Orchestrator API
```

**Key concepts:**
- **Cluster**: A physical container platform. Auto-registered when its agent connects to the server. Identified by `cluster_id`.
- **Environment**: A tenant/namespace-scoped view into a cluster. Created via API or config file. References a `cluster_id` and optionally a `namespace`.
- **Agent**: Deployed in the target network. Opens an outbound WebSocket to the server, receives proxied HTTP requests, injects auth headers, and forwards to the local orchestrator API.

**Namespace rules:**
- Kubernetes and Nomad support namespaces natively. Providers scope API calls to the configured namespace.
- Docker Swarm has no namespace support. It is treated as a single-namespace solution (namespace is always empty).

**Auth flow:**
- The agent holds the auth token, not the server.
- On each proxied request, the agent injects the correct header based on cluster type:
  - Kubernetes / Docker Swarm: `Authorization: Bearer <token>`
  - Nomad: `X-Nomad-Token: <token>`
- Providers use `http://api` as a dummy host. The tunnel transport extracts the path and query only. The agent prepends its local endpoint.

## Project Layout

```
cmd/server/main.go          Server entry point. Loads config, creates Hub + API server.
cmd/agent/main.go           Agent entry point. Parses flags/env, creates AgentClient, runs forever.

internal/models/models.go   All shared types: Cluster, Environment, Container, ContainerDetail,
                            tunnel protocol (TunnelRequest/Response/Cancel), ExecRequest/Response.

internal/tunnel/hub.go      Server-side Hub. Manages agent connections keyed by clusterID.
                            Provides Transport(clusterID) returning an http.RoundTripper.
                            tunnelTransport serializes HTTP requests as TunnelRequest JSON.
                            Supports both buffered (RoundTrip) and streaming (RoundTripStream) modes.

internal/tunnel/agent.go    Agent-side client. Connects via WebSocket, reads TunnelRequest messages,
                            makes HTTP calls to LocalEndpoint, returns TunnelResponse messages.
                            Reconnects with exponential backoff (5s–60s) on failure.
                            handleStreamRequest sends chunked responses for follow/streaming.

internal/provider/provider.go   Provider interface (ListContainers, InspectContainer, ContainerLogs,
                                ExecContainer) and factory function. Config holds ClusterType,
                                Namespace, EnvID, EnvName. No auth fields — auth is agent-side.

internal/provider/docker.go     Docker Swarm provider. Uses Docker Engine API v1.41.
                                No namespace scoping (Swarm has no namespaces).

internal/provider/kube.go       Kubernetes provider. Uses /api/v1/namespaces/{ns}/pods.
                                Falls back to all namespaces if namespace is empty.

internal/provider/nomad.go      Nomad provider. Uses /v1/jobs and /v1/allocations.
                                Scopes to namespace via ?namespace= query param.

internal/api/handlers.go        HTTP router and handlers. Clusters are auto-registered via
                                ClusterJoined/ClusterLeft callbacks from the Hub. Environments
                                are CRUD via API. Containers are fetched per-environment in parallel.
                                Includes WebSocket handlers for live log streaming (/ws/logs/)
                                and interactive shell (/ws/shell/).

ui/index.html               Single-page app with three tabs: Containers, Environments, Clusters.
ui/style.css                Dark theme styles.
ui/app.js                   Frontend logic. Fetches from /api/*, opens WebSockets for logs/shell.
```

## Build & Run

Requires Go 1.25+.

```bash
go build -o bin/container-hub       ./cmd/server/
go build -o bin/container-hub-agent ./cmd/agent/
```

Server: `./bin/container-hub [-config environments.json] [-addr :8080]`
Agent:  `./bin/container-hub-agent -server ws://host:8080/ws/tunnel -cluster-id X -cluster-name Y -cluster-type Z -endpoint http://... [-token T] [-skip-tls]`

All flags have environment variable equivalents (see README.md).

## How to Add a New Orchestrator

1. Define a new `ClusterType` constant in `internal/models/models.go`.
2. Create `internal/provider/<name>.go` implementing the `Provider` interface.
3. Add a case to the `New()` factory in `internal/provider/provider.go`.
4. The agent already supports arbitrary cluster types — it just proxies HTTP. If the new orchestrator uses a different auth header, add a case to `authHeader()` in `internal/tunnel/agent.go`.
5. Update the UI's namespace logic in `ui/app.js` `updateNsVisibility()` if the new type has special namespace behavior.

## How to Add a New API Endpoint

1. Add the route in `NewServer()` in `internal/api/handlers.go`.
2. Write the handler method on `*Server`.
3. If it needs orchestrator data, call `s.providerFor(env)` to get a `Provider` that tunnels through the agent automatically.

## Testing

The PR workflow (`.github/workflows/pr.yml`) runs `go build`, `go vet`, and `go test -race ./...`. Unit tests exist for the `config`, `auth`, and `provider` packages. When adding tests, place them next to the code they test (`*_test.go` files).

## Common Patterns

- **Parallel environment queries**: `listContainers` fans out goroutines per environment and collects results via a channel.
- **Streaming via tunnel**: Set `Stream: true` on `TunnelRequest`. The agent sends chunked `TunnelResponse` messages with `Chunk: true` and a final `Done: true`. The hub's `RoundTripStream` returns an `io.ReadCloser` that pipes chunks through.
- **Provider HTTP calls**: All providers use the injected `*http.Client` whose transport is `tunnelTransport`. They make requests to `http://api/...` — the tunnel transport strips the host and sends only the path through the WebSocket. The agent prepends its local endpoint URL.

## Conventions

- Go code follows standard `gofmt` formatting.
- No external dependencies beyond `github.com/gorilla/websocket`.
- Frontend is plain HTML + CSS + vanilla JavaScript (no frameworks, no build step).
- The server is fully stateless — all data comes from connected agents and in-memory environment config.

# Container Hub

Unified view and troubleshooting access to containers across Docker Swarm, Kubernetes and HashiCorp Nomad.

All clusters connect through lightweight agents that open reverse WebSocket tunnels. The server is stateless — it proxies every request through the tunnel and the agent forwards it to the local orchestrator API. Authentication tokens are injected by the agent, never stored on the server.

```
                     ┌────────────┐
                     │  Browser   │
                     └─────┬──────┘
                           │ HTTP / WS
                     ┌─────┴──────┐
                     │   Server   │
                     │  (Go API)  │
                     └──┬──────┬──┘
            WebSocket   │      │   WebSocket
            tunnel      │      │   tunnel
              ┌─────────┘      └──────────┐
              ▼                           ▼
    ┌──────────────────┐       ┌──────────────────┐
    │     Agent A       │       │     Agent B       │
    │  ┌────────────┐   │       │  ┌────────────┐   │
    │  │ K8s cluster │   │       │  │ Swarm/Nomad│   │
    │  └────────────┘   │       │  └────────────┘   │
    └───────────────────┘       └───────────────────┘
```

## Concepts

| Term | Description |
|------|-------------|
| **Cluster** | A container platform (K8s, Swarm, Nomad). Auto-registered when its agent connects. |
| **Environment** | A tenant / namespace-scoped view into a cluster. K8s and Nomad map to namespaces; Docker Swarm has no namespace support and is treated as a single-namespace solution. |
| **Agent** | A small binary deployed in each target network. Opens an outbound WebSocket to the server and proxies API calls to the local orchestrator. |

A single cluster can have multiple environments (e.g. `default` and `monitoring` namespaces in the same K8s cluster). A user can have access to one or multiple environments.

## Quick Start

### Build

```bash
go build -o bin/container-hub       ./cmd/server/
go build -o bin/container-hub-agent ./cmd/agent/
```

### Run the server

```bash
# With a YAML config file (see config.yaml.example)
./bin/container-hub -config config.yaml

# Or with legacy JSON environments
ENVIRONMENTS='[{"id":"prod","name":"Production","cluster_id":"k8s-prod","namespace":"default"}]' \
  ./bin/container-hub

# Or configure environments at runtime via the UI
./bin/container-hub
```

Open `http://localhost:8080` in a browser.

### Run the agent

Deploy the agent inside each target network. It connects outbound to the server — no inbound firewall rules required.

```bash
./bin/container-hub-agent \
  -server       ws://hub-server:8080/ws/tunnel \
  -cluster-id   k8s-prod \
  -cluster-name "Production Kubernetes" \
  -cluster-type kubernetes \
  -endpoint     https://kubernetes.default.svc:6443 \
  -token        "$KUBE_TOKEN" \
  -skip-tls
```

Or with environment variables:

```bash
HUB_SERVER=ws://hub-server:8080/ws/tunnel \
CLUSTER_ID=k8s-prod \
CLUSTER_NAME="Production Kubernetes" \
CLUSTER_TYPE=kubernetes \
LOCAL_ENDPOINT=https://kubernetes.default.svc:6443 \
AUTH_TOKEN="$KUBE_TOKEN" \
SKIP_TLS=true \
  ./bin/container-hub-agent
```

The agent auto-reconnects on connection failure.

### Docker

```bash
# Build
docker build -t container-hub .
docker build -t container-hub-agent -f Dockerfile.agent .

# Run server
docker run -p 8080:8080 container-hub

# Run agent
docker run \
  -e HUB_SERVER=ws://hub-host:8080/ws/tunnel \
  -e CLUSTER_ID=k8s-prod \
  -e CLUSTER_NAME="Production Kubernetes" \
  -e CLUSTER_TYPE=kubernetes \
  -e LOCAL_ENDPOINT=https://kubernetes.default.svc:6443 \
  -e AUTH_TOKEN="$KUBE_TOKEN" \
  -e SKIP_TLS=true \
  container-hub-agent
```

## API

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/clusters` | List registered clusters (auto-populated by agents) |
| `GET` | `/api/environments` | List configured environments |
| `POST` | `/api/environments` | Create an environment |
| `DELETE` | `/api/environments/{id}` | Remove an environment |
| `GET` | `/api/containers` | List containers across all environments |
| `GET` | `/api/containers?env={id}` | List containers for one environment |
| `GET` | `/api/containers/{env}/{id}` | Inspect a container |
| `GET` | `/api/containers/{env}/{id}/logs?tail=200` | Fetch container logs |
| `POST` | `/api/containers/{env}/{id}/exec` | Execute a command (`{"cmd":["sh","-c","ls"]}`) |
| `WS` | `/ws/logs/{env}/{id}` | Stream container logs |
| `WS` | `/ws/shell/{env}/{id}` | Interactive shell |
| `WS` | `/ws/tunnel` | Agent reverse-tunnel endpoint |

## Cluster Types

| Type | `type` value | Orchestrator API | Namespace Support |
|------|-------------|------------------|-------------------|
| Docker Swarm | `docker-swarm` | Docker Engine API v1.41 | No (single namespace) |
| Kubernetes | `kubernetes` | Kubernetes API `/api/v1` | Yes |
| HashiCorp Nomad | `nomad` | Nomad HTTP API `/v1` | Yes |

## Configuration

### Environments config file

Environments reference clusters by `cluster_id`. Clusters are auto-registered when their agents connect.

```json
[
  {
    "id": "prod-k8s-default",
    "name": "Production (default)",
    "cluster_id": "k8s-prod",
    "namespace": "default"
  },
  {
    "id": "staging-swarm",
    "name": "Staging Swarm",
    "cluster_id": "swarm-staging",
    "namespace": ""
  }
]
```

### Server environment variables

| Variable | Description |
|----------|-------------|
| `LISTEN_ADDR` | Listen address (default `:8080`) |
| `CONFIG_FILE` | Path to YAML config file (see `config.yaml.example`) |
| `ENVIRONMENTS` | JSON array of environments (legacy format) |

### Agent environment variables

| Variable | Description |
|----------|-------------|
| `HUB_SERVER` | WebSocket URL of the server (`ws://host:port/ws/tunnel`) |
| `CLUSTER_ID` | Unique cluster identifier |
| `CLUSTER_NAME` | Human-readable cluster name |
| `CLUSTER_TYPE` | Cluster type: `docker-swarm`, `kubernetes`, or `nomad` |
| `LOCAL_ENDPOINT` | Local orchestrator API URL |
| `AUTH_TOKEN` | Auth token for the local orchestrator (injected by agent) |
| `SKIP_TLS` | Skip TLS verification for local endpoint and hub (`true`/`false`) |

## How the Tunnel Works

1. The agent opens an outbound WebSocket connection to the server at `/ws/tunnel`
2. Query parameters identify the cluster: `cluster_id`, `cluster_name`, `cluster_type`
3. The server auto-registers the cluster and marks it as online
4. When the UI requests data, the server serializes the HTTP request as JSON and sends it through the WebSocket
5. The agent receives the request, injects the auth header (Bearer token for K8s/Docker, X-Nomad-Token for Nomad), and forwards it to the local orchestrator
6. The response travels back through the WebSocket to the server and then to the browser
7. Streaming (logs with `follow=true`) uses chunked tunnel messages so data flows in real time

Only outbound connections are required from the agent — no firewall changes needed.

## Graceful Shutdown

The server handles `SIGINT` and `SIGTERM` signals gracefully. On shutdown it stops accepting new connections and waits up to 10 seconds for in-flight requests (including WebSocket streams) to complete before exiting.

## Project Structure

```
├── cmd/
│   ├── server/main.go          # Server entry point (graceful shutdown)
│   └── agent/main.go           # Agent entry point
├── internal/
│   ├── models/models.go        # Cluster, Environment, Container types + tunnel protocol
│   ├── tunnel/
│   │   ├── hub.go              # Server-side tunnel hub (keyed by clusterID)
│   │   └── agent.go            # Agent-side tunnel client (auth injection)
│   ├── provider/
│   │   ├── provider.go         # Provider interface + factory
│   │   ├── docker.go           # Docker Swarm provider
│   │   ├── kube.go             # Kubernetes provider
│   │   └── nomad.go            # Nomad provider
│   ├── api/handlers.go         # HTTP/WS handlers + router
│   ├── auth/auth.go            # JWT authentication + RBAC
│   ├── config/config.go        # YAML configuration loading
│   └── discovery/discovery.go  # Auto-discovery of environments
├── ui/
│   ├── index.html              # Three tabs: Containers, Environments, Clusters
│   ├── style.css               # Dark theme
│   └── app.js                  # UI logic with WebSocket streaming
├── Dockerfile                  # Server container image
├── Dockerfile.agent            # Agent container image
├── docker-compose.yml
├── config.yaml.example         # Full YAML config with auth and groups
├── environments.json.example   # Legacy JSON config
└── AGENTS.md                   # Guide for AI agent contributors
```

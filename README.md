# Container Hub

Unified view and troubleshooting access to containers across Docker Swarm, Kubernetes and HashiCorp Nomad.

The server is stateless — it proxies every request to the target orchestrator API and lets that handle authentication. Environments behind firewalls are supported through a lightweight agent that opens a reverse WebSocket tunnel.

```
                     ┌────────────┐
                     │  Browser   │
                     └─────┬──────┘
                           │ HTTP
                     ┌─────┴──────┐
                     │   Server   │
                     │  (Go API)  │
                     └──┬──────┬──┘
            Direct HTTP │      │ WebSocket tunnel
                        │      │
              ┌─────────┘      └──────────┐
              ▼                           ▼
    ┌──────────────────┐       ┌──────────────────┐
    │  K8s / Swarm /   │       │     Agent         │
    │  Nomad (direct)  │       │  (behind FW)      │
    └──────────────────┘       │  ┌────────────┐   │
                               │  │ K8s/Swarm/ │   │
                               │  │   Nomad    │   │
                               │  └────────────┘   │
                               └───────────────────┘
```

## Quick Start

### Build

```bash
go build -o bin/container-hub       ./cmd/server/
go build -o bin/container-hub-agent ./cmd/agent/
```

### Run the server

```bash
# With a config file
./bin/container-hub -config environments.json

# Or with env vars for a single environment
ENV_NAME="My K8s" ENV_TYPE=kubernetes ENV_ENDPOINT=https://kube:6443 ENV_TOKEN=xxx ./bin/container-hub

# Or register environments at runtime via the API / UI
./bin/container-hub
```

Open `http://localhost:8080` in a browser.

### Run the agent (for firewalled environments)

Deploy the agent inside the target network. It connects outbound to the hub server.

```bash
./bin/container-hub-agent \
  -server  ws://hub-server:8080/ws/tunnel \
  -env-id  swarm-dc1 \
  -env-name "DC1 Docker Swarm" \
  -env-type docker-swarm \
  -endpoint http://localhost:2375
```

Or with environment variables:

```bash
HUB_SERVER=ws://hub-server:8080/ws/tunnel \
ENV_ID=swarm-dc1 \
ENV_NAME="DC1 Docker Swarm" \
ENV_TYPE=docker-swarm \
LOCAL_ENDPOINT=http://localhost:2375 \
./bin/container-hub-agent
```

The agent auto-reconnects if the connection drops.

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
  -e ENV_ID=k8s-prod \
  -e ENV_NAME="Production K8s" \
  -e ENV_TYPE=kubernetes \
  -e LOCAL_ENDPOINT=https://kubernetes.default.svc:6443 \
  -e SKIP_TLS=true \
  container-hub-agent
```

## API

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/environments` | List registered environments |
| `POST` | `/api/environments` | Register an environment |
| `DELETE` | `/api/environments/{id}` | Remove an environment |
| `GET` | `/api/containers` | List containers across all environments |
| `GET` | `/api/containers?env={id}` | List containers for one environment |
| `GET` | `/api/containers/{env}/{id}` | Inspect a container |
| `GET` | `/api/containers/{env}/{id}/logs?tail=200` | Fetch container logs |
| `POST` | `/api/containers/{env}/{id}/exec` | Execute a command (`{"cmd":["sh","-c","ls"]}`) |
| `WS` | `/ws/tunnel` | Agent reverse-tunnel endpoint |

## Environment Types

| Type | `type` value | Orchestrator API used |
|------|-------------|----------------------|
| Docker Swarm | `docker-swarm` | Docker Engine API v1.41 |
| Kubernetes | `kubernetes` | Kubernetes API `/api/v1` |
| HashiCorp Nomad | `nomad` | Nomad HTTP API `/v1` |

## Configuration

### Config file (`environments.json`)

```json
[
  {
    "id": "k8s-prod",
    "name": "Production Kubernetes",
    "type": "kubernetes",
    "endpoint": "https://kube-apiserver:6443",
    "token": "bearer-token",
    "skip_tls": true,
    "tunnel": false
  }
]
```

### Environment variables

| Variable | Description |
|----------|-------------|
| `LISTEN_ADDR` | Server listen address (default `:8080`) |
| `ENVIRONMENTS` | JSON array of environments |
| `ENV_NAME` | Single environment name |
| `ENV_TYPE` | Single environment type |
| `ENV_ENDPOINT` | Single environment API URL |
| `ENV_TOKEN` | Single environment auth token |
| `ENV_SKIP_TLS` | Skip TLS verification (`true`/`false`) |

### Agent environment variables

| Variable | Description |
|----------|-------------|
| `HUB_SERVER` | WebSocket URL of the hub server |
| `ENV_ID` | Unique environment identifier |
| `ENV_NAME` | Human-readable environment name |
| `ENV_TYPE` | Environment type |
| `LOCAL_ENDPOINT` | Local orchestrator API URL |
| `AGENT_TOKEN` | Shared secret for authentication |
| `SKIP_TLS` | Skip TLS verification for local endpoint |

## How the tunnel works

1. The agent opens an outbound WebSocket connection to the server at `/ws/tunnel`
2. The server registers the agent as the tunnel for that environment
3. When the UI requests data from a tunneled environment, the server serializes the HTTP request as JSON and sends it through the WebSocket
4. The agent receives the request, makes the HTTP call to the local orchestrator, and returns the response through the WebSocket
5. The server deserializes the response and returns it to the UI

Only outbound connections are required from the agent, so no firewall changes are needed.

## Project Structure

```
├── cmd/
│   ├── server/main.go          # Server entry point
│   └── agent/main.go           # Agent entry point
├── internal/
│   ├── models/models.go        # Shared types
│   ├── tunnel/
│   │   ├── hub.go              # Server-side tunnel management
│   │   └── agent.go            # Agent-side tunnel client
│   ├── provider/
│   │   ├── provider.go         # Provider interface + factory
│   │   ├── docker.go           # Docker Swarm provider
│   │   ├── kube.go             # Kubernetes provider
│   │   └── nomad.go            # Nomad provider
│   └── api/handlers.go         # HTTP handlers + router
├── ui/
│   ├── index.html
│   ├── style.css
│   └── app.js
├── Dockerfile                  # Server container image
├── Dockerfile.agent            # Agent container image
├── docker-compose.yml
└── environments.json.example
```

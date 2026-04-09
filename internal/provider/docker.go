package provider

import (
	"container-hub/internal/models"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DockerProvider talks to the Docker Engine API (works for standalone and Swarm).
type DockerProvider struct {
	client *http.Client
	base   string
	token  string
	env    *models.Environment
}

func (d *DockerProvider) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	url := d.base + path
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	if d.token != "" {
		req.Header.Set("Authorization", "Bearer "+d.token)
	}
	req.Header.Set("Content-Type", "application/json")
	return d.client.Do(req)
}

// dockerContainer is the subset of fields we read from Docker's /containers/json.
type dockerContainer struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	State   string            `json:"State"`
	Status  string            `json:"Status"`
	Created int64             `json:"Created"`
	Labels  map[string]string `json:"Labels"`
	Ports   []struct {
		PrivatePort int    `json:"PrivatePort"`
		PublicPort  int    `json:"PublicPort"`
		Type        string `json:"Type"`
	} `json:"Ports"`
	HostConfig struct {
		NetworkMode string `json:"NetworkMode"`
	} `json:"HostConfig"`
	NetworkSettings struct {
		Networks map[string]interface{} `json:"Networks"`
	} `json:"NetworkSettings"`
}

type dockerInspect struct {
	ID      string `json:"Id"`
	Name    string `json:"Name"`
	State   struct {
		Status     string `json:"Status"`
		Running    bool   `json:"Running"`
		StartedAt  string `json:"StartedAt"`
		FinishedAt string `json:"FinishedAt"`
	} `json:"State"`
	Config struct {
		Image string   `json:"Image"`
		Cmd   []string `json:"Cmd"`
		Env   []string `json:"Env"`
	} `json:"Config"`
	Mounts []struct {
		Type        string `json:"Type"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
	} `json:"Mounts"`
	HostConfig struct {
		RestartPolicy struct {
			MaximumRetryCount int `json:"MaximumRetryCount"`
		} `json:"RestartPolicy"`
	} `json:"HostConfig"`
	NetworkSettings struct {
		Ports map[string][]struct {
			HostPort string `json:"HostPort"`
		} `json:"Ports"`
	} `json:"NetworkSettings"`
}

// node returns the node name, falling back to "local".
func (d *DockerProvider) node(ctx context.Context) string {
	resp, err := d.do(ctx, "GET", "/info", nil)
	if err != nil {
		return "local"
	}
	defer resp.Body.Close()
	var info struct {
		Name string `json:"Name"`
	}
	json.NewDecoder(resp.Body).Decode(&info)
	if info.Name != "" {
		return info.Name
	}
	return "local"
}

func (d *DockerProvider) ListContainers(ctx context.Context) ([]models.Container, error) {
	resp, err := d.do(ctx, "GET", "/v1.41/containers/json?all=true", nil)
	if err != nil {
		return nil, fmt.Errorf("docker list: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("docker list: %s – %s", resp.Status, string(b))
	}

	var dcs []dockerContainer
	if err := json.NewDecoder(resp.Body).Decode(&dcs); err != nil {
		return nil, fmt.Errorf("docker list decode: %w", err)
	}

	nodeName := d.node(ctx)

	out := make([]models.Container, 0, len(dcs))
	for _, dc := range dcs {
		name := ""
		if len(dc.Names) > 0 {
			name = strings.TrimPrefix(dc.Names[0], "/")
		}
		out = append(out, models.Container{
			ID:        dc.ID[:12],
			Name:      name,
			Image:     dc.Image,
			Status:    dc.Status,
			State:     dc.State,
			EnvID:     d.env.ID,
			EnvName:   d.env.Name,
			EnvType:   d.env.Type,
			Node:      nodeName,
			CreatedAt: time.Unix(dc.Created, 0).UTC().Format(time.RFC3339),
			Labels:    dc.Labels,
		})
	}
	return out, nil
}

func (d *DockerProvider) InspectContainer(ctx context.Context, id string) (*models.ContainerDetail, error) {
	resp, err := d.do(ctx, "GET", "/v1.41/containers/"+id+"/json", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("docker inspect: %s – %s", resp.Status, string(b))
	}

	var di dockerInspect
	if err := json.NewDecoder(resp.Body).Decode(&di); err != nil {
		return nil, err
	}

	detail := &models.ContainerDetail{
		Container: models.Container{
			ID:      di.ID[:12],
			Name:    strings.TrimPrefix(di.Name, "/"),
			Image:   di.Config.Image,
			State:   di.State.Status,
			Status:  di.State.Status,
			EnvID:   d.env.ID,
			EnvName: d.env.Name,
			EnvType: d.env.Type,
		},
		Env:     di.Config.Env,
		Command: strings.Join(di.Config.Cmd, " "),
	}
	for _, m := range di.Mounts {
		detail.Mounts = append(detail.Mounts, models.Mount{
			Source: m.Source,
			Target: m.Destination,
			Type:   m.Type,
		})
	}
	return detail, nil
}

func (d *DockerProvider) ContainerLogs(ctx context.Context, id string, tail int) (io.ReadCloser, error) {
	path := fmt.Sprintf("/v1.41/containers/%s/logs?stdout=1&stderr=1&tail=%d&timestamps=1", id, tail)
	resp, err := d.do(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("docker logs: %s", resp.Status)
	}
	return resp.Body, nil
}

type dockerExecCreate struct {
	ID string `json:"Id"`
}

type dockerExecInspect struct {
	ExitCode int `json:"ExitCode"`
}

func (d *DockerProvider) ExecContainer(ctx context.Context, id string, cmd []string) (*models.ExecResponse, error) {
	// Create exec instance
	payload := map[string]interface{}{
		"AttachStdout": true,
		"AttachStderr": true,
		"Cmd":          cmd,
	}
	payloadBytes, _ := json.Marshal(payload)
	resp, err := d.do(ctx, "POST", "/v1.41/containers/"+id+"/exec", strings.NewReader(string(payloadBytes)))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("docker exec create: %s – %s", resp.Status, string(b))
	}

	var execCreate dockerExecCreate
	json.NewDecoder(resp.Body).Decode(&execCreate)

	// Start exec
	startPayload := `{"Detach":false,"Tty":false}`
	startResp, err := d.do(ctx, "POST", "/v1.41/exec/"+execCreate.ID+"/start", strings.NewReader(startPayload))
	if err != nil {
		return nil, err
	}
	defer startResp.Body.Close()
	output, _ := io.ReadAll(startResp.Body)

	// Inspect for exit code
	inspResp, err := d.do(ctx, "GET", "/v1.41/exec/"+execCreate.ID+"/json", nil)
	exitCode := 0
	if err == nil {
		defer inspResp.Body.Close()
		var ei dockerExecInspect
		json.NewDecoder(inspResp.Body).Decode(&ei)
		exitCode = ei.ExitCode
	}

	// Strip Docker stream headers (8-byte prefix per frame)
	cleaned := stripDockerStream(output)

	return &models.ExecResponse{Output: string(cleaned), ExitCode: exitCode}, nil
}

// stripDockerStream removes the 8-byte header from each Docker multiplexed stream frame.
func stripDockerStream(data []byte) []byte {
	var out []byte
	for len(data) >= 8 {
		size := int(data[4])<<24 | int(data[5])<<16 | int(data[6])<<8 | int(data[7])
		data = data[8:]
		if size > len(data) {
			size = len(data)
		}
		out = append(out, data[:size]...)
		data = data[size:]
	}
	if len(out) == 0 {
		return data // fallback: return as-is
	}
	return out
}

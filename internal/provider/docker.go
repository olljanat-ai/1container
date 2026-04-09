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

// DockerProvider talks to the Docker Engine API (standalone and Swarm).
// Auth is injected by the agent; the provider sends plain requests.
type DockerProvider struct {
	client *http.Client
	cfg    Config
}

func (d *DockerProvider) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://api"+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return d.client.Do(req)
}

type dockerContainer struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	State   string            `json:"State"`
	Status  string            `json:"Status"`
	Created int64             `json:"Created"`
	Labels  map[string]string `json:"Labels"`
}

type dockerInspect struct {
	ID    string `json:"Id"`
	Name  string `json:"Name"`
	State struct {
		Status string `json:"Status"`
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
}

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
			ID:          truncateID(dc.ID, 12),
			Name:        name,
			Image:       dc.Image,
			Status:      dc.Status,
			State:       dc.State,
			EnvID:       d.cfg.EnvID,
			EnvName:     d.cfg.EnvName,
			ClusterType: models.ClusterDockerSwarm,
			Node:        nodeName,
			CreatedAt:   time.Unix(dc.Created, 0).UTC().Format(time.RFC3339),
			Labels:      dc.Labels,
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
			ID:          truncateID(di.ID, 12),
			Name:        strings.TrimPrefix(di.Name, "/"),
			Image:       di.Config.Image,
			State:       di.State.Status,
			Status:      di.State.Status,
			EnvID:       d.cfg.EnvID,
			EnvName:     d.cfg.EnvName,
			ClusterType: models.ClusterDockerSwarm,
		},
		Env:     di.Config.Env,
		Command: strings.Join(di.Config.Cmd, " "),
	}
	for _, m := range di.Mounts {
		detail.Mounts = append(detail.Mounts, models.Mount{
			Source: m.Source, Target: m.Destination, Type: m.Type,
		})
	}
	return detail, nil
}

func (d *DockerProvider) ContainerLogs(ctx context.Context, id string, tail int, follow bool) (io.ReadCloser, error) {
	path := fmt.Sprintf("/v1.41/containers/%s/logs?stdout=1&stderr=1&tail=%d&timestamps=1", id, tail)
	if follow {
		path += "&follow=1"
	}
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

func (d *DockerProvider) ExecContainer(ctx context.Context, id string, cmd []string) (*models.ExecResponse, error) {
	payload, _ := json.Marshal(map[string]interface{}{
		"AttachStdout": true, "AttachStderr": true, "Cmd": cmd,
	})
	resp, err := d.do(ctx, "POST", "/v1.41/containers/"+id+"/exec", strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("docker exec create: %s – %s", resp.Status, string(b))
	}
	var execCreate struct {
		ID string `json:"Id"`
	}
	json.NewDecoder(resp.Body).Decode(&execCreate)

	startResp, err := d.do(ctx, "POST", "/v1.41/exec/"+execCreate.ID+"/start",
		strings.NewReader(`{"Detach":false,"Tty":false}`))
	if err != nil {
		return nil, err
	}
	defer startResp.Body.Close()
	output, _ := io.ReadAll(startResp.Body)

	inspResp, _ := d.do(ctx, "GET", "/v1.41/exec/"+execCreate.ID+"/json", nil)
	exitCode := 0
	if inspResp != nil {
		defer inspResp.Body.Close()
		var ei struct {
			ExitCode int `json:"ExitCode"`
		}
		json.NewDecoder(inspResp.Body).Decode(&ei)
		exitCode = ei.ExitCode
	}

	return &models.ExecResponse{Output: string(stripDockerStream(output)), ExitCode: exitCode}, nil
}

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
		return data
	}
	return out
}

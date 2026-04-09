package provider

import (
	"container-hub/internal/models"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// NomadProvider talks to the HashiCorp Nomad HTTP API.
type NomadProvider struct {
	client *http.Client
	base   string
	token  string
	env    *models.Environment
}

func (n *NomadProvider) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	u := n.base + path
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if n.token != "" {
		req.Header.Set("X-Nomad-Token", n.token)
	}
	req.Header.Set("Accept", "application/json")
	return n.client.Do(req)
}

type nomadAlloc struct {
	ID                string `json:"ID"`
	Name              string `json:"Name"`
	TaskGroup         string `json:"TaskGroup"`
	JobID             string `json:"JobID"`
	ClientStatus      string `json:"ClientStatus"`
	ClientDescription string `json:"ClientDescription"`
	NodeID            string `json:"NodeID"`
	NodeName          string `json:"NodeName"`
	CreateTime        int64  `json:"CreateTime"` // nanoseconds
	TaskStates        map[string]struct {
		State     string `json:"State"`
		Restarts  int    `json:"Restarts"`
		StartedAt string `json:"StartedAt"`
		Events    []struct {
			Type    string `json:"Type"`
			Message string `json:"Message"`
		} `json:"Events"`
	} `json:"TaskStates"`
	AllocatedResources struct {
		Tasks map[string]struct {
			Networks []struct {
				DynamicPorts []struct {
					Label string `json:"Label"`
					Value int    `json:"Value"`
				} `json:"DynamicPorts"`
			} `json:"Networks"`
		} `json:"Tasks"`
	} `json:"AllocatedResources"`
}

type nomadAllocDetail struct {
	nomadAlloc
	Job struct {
		ID         string `json:"ID"`
		Name       string `json:"Name"`
		TaskGroups []struct {
			Name  string `json:"Name"`
			Tasks []struct {
				Name   string `json:"Name"`
				Driver string `json:"Driver"`
				Config map[string]interface{} `json:"Config"`
				Env    map[string]string      `json:"Env"`
			} `json:"Tasks"`
		} `json:"TaskGroups"`
	} `json:"Job"`
}

func (n *NomadProvider) ListContainers(ctx context.Context) ([]models.Container, error) {
	resp, err := n.do(ctx, "GET", "/v1/allocations?task_states=true", nil)
	if err != nil {
		return nil, fmt.Errorf("nomad list: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("nomad list: %s – %s", resp.Status, string(b))
	}

	var allocs []nomadAlloc
	if err := json.NewDecoder(resp.Body).Decode(&allocs); err != nil {
		return nil, err
	}

	var out []models.Container
	for _, a := range allocs {
		// Create one entry per task in the allocation
		for taskName, ts := range a.TaskStates {
			image := ""
			status := a.ClientDescription
			if status == "" {
				status = a.ClientStatus
			}

			out = append(out, models.Container{
				ID:        a.ID[:8] + "/" + taskName,
				Name:      a.JobID + "." + a.TaskGroup + "." + taskName,
				Image:     image,
				Status:    status,
				State:     ts.State,
				EnvID:     n.env.ID,
				EnvName:   n.env.Name,
				EnvType:   n.env.Type,
				Node:      a.NodeName,
				CreatedAt: ts.StartedAt,
				Extra: map[string]string{
					"alloc_id":   a.ID,
					"job_id":     a.JobID,
					"task_group": a.TaskGroup,
					"task":       taskName,
				},
			})
		}
	}
	return out, nil
}

func (n *NomadProvider) InspectContainer(ctx context.Context, id string) (*models.ContainerDetail, error) {
	allocID, taskName := parseNomadID(id)
	resp, err := n.do(ctx, "GET", "/v1/allocation/"+allocID, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("nomad inspect: %s – %s", resp.Status, string(b))
	}

	var ad nomadAllocDetail
	if err := json.NewDecoder(resp.Body).Decode(&ad); err != nil {
		return nil, err
	}

	detail := &models.ContainerDetail{
		Container: models.Container{
			ID:      id,
			Name:    ad.JobID + "." + ad.TaskGroup + "." + taskName,
			State:   ad.ClientStatus,
			Status:  ad.ClientDescription,
			EnvID:   n.env.ID,
			EnvName: n.env.Name,
			EnvType: n.env.Type,
			Node:    ad.NodeName,
			Extra: map[string]string{
				"alloc_id":   allocID,
				"job_id":     ad.JobID,
				"task_group": ad.TaskGroup,
				"task":       taskName,
			},
		},
	}

	// Find the task in the job spec
	for _, tg := range ad.Job.TaskGroups {
		for _, t := range tg.Tasks {
			if t.Name == taskName {
				detail.Command = t.Driver
				if img, ok := t.Config["image"].(string); ok {
					detail.Image = img
				}
				for k, v := range t.Env {
					detail.Env = append(detail.Env, k+"="+v)
				}
			}
		}
	}

	if ts, ok := ad.TaskStates[taskName]; ok {
		detail.RestartCount = ts.Restarts
	}

	return detail, nil
}

func (n *NomadProvider) ContainerLogs(ctx context.Context, id string, tail int, follow bool) (io.ReadCloser, error) {
	allocID, taskName := parseNomadID(id)
	path := fmt.Sprintf("/v1/client/fs/logs/%s?task=%s&type=stderr&origin=end&offset=50000&plain=true",
		allocID, taskName)
	if follow {
		path += "&follow=true"
	}
	resp, err := n.do(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		// Try stdout instead
		path = fmt.Sprintf("/v1/client/fs/logs/%s?task=%s&type=stdout&origin=end&offset=50000&plain=true",
			allocID, taskName)
		resp, err = n.do(ctx, "GET", path, nil)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			return nil, fmt.Errorf("nomad logs: %s", resp.Status)
		}
	}
	return resp.Body, nil
}

func (n *NomadProvider) ExecContainer(ctx context.Context, id string, cmd []string) (*models.ExecResponse, error) {
	allocID, taskName := parseNomadID(id)

	payload := map[string]interface{}{
		"command": cmd[0],
	}
	if len(cmd) > 1 {
		payload["args"] = cmd[1:]
	}
	payloadBytes, _ := json.Marshal(payload)

	path := fmt.Sprintf("/v1/client/allocation/%s/exec?task=%s", allocID, taskName)
	resp, err := n.do(ctx, "POST", path, strings.NewReader(string(payloadBytes)))
	if err != nil {
		return nil, fmt.Errorf("nomad exec: %w", err)
	}
	defer resp.Body.Close()
	output, _ := io.ReadAll(resp.Body)

	return &models.ExecResponse{Output: string(output), ExitCode: 0}, nil
}

// parseNomadID splits "allocShort/taskName" back.
func parseNomadID(id string) (string, string) {
	parts := strings.SplitN(id, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return id, "main"
}

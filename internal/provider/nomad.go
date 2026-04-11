package provider

import (
	"bytes"
	"container-hub/internal/models"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// NomadProvider talks to the HashiCorp Nomad HTTP API, scoped to a namespace.
type NomadProvider struct {
	client *http.Client
	cfg    Config
}

func (n *NomadProvider) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://api"+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	return n.client.Do(req)
}

// nsParam returns the namespace query param fragment. Empty for "default".
func (n *NomadProvider) nsParam(prefix string) string {
	ns := n.cfg.Namespace
	if ns == "" || ns == "default" {
		return ""
	}
	return prefix + "namespace=" + url.QueryEscape(ns)
}

type nomadAlloc struct {
	ID                string `json:"ID"`
	Name              string `json:"Name"`
	TaskGroup         string `json:"TaskGroup"`
	JobID             string `json:"JobID"`
	ClientStatus      string `json:"ClientStatus"`
	ClientDescription string `json:"ClientDescription"`
	NodeName          string `json:"NodeName"`
	TaskStates        map[string]struct {
		State     string `json:"State"`
		Restarts  int    `json:"Restarts"`
		StartedAt string `json:"StartedAt"`
	} `json:"TaskStates"`
}

type nomadAllocDetail struct {
	nomadAlloc
	Job struct {
		ID         string `json:"ID"`
		Name       string `json:"Name"`
		TaskGroups []struct {
			Name  string `json:"Name"`
			Tasks []struct {
				Name   string                 `json:"Name"`
				Driver string                 `json:"Driver"`
				Config map[string]interface{} `json:"Config"`
				Env    map[string]string      `json:"Env"`
			} `json:"Tasks"`
		} `json:"TaskGroups"`
	} `json:"Job"`
}

func (n *NomadProvider) ListContainers(ctx context.Context) ([]models.Container, error) {
	path := "/v1/allocations?task_states=true" + n.nsParam("&")
	resp, err := n.do(ctx, "GET", path, nil)
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

	out := make([]models.Container, 0)
	for _, a := range allocs {
		for taskName, ts := range a.TaskStates {
			status := a.ClientDescription
			if status == "" {
				status = a.ClientStatus
			}
			out = append(out, models.Container{
				ID:          a.ID + "/" + taskName,
				Name:        a.JobID + "." + a.TaskGroup + "." + taskName,
				Status:      status,
				State:       ts.State,
				EnvID:       n.cfg.EnvID,
				EnvName:     n.cfg.EnvName,
				ClusterType: models.ClusterNomad,
				Namespace:   n.cfg.Namespace,
				Node:        a.NodeName,
				CreatedAt:   ts.StartedAt,
				Extra: map[string]string{
					"alloc_id": a.ID, "job_id": a.JobID,
					"task_group": a.TaskGroup, "task": taskName,
				},
			})
		}
	}
	return out, nil
}

func (n *NomadProvider) InspectContainer(ctx context.Context, id string) (*models.ContainerDetail, error) {
	allocID, taskName := parseNomadID(id)
	resp, err := n.do(ctx, "GET", "/v1/allocation/"+allocID+n.nsParam("?"), nil)
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
			ID:          id,
			Name:        ad.JobID + "." + ad.TaskGroup + "." + taskName,
			State:       ad.ClientStatus,
			Status:      ad.ClientDescription,
			EnvID:       n.cfg.EnvID,
			EnvName:     n.cfg.EnvName,
			ClusterType: models.ClusterNomad,
			Namespace:   n.cfg.Namespace,
			Node:        ad.NodeName,
			Extra: map[string]string{
				"alloc_id": allocID, "job_id": ad.JobID,
				"task_group": ad.TaskGroup, "task": taskName,
			},
		},
	}
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
	path += n.nsParam("&")
	if follow {
		path += "&follow=true"
	}
	resp, err := n.do(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		// Fallback to stdout
		path = fmt.Sprintf("/v1/client/fs/logs/%s?task=%s&type=stdout&origin=end&offset=50000&plain=true",
			allocID, taskName)
		path += n.nsParam("&")
		if follow {
			path += "&follow=true"
		}
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
	payload := map[string]interface{}{"command": cmd[0]}
	if len(cmd) > 1 {
		payload["args"] = cmd[1:]
	}
	payloadBytes, _ := json.Marshal(payload)
	path := fmt.Sprintf("/v1/client/allocation/%s/exec?task=%s", allocID, taskName)
	path += n.nsParam("&")
	resp, err := n.do(ctx, "POST", path, bytes.NewReader(payloadBytes))
	if err != nil {
		return nil, fmt.Errorf("nomad exec: %w", err)
	}
	defer resp.Body.Close()
	output, _ := io.ReadAll(resp.Body)
	return &models.ExecResponse{Output: string(output), ExitCode: 0}, nil
}

func parseNomadID(id string) (string, string) {
	parts := strings.SplitN(id, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return id, "main"
}

package provider

import (
	"container-hub/internal/models"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// KubeProvider talks to the Kubernetes API server.
type KubeProvider struct {
	client *http.Client
	base   string
	token  string
	env    *models.Environment
}

func (k *KubeProvider) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	u := k.base + path
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if k.token != "" {
		req.Header.Set("Authorization", "Bearer "+k.token)
	}
	req.Header.Set("Accept", "application/json")
	return k.client.Do(req)
}

// Minimal Kubernetes API structs.
type k8sPodList struct {
	Items []k8sPod `json:"items"`
}

type k8sPod struct {
	Metadata struct {
		Name              string            `json:"name"`
		Namespace         string            `json:"namespace"`
		UID               string            `json:"uid"`
		CreationTimestamp string            `json:"creationTimestamp"`
		Labels            map[string]string `json:"labels"`
	} `json:"metadata"`
	Spec struct {
		NodeName   string `json:"nodeName"`
		Containers []struct {
			Name    string   `json:"name"`
			Image   string   `json:"image"`
			Command []string `json:"command"`
			Ports   []struct {
				ContainerPort int    `json:"containerPort"`
				Protocol      string `json:"protocol"`
			} `json:"ports"`
			VolumeMounts []struct {
				Name      string `json:"name"`
				MountPath string `json:"mountPath"`
			} `json:"volumeMounts"`
			Env []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"env"`
		} `json:"containers"`
	} `json:"spec"`
	Status struct {
		Phase             string `json:"phase"`
		ContainerStatuses []struct {
			Name         string `json:"name"`
			ContainerID  string `json:"containerID"`
			Ready        bool   `json:"ready"`
			RestartCount int    `json:"restartCount"`
			State        struct {
				Running    *struct{ StartedAt string } `json:"running"`
				Waiting    *struct{ Reason string }    `json:"waiting"`
				Terminated *struct{ Reason string }    `json:"terminated"`
			} `json:"state"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

func (k *KubeProvider) ListContainers(ctx context.Context) ([]models.Container, error) {
	resp, err := k.do(ctx, "GET", "/api/v1/pods", nil)
	if err != nil {
		return nil, fmt.Errorf("k8s list: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("k8s list: %s – %s", resp.Status, string(b))
	}

	var pl k8sPodList
	if err := json.NewDecoder(resp.Body).Decode(&pl); err != nil {
		return nil, err
	}

	var out []models.Container
	for _, pod := range pl.Items {
		state := strings.ToLower(string(pod.Status.Phase))
		status := string(pod.Status.Phase)
		image := ""
		if len(pod.Spec.Containers) > 0 {
			image = pod.Spec.Containers[0].Image
		}
		// Detailed state from container statuses
		if len(pod.Status.ContainerStatuses) > 0 {
			cs := pod.Status.ContainerStatuses[0]
			if cs.State.Waiting != nil {
				status = cs.State.Waiting.Reason
				state = "waiting"
			} else if cs.State.Terminated != nil {
				status = cs.State.Terminated.Reason
				state = "terminated"
			}
		}

		extra := map[string]string{
			"namespace": pod.Metadata.Namespace,
		}
		// Use namespace/name as the compound ID for K8s
		compoundID := pod.Metadata.Namespace + "/" + pod.Metadata.Name
		out = append(out, models.Container{
			ID:        compoundID,
			Name:      pod.Metadata.Name,
			Image:     image,
			Status:    status,
			State:     state,
			EnvID:     k.env.ID,
			EnvName:   k.env.Name,
			EnvType:   k.env.Type,
			Node:      pod.Spec.NodeName,
			CreatedAt: pod.Metadata.CreationTimestamp,
			Labels:    pod.Metadata.Labels,
			Extra:     extra,
		})
	}
	return out, nil
}

func (k *KubeProvider) InspectContainer(ctx context.Context, id string) (*models.ContainerDetail, error) {
	ns, name := parseK8sID(id)
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s", ns, name)
	resp, err := k.do(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("k8s inspect: %s – %s", resp.Status, string(b))
	}

	var pod k8sPod
	if err := json.NewDecoder(resp.Body).Decode(&pod); err != nil {
		return nil, err
	}

	detail := &models.ContainerDetail{
		Container: models.Container{
			ID:        id,
			Name:      pod.Metadata.Name,
			State:     strings.ToLower(string(pod.Status.Phase)),
			Status:    string(pod.Status.Phase),
			EnvID:     k.env.ID,
			EnvName:   k.env.Name,
			EnvType:   k.env.Type,
			Node:      pod.Spec.NodeName,
			CreatedAt: pod.Metadata.CreationTimestamp,
			Labels:    pod.Metadata.Labels,
			Extra:     map[string]string{"namespace": pod.Metadata.Namespace},
		},
	}
	if len(pod.Spec.Containers) > 0 {
		c := pod.Spec.Containers[0]
		detail.Image = c.Image
		detail.Command = strings.Join(c.Command, " ")
		for _, p := range c.Ports {
			detail.Ports = append(detail.Ports, models.PortMapping{
				ContainerPort: p.ContainerPort,
				Protocol:      p.Protocol,
			})
		}
		for _, e := range c.Env {
			detail.Env = append(detail.Env, e.Name+"="+e.Value)
		}
	}
	if len(pod.Status.ContainerStatuses) > 0 {
		detail.RestartCount = pod.Status.ContainerStatuses[0].RestartCount
	}
	return detail, nil
}

func (k *KubeProvider) ContainerLogs(ctx context.Context, id string, tail int) (io.ReadCloser, error) {
	ns, name := parseK8sID(id)
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/log?tailLines=%d&timestamps=true", ns, name, tail)
	resp, err := k.do(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("k8s logs: %s – %s", resp.Status, string(b))
	}
	return resp.Body, nil
}

func (k *KubeProvider) ExecContainer(ctx context.Context, id string, cmd []string) (*models.ExecResponse, error) {
	ns, name := parseK8sID(id)

	// Build exec URL with query params (Kubernetes SPDY/WS exec)
	params := url.Values{}
	params.Set("stdout", "1")
	params.Set("stderr", "1")
	for _, c := range cmd {
		params.Add("command", c)
	}

	// Use the simple POST exec approach (requires compatible API versions)
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/exec?%s", ns, name, params.Encode())

	resp, err := k.do(ctx, "POST", path, nil)
	if err != nil {
		return nil, fmt.Errorf("k8s exec: %w", err)
	}
	defer resp.Body.Close()
	output, _ := io.ReadAll(resp.Body)

	return &models.ExecResponse{Output: string(output), ExitCode: 0}, nil
}

// parseK8sID splits "namespace/name" back into parts.
func parseK8sID(id string) (string, string) {
	parts := strings.SplitN(id, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "default", id
}

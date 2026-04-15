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

// KubeProvider talks to the Kubernetes API server, scoped to a single namespace.
type KubeProvider struct {
	client *http.Client
	cfg    Config
}

func (k *KubeProvider) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://api"+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	return k.client.Do(req)
}

// ns returns the namespace to use, defaulting to "default".
func (k *KubeProvider) ns() string {
	if k.cfg.Namespace != "" {
		return k.cfg.Namespace
	}
	return "default"
}

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
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods", k.ns())
	resp, err := k.do(ctx, "GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("k8s list: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("k8s list: %s – read body: %w", resp.Status, err)
		}
		return nil, fmt.Errorf("k8s list: %s – %s", resp.Status, string(b))
	}

	var pl k8sPodList
	if err := json.NewDecoder(resp.Body).Decode(&pl); err != nil {
		return nil, err
	}

	out := make([]models.Container, 0, len(pl.Items))
	for _, pod := range pl.Items {
		state := strings.ToLower(string(pod.Status.Phase))
		status := string(pod.Status.Phase)
		image := ""
		if len(pod.Spec.Containers) > 0 {
			image = pod.Spec.Containers[0].Image
		}
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

		out = append(out, models.Container{
			ID:          pod.Metadata.Name,
			Name:        pod.Metadata.Name,
			Image:       image,
			Status:      status,
			State:       state,
			EnvID:       k.cfg.EnvID,
			EnvName:     k.cfg.EnvName,
			ClusterType: models.ClusterKubernetes,
			Namespace:   pod.Metadata.Namespace,
			Node:        pod.Spec.NodeName,
			CreatedAt:   pod.Metadata.CreationTimestamp,
			Labels:      pod.Metadata.Labels,
		})
	}
	return out, nil
}

func (k *KubeProvider) InspectContainer(ctx context.Context, id string) (*models.ContainerDetail, error) {
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s", k.ns(), id)
	resp, err := k.do(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("k8s inspect: %s – read body: %w", resp.Status, err)
		}
		return nil, fmt.Errorf("k8s inspect: %s – %s", resp.Status, string(b))
	}

	var pod k8sPod
	if err := json.NewDecoder(resp.Body).Decode(&pod); err != nil {
		return nil, err
	}

	detail := &models.ContainerDetail{
		Container: models.Container{
			ID:          pod.Metadata.Name,
			Name:        pod.Metadata.Name,
			State:       strings.ToLower(string(pod.Status.Phase)),
			Status:      string(pod.Status.Phase),
			EnvID:       k.cfg.EnvID,
			EnvName:     k.cfg.EnvName,
			ClusterType: models.ClusterKubernetes,
			Namespace:   pod.Metadata.Namespace,
			Node:        pod.Spec.NodeName,
			CreatedAt:   pod.Metadata.CreationTimestamp,
			Labels:      pod.Metadata.Labels,
		},
	}
	if len(pod.Spec.Containers) > 0 {
		c := pod.Spec.Containers[0]
		detail.Image = c.Image
		detail.Command = strings.Join(c.Command, " ")
		for _, p := range c.Ports {
			detail.Ports = append(detail.Ports, models.PortMapping{
				ContainerPort: p.ContainerPort, Protocol: p.Protocol,
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

func (k *KubeProvider) ContainerLogs(ctx context.Context, id string, tail int, follow bool) (io.ReadCloser, error) {
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/log?tailLines=%d&timestamps=true", k.ns(), id, tail)
	if follow {
		path += "&follow=true"
	}
	resp, err := k.do(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("k8s logs: %s", resp.Status)
	}
	return resp.Body, nil
}

func (k *KubeProvider) StopContainer(ctx context.Context, id string) error {
	// In Kubernetes, "stop" means deleting the pod with a graceful termination period.
	// If the pod is managed by a controller (Deployment, ReplicaSet), the controller
	// will recreate it. For bare pods, this effectively stops the workload.
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s?gracePeriodSeconds=30", k.ns(), id)
	resp, err := k.do(ctx, "DELETE", path, nil)
	if err != nil {
		return fmt.Errorf("k8s stop: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 202 {
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("k8s stop: %s – read body: %w", resp.Status, err)
		}
		return fmt.Errorf("k8s stop: %s – %s", resp.Status, string(b))
	}
	return nil
}

func (k *KubeProvider) RestartContainer(ctx context.Context, id string) error {
	// Restart by deleting the pod; if managed by a controller it gets recreated.
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s", k.ns(), id)
	resp, err := k.do(ctx, "DELETE", path, nil)
	if err != nil {
		return fmt.Errorf("k8s restart: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 202 {
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("k8s restart: %s – read body: %w", resp.Status, err)
		}
		return fmt.Errorf("k8s restart: %s – %s", resp.Status, string(b))
	}
	return nil
}

func (k *KubeProvider) DeleteContainer(ctx context.Context, id string) error {
	// Force delete with zero grace period.
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s?gracePeriodSeconds=0", k.ns(), id)
	resp, err := k.do(ctx, "DELETE", path, nil)
	if err != nil {
		return fmt.Errorf("k8s delete: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 202 {
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("k8s delete: %s – read body: %w", resp.Status, err)
		}
		return fmt.Errorf("k8s delete: %s – %s", resp.Status, string(b))
	}
	return nil
}

func (k *KubeProvider) ExecContainer(ctx context.Context, id string, cmd []string) (*models.ExecResponse, error) {
	params := url.Values{}
	params.Set("stdout", "1")
	params.Set("stderr", "1")
	for _, c := range cmd {
		params.Add("command", c)
	}
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/exec?%s", k.ns(), id, params.Encode())
	resp, err := k.do(ctx, "POST", path, nil)
	if err != nil {
		return nil, fmt.Errorf("k8s exec: %w", err)
	}
	defer resp.Body.Close()
	output, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("k8s exec read output: %w", err)
	}
	return &models.ExecResponse{Output: string(output), ExitCode: 0}, nil
}

//go:build integration

package integration

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// redirectTransport rewrites requests from http://api/... to a real endpoint,
// simulating the tunnel transport that production uses. It optionally injects
// an auth header (as the agent would in production).
type redirectTransport struct {
	target *url.URL
	header string // auth header name (e.g. "Authorization", "X-Nomad-Token")
	token  string // auth header value (e.g. "Bearer <jwt>")
	inner  http.RoundTripper
}

func (rt *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req2 := req.Clone(req.Context())
	req2.URL.Scheme = rt.target.Scheme
	req2.URL.Host = rt.target.Host
	if rt.header != "" && rt.token != "" {
		req2.Header.Set(rt.header, rt.token)
	}
	return rt.inner.RoundTrip(req2)
}

type clientOption func(*redirectTransport)

func withAuth(header, token string) clientOption {
	return func(rt *redirectTransport) {
		rt.header = header
		rt.token = token
	}
}

// newTestClient returns an *http.Client whose transport redirects the
// provider's http://api/... requests to the given real endpoint.
func newTestClient(endpoint string, opts ...clientOption) *http.Client {
	u, err := url.Parse(endpoint)
	if err != nil {
		panic(fmt.Sprintf("invalid endpoint %q: %v", endpoint, err))
	}
	rt := &redirectTransport{
		target: u,
		inner: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	for _, opt := range opts {
		opt(rt)
	}
	return &http.Client{
		Transport: rt,
		Timeout:   30 * time.Second,
	}
}

// skipIfNoDocker skips the test if the Docker daemon is unreachable.
func skipIfNoDocker(t *testing.T) {
	t.Helper()
	if out, err := exec.Command("docker", "info").CombinedOutput(); err != nil {
		t.Skipf("Docker not available, skipping: %v\n%s", err, out)
	}
}

// dockerRun starts a container and returns its ID. The container is
// automatically removed when the test finishes.
func dockerRun(t *testing.T, args ...string) string {
	t.Helper()
	cmdArgs := append([]string{"run", "-d"}, args...)
	out, err := exec.Command("docker", cmdArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker run %v: %v\n%s", args, err, out)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() {
		exec.Command("docker", "rm", "-f", id).Run()
	})
	return id
}

// dockerPort returns the host port mapped to a container port.
func dockerPort(t *testing.T, id, containerPort string) string {
	t.Helper()
	out, err := exec.Command("docker", "port", id, containerPort).CombinedOutput()
	if err != nil {
		t.Fatalf("docker port %s %s: %v\n%s", id, containerPort, err, out)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if idx := strings.LastIndex(line, ":"); idx >= 0 {
			return line[idx+1:]
		}
	}
	t.Fatalf("cannot parse mapped port from %q", string(out))
	return ""
}

// dockerExec runs a command inside a running container and returns stdout.
func dockerExec(t *testing.T, id string, args ...string) string {
	t.Helper()
	cmdArgs := append([]string{"exec", id}, args...)
	out, err := exec.Command("docker", cmdArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker exec %s %v: %v\n%s", id, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// waitForHTTP polls endpoint until it returns HTTP 200 or the timeout expires.
func waitForHTTP(t *testing.T, endpoint string, timeout time.Duration) {
	t.Helper()
	client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(endpoint)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("timeout waiting for %s: %v", endpoint, lastErr)
}

// waitForShell polls a shell command until it succeeds or the timeout expires.
func waitForShell(t *testing.T, timeout time.Duration, name string, args ...string) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		out, err := exec.Command(name, args...).CombinedOutput()
		if err == nil {
			return strings.TrimSpace(string(out))
		}
		lastErr = fmt.Errorf("%v: %s", err, out)
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("timeout waiting for %s %v: %v", name, args, lastErr)
	return ""
}

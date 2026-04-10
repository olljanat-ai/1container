package provider

import (
	"container-hub/internal/models"
	"testing"
)

func TestTruncateID(t *testing.T) {
	tests := []struct {
		id     string
		maxLen int
		want   string
	}{
		{"abcdef123456", 12, "abcdef123456"},
		{"abcdef1234567890", 12, "abcdef123456"},
		{"short", 12, "short"},
		{"", 12, ""},
		{"abc", 3, "abc"},
		{"abcd", 3, "abc"},
	}
	for _, tt := range tests {
		got := truncateID(tt.id, tt.maxLen)
		if got != tt.want {
			t.Errorf("truncateID(%q, %d) = %q, want %q", tt.id, tt.maxLen, got, tt.want)
		}
	}
}

func TestNewProviderFactory(t *testing.T) {
	tests := []struct {
		clusterType models.ClusterType
		wantNil     bool
		wantType    string
	}{
		{models.ClusterDockerSwarm, false, "*provider.DockerProvider"},
		{models.ClusterKubernetes, false, "*provider.KubeProvider"},
		{models.ClusterNomad, false, "*provider.NomadProvider"},
		{"unknown-type", true, ""},
	}

	for _, tt := range tests {
		cfg := Config{ClusterType: tt.clusterType, Namespace: "default", EnvID: "test", EnvName: "Test"}
		p := New(cfg, nil)
		if tt.wantNil {
			if p != nil {
				t.Errorf("New(%q) = %T, want nil", tt.clusterType, p)
			}
		} else {
			if p == nil {
				t.Errorf("New(%q) = nil, want %s", tt.clusterType, tt.wantType)
			}
		}
	}
}

func TestParseNomadID(t *testing.T) {
	tests := []struct {
		id        string
		wantAlloc string
		wantTask  string
	}{
		{"abc123/web", "abc123", "web"},
		{"abc123/worker", "abc123", "worker"},
		{"alloconly", "alloconly", "main"},
		{"a/b/c", "a", "b/c"},
	}
	for _, tt := range tests {
		gotAlloc, gotTask := parseNomadID(tt.id)
		if gotAlloc != tt.wantAlloc || gotTask != tt.wantTask {
			t.Errorf("parseNomadID(%q) = (%q, %q), want (%q, %q)",
				tt.id, gotAlloc, gotTask, tt.wantAlloc, tt.wantTask)
		}
	}
}

func TestStripDockerStream(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  string
	}{
		{
			name:  "valid docker stream frame",
			input: append([]byte{1, 0, 0, 0, 0, 0, 0, 5}, []byte("hello")...),
			want:  "hello",
		},
		{
			name:  "multiple frames",
			input: append(append([]byte{1, 0, 0, 0, 0, 0, 0, 3}, []byte("foo")...), append([]byte{2, 0, 0, 0, 0, 0, 0, 3}, []byte("bar")...)...),
			want:  "foobar",
		},
		{
			name:  "no valid frames returns raw data",
			input: []byte("plain"),
			want:  "plain",
		},
		{
			name:  "empty input",
			input: []byte{},
			want:  "",
		},
		{
			name:  "frame size exceeds data truncates gracefully",
			input: append([]byte{1, 0, 0, 0, 0, 0, 0, 100}, []byte("short")...),
			want:  "short",
		},
	}
	for _, tt := range tests {
		got := string(stripDockerStream(tt.input))
		if got != tt.want {
			t.Errorf("stripDockerStream(%s) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestNomadNsParam(t *testing.T) {
	tests := []struct {
		namespace string
		prefix    string
		want      string
	}{
		{"", "&", ""},
		{"default", "&", ""},
		{"production", "&", "&namespace=production"},
		{"production", "?", "?namespace=production"},
		{"my ns", "&", "&namespace=my+ns"},
	}
	for _, tt := range tests {
		n := &NomadProvider{cfg: Config{Namespace: tt.namespace}}
		got := n.nsParam(tt.prefix)
		if got != tt.want {
			t.Errorf("nsParam(%q, %q) = %q, want %q", tt.namespace, tt.prefix, got, tt.want)
		}
	}
}

func TestKubeNsDefault(t *testing.T) {
	k := &KubeProvider{cfg: Config{Namespace: ""}}
	if got := k.ns(); got != "default" {
		t.Errorf("ns() = %q, want %q", got, "default")
	}

	k2 := &KubeProvider{cfg: Config{Namespace: "monitoring"}}
	if got := k2.ns(); got != "monitoring" {
		t.Errorf("ns() = %q, want %q", got, "monitoring")
	}
}

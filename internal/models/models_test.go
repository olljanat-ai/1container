package models

import "testing"

func TestValidClusterType(t *testing.T) {
	tests := []struct {
		ct   ClusterType
		want bool
	}{
		{ClusterDockerSwarm, true},
		{ClusterKubernetes, true},
		{ClusterNomad, true},
		{"unknown", false},
		{"", false},
		{"docker", false},
		{"Docker-Swarm", false},
	}
	for _, tt := range tests {
		got := ValidClusterType(tt.ct)
		if got != tt.want {
			t.Errorf("ValidClusterType(%q) = %v, want %v", tt.ct, got, tt.want)
		}
	}
}

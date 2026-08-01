package main

import (
	"strings"
	"testing"
)

// TestIsApproval documents that consent is opt-in: only an explicit
// affirmative unlocks the production-mutating branch.
func TestIsApproval(t *testing.T) {
	tests := []struct {
		reply any
		want  bool
	}{
		{"yes", true},
		{"YES", true},
		{" y ", true},
		{"no", false},
		{"", false},
		{"yes, but wait", false},
		{nil, false},
		{map[string]any{"answer": "yes"}, false},
	}
	for _, tc := range tests {
		if got := isApproval(tc.reply); got != tc.want {
			t.Errorf("isApproval(%#v) = %v, want %v", tc.reply, got, tc.want)
		}
	}
}

func TestScaleOutAndPageOncallReportTheService(t *testing.T) {
	ev := Evidence{
		Service: "search-api",
		Metrics: Metrics{ErrorRate: 0.01, LatencyP99Ms: 1800, CPUSaturation: 0.93},
		Deploy:  DeployInfo{LastDeployID: "rel-9", MinutesSinceDeploy: 300},
	}

	scaled, err := scaleOut(nil, ev)
	if err != nil {
		t.Fatalf("scaleOut: %v", err)
	}
	if !strings.Contains(scaled, "search-api") {
		t.Errorf("scaleOut output does not name the service: %q", scaled)
	}

	paged, err := pageOncall(nil, ev)
	if err != nil {
		t.Fatalf("pageOncall: %v", err)
	}
	if !strings.Contains(paged, "search-api") {
		t.Errorf("pageOncall output does not name the service: %q", paged)
	}
}

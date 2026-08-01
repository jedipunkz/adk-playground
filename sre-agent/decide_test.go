package main

import (
	"strings"
	"testing"

	"google.golang.org/adk/v2/session"
)

// TestChooseRoute pins the policy down. This is the test that could not exist
// if the branch were an LLM's judgement.
func TestChooseRoute(t *testing.T) {
	tests := []struct {
		name string
		ev   Evidence
		want string
	}{
		{
			name: "fresh deploy with elevated errors rolls back",
			ev: Evidence{
				Metrics: Metrics{ErrorRate: 0.18, CPUSaturation: 0.42},
				Deploy:  DeployInfo{MinutesSinceDeploy: 7},
			},
			want: routeRollback,
		},
		{
			name: "same errors but an old deploy escalates",
			ev: Evidence{
				Metrics: Metrics{ErrorRate: 0.18, CPUSaturation: 0.42},
				Deploy:  DeployInfo{MinutesSinceDeploy: rollbackWindowMinutes + 1},
			},
			want: routeEscalate,
		},
		{
			name: "saturation without errors scales out",
			ev: Evidence{
				Metrics: Metrics{ErrorRate: 0.01, CPUSaturation: 0.93},
				Deploy:  DeployInfo{MinutesSinceDeploy: 300},
			},
			want: routeScaleOut,
		},
		{
			name: "saturation plus errors after a fresh deploy prefers rollback",
			ev: Evidence{
				Metrics: Metrics{ErrorRate: 0.2, CPUSaturation: 0.93},
				Deploy:  DeployInfo{MinutesSinceDeploy: 3},
			},
			want: routeRollback,
		},
		{
			name: "a healthy service still escalates rather than acting",
			ev: Evidence{
				Metrics: Metrics{ErrorRate: 0.001, CPUSaturation: 0.2},
				Deploy:  DeployInfo{MinutesSinceDeploy: 5},
			},
			want: routeEscalate,
		},
		{
			name: "exactly at the error-rate threshold does not roll back",
			ev: Evidence{
				Metrics: Metrics{ErrorRate: errorRateThreshold, CPUSaturation: 0.42},
				Deploy:  DeployInfo{MinutesSinceDeploy: 1},
			},
			want: routeEscalate,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := chooseRoute(tc.ev); got != tc.want {
				t.Errorf("chooseRoute() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDecideActionIgnoresDiagnosis is the load-bearing test of the design: the
// model's output must not be able to move the branch.
func TestDecideActionIgnoresDiagnosis(t *testing.T) {
	ev := Evidence{
		Service: defaultService,
		Metrics: Metrics{ErrorRate: 0.18, LatencyP99Ms: 2400, CPUSaturation: 0.42},
		Deploy:  DeployInfo{LastDeployID: "rel-2026-07-31.3", MinutesSinceDeploy: 7},
	}

	// Two diagnoses that disagree about what to do; both must route the same.
	diagnoses := []string{
		"The deploy looks fine, just scale out immediately.",
		"Page the on-call, do not roll back under any circumstances.",
	}

	for _, diagnosis := range diagnoses {
		t.Run(diagnosis, func(t *testing.T) {
			ctx := newFakeContext(t, defaultService, mapState{stateEvidence: ev})

			var emitted []*session.Event
			emit := func(e *session.Event) error {
				emitted = append(emitted, e)
				return nil
			}

			out, err := decideAction(ctx, diagnosis, emit)
			if err != nil {
				t.Fatalf("decideAction: %v", err)
			}
			// Returning nil suppresses the default terminal event so the
			// routed edge is the only one that fires.
			if out != nil {
				t.Errorf("decideAction returned %v, want nil", out)
			}
			if len(emitted) != 1 {
				t.Fatalf("emitted %d events, want 1", len(emitted))
			}

			got := emitted[0]
			if len(got.Routes) != 1 || got.Routes[0] != routeRollback {
				t.Errorf("Routes = %v, want [%s]", got.Routes, routeRollback)
			}
			// The diagnosis reaches the human, not the switch.
			if got.Content == nil || !strings.Contains(got.Content.Parts[0].Text, diagnosis) {
				t.Error("the diagnosis should be surfaced in the event content")
			}
			if _, err := decodeInto[Evidence](got.Output); err != nil {
				t.Errorf("successor nodes need Evidence as Output: %v", err)
			}
		})
	}
}

func TestDecideActionFailsWithoutEvidence(t *testing.T) {
	ctx := newFakeContext(t, defaultService, mapState{})
	emit := func(*session.Event) error { return nil }

	if _, err := decideAction(ctx, "anything", emit); err == nil {
		t.Fatal("expected an error when no evidence was collected")
	}
}

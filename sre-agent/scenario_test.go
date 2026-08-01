package main

import (
	"reflect"
	"testing"
)

// TestScenariosCoverEveryRoute keeps the demo data honest: if a threshold
// moves, at least one canned incident stops reaching its branch and this fails.
func TestScenariosCoverEveryRoute(t *testing.T) {
	want := map[string]string{
		"deploy-regression": routeRollback,
		"saturation":        routeScaleOut,
		"unknown-cause":     routeEscalate,
	}

	if len(want) != len(scenarios) {
		t.Fatalf("scenarios has %d entries, expectations cover %d", len(scenarios), len(want))
	}

	for name, wantRoute := range want {
		s, ok := scenarios[name]
		if !ok {
			t.Errorf("scenario %q is missing", name)
			continue
		}
		ev := Evidence{Service: defaultService, Metrics: s.metrics, Logs: s.logs, Deploy: s.deploy}
		if got := chooseRoute(ev); got != wantRoute {
			t.Errorf("scenario %q routes to %q, want %q", name, got, wantRoute)
		}
	}
}

func TestCurrentScenarioFallsBackOnUnknownName(t *testing.T) {
	t.Setenv(scenarioEnv, "no-such-scenario")
	if got := currentScenario(); !reflect.DeepEqual(got, scenarios[defaultScenario]) {
		t.Errorf("currentScenario() = %+v, want the %q scenario", got, defaultScenario)
	}
}

func TestCurrentScenarioHonoursTheEnvironment(t *testing.T) {
	t.Setenv(scenarioEnv, "saturation")
	if got := currentScenario(); !reflect.DeepEqual(got, scenarios["saturation"]) {
		t.Errorf("currentScenario() = %+v, want the saturation scenario", got)
	}
}

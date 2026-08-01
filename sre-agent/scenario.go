package main

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

// scenarioEnv selects which canned incident the collectors report. It exists
// so every routed edge in the graph can be exercised without an observability
// backend; a real deployment deletes this file and queries Datadog instead.
const scenarioEnv = "SRE_AGENT_SCENARIO"

// defaultScenario is the one the graph runs when the environment says nothing.
const defaultScenario = "deploy-regression"

// scenario is one frozen incident: exactly what the three collectors would
// have returned had they queried a live backend.
type scenario struct {
	metrics Metrics
	logs    LogDigest
	deploy  DeployInfo
}

// scenarios covers one incident per branch of chooseRoute, so
// SRE_AGENT_SCENARIO doubles as a way to demo each runbook.
var scenarios = map[string]scenario{
	// A bad release: recent deploy, errors up. Routes to propose_rollback.
	"deploy-regression": {
		metrics: Metrics{ErrorRate: 0.18, LatencyP99Ms: 2400, CPUSaturation: 0.42},
		logs: LogDigest{
			TopErrors: []string{"panic: nil map write", "context deadline exceeded"},
			Volume:    18342,
		},
		deploy: DeployInfo{LastDeployID: "rel-2026-07-31.3", MinutesSinceDeploy: 7},
	},
	// Organic load growth: hot CPU, errors still within budget. Routes to
	// scale_out_service, which needs no approval.
	"saturation": {
		metrics: Metrics{ErrorRate: 0.012, LatencyP99Ms: 1850, CPUSaturation: 0.93},
		logs: LogDigest{
			TopErrors: []string{"upstream request timeout"},
			Volume:    412,
		},
		deploy: DeployInfo{LastDeployID: "rel-2026-07-24.1", MinutesSinceDeploy: 10_080},
	},
	// Elevated errors with no recent deploy and no saturation: nothing the
	// policy recognises, so it routes to page_oncall.
	"unknown-cause": {
		metrics: Metrics{ErrorRate: 0.09, LatencyP99Ms: 5200, CPUSaturation: 0.31},
		logs: LogDigest{
			TopErrors: []string{"connection reset by peer", "tls handshake timeout"},
			Volume:    2044,
		},
		deploy: DeployInfo{LastDeployID: "rel-2026-07-29.2", MinutesSinceDeploy: 2880},
	},
}

// currentScenario resolves the selected scenario, falling back to the default
// for an unset or unrecognised value rather than failing the triage.
func currentScenario() scenario {
	name := strings.TrimSpace(os.Getenv(scenarioEnv))
	if s, ok := scenarios[name]; ok {
		return s
	}
	return scenarios[defaultScenario]
}

// scenarioNames lists the available scenarios in a stable order, for the
// startup log line.
func scenarioNames() string {
	names := make([]string, 0, len(scenarios))
	for name := range scenarios {
		names = append(names, name)
	}
	slices.Sort(names)
	return fmt.Sprintf("%v", names)
}

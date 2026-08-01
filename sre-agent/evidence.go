package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/adk/v2/agent"
)

// defaultService is used when the incoming message names no known service.
const defaultService = "checkout-api"

// knownServices is the allow-list of services this agent may investigate.
// Anything outside it falls back to defaultService, so a stray message can
// never make the collectors query an unexpected target.
var knownServices = []string{"checkout-api", "payment-api", "search-api"}

// stateEvidence is the session-state key under which build_evidence stores the
// raw evidence. decide_action reads it back from there rather than from the
// LLM's prose, which is what keeps the routing decision reproducible.
const stateEvidence = "incident_evidence"

// Metrics is the numeric slice of the incident: the part a threshold can be
// written against.
type Metrics struct {
	ErrorRate     float64 `json:"errorRate"`
	LatencyP99Ms  int     `json:"latencyP99Ms"`
	CPUSaturation float64 `json:"cpuSaturation"`
}

// LogDigest is a pre-aggregated view of the error logs. The aggregation
// happens here, in Go, so the LLM never decides how much log to look at.
type LogDigest struct {
	TopErrors []string `json:"topErrors"`
	Volume    int      `json:"volume"`
}

// DeployInfo describes the most recent release of the service under
// investigation.
type DeployInfo struct {
	LastDeployID       string `json:"lastDeployId"`
	MinutesSinceDeploy int    `json:"minutesSinceDeploy"`
}

// Evidence is everything the graph collected about one incident. It is the
// sole input to the routing decision, and it is also what the LLM is asked to
// summarize — the two consume the same snapshot.
type Evidence struct {
	Service string     `json:"service"`
	Metrics Metrics    `json:"metrics"`
	Logs    LogDigest  `json:"logs"`
	Deploy  DeployInfo `json:"deploy"`
}

// normalizeService maps free-form user text onto the allow-list. Matching by
// substring keeps "checkout-api is on fire" working while still refusing to
// produce a target that is not in knownServices.
func normalizeService(raw string) string {
	msg := strings.ToLower(strings.TrimSpace(raw))
	for _, svc := range knownServices {
		if strings.Contains(msg, svc) {
			return svc
		}
	}
	return defaultService
}

// fetchMetrics queries the metrics backend for the service's error rate,
// tail latency and saturation.
//
// The values come from the selected scenario so the sample runs without
// credentials. In a real deployment this is where datadog-api-client-go's
// MetricsApi.QueryTimeseriesData would go, with queries such as
// sum:trace.http.request.errors{service:checkout-api}.as_rate(). Note that the
// query and the time window are chosen here, in Go, not by the model.
func fetchMetrics(_ agent.Context, service string) (Metrics, error) {
	_ = normalizeService(service)
	return currentScenario().metrics, nil
}

// fetchLogs pulls the top error signatures and the total error volume for the
// service. The real implementation would post
// "service:checkout-api status:error" to the Logs Aggregate API and keep the
// top N buckets — again a fixed query, so two runs on the same incident see
// the same logs.
func fetchLogs(_ agent.Context, service string) (LogDigest, error) {
	_ = normalizeService(service)
	return currentScenario().logs, nil
}

// fetchDeploys resolves the service's most recent release and how long ago it
// went out — the single most useful correlate for a sudden error-rate jump.
func fetchDeploys(_ agent.Context, service string) (DeployInfo, error) {
	_ = normalizeService(service)
	return currentScenario().deploy, nil
}

// buildEvidence is the fan-in formatter. It receives the JoinNode's
// map[nodeName]output, assembles one Evidence value, stashes it in session
// state for decide_action, and returns the prompt the diagnosis LLM sees.
//
// Reading the three collectors by name (rather than ranging the map) keeps
// the prompt byte-identical across runs, which matters because the parallel
// collectors finish in a non-deterministic order.
func buildEvidence(ctx agent.Context, gathered map[string]any) (string, error) {
	ev := Evidence{Service: normalizeService(userMessage(ctx))}

	var err error
	if ev.Metrics, err = decodeInto[Metrics](gathered[nodeMetrics]); err != nil {
		return "", fmt.Errorf("decoding metrics: %w", err)
	}
	if ev.Logs, err = decodeInto[LogDigest](gathered[nodeLogs]); err != nil {
		return "", fmt.Errorf("decoding logs: %w", err)
	}
	if ev.Deploy, err = decodeInto[DeployInfo](gathered[nodeDeploys]); err != nil {
		return "", fmt.Errorf("decoding deploys: %w", err)
	}

	// Persist the raw evidence so decide_action can read it. The LLM's
	// output is deliberately not the carrier here: this value is the only
	// thing the branch is allowed to depend on.
	if err := ctx.State().Set(stateEvidence, ev); err != nil {
		return "", fmt.Errorf("saving evidence: %w", err)
	}

	b, err := json.MarshalIndent(ev, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encoding evidence: %w", err)
	}
	return "Incident evidence:\n" + string(b), nil
}

// decodeInto converts a value that travelled through the graph (and possibly
// through session persistence, which turns structs into map[string]any) back
// into a concrete type.
func decodeInto[T any](v any) (T, error) {
	var out T
	if v == nil {
		return out, fmt.Errorf("missing value for %T", out)
	}
	if typed, ok := v.(T); ok {
		return typed, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return out, fmt.Errorf("re-encoding %T: %w", v, err)
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return out, fmt.Errorf("decoding into %T: %w", out, err)
	}
	return out, nil
}

// userMessage returns the text of the message that started this invocation.
func userMessage(ctx agent.Context) string {
	uc := ctx.UserContent()
	if uc == nil {
		return ""
	}
	var sb strings.Builder
	for _, p := range uc.Parts {
		sb.WriteString(p.Text)
	}
	return strings.TrimSpace(sb.String())
}

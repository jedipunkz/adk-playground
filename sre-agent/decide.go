package main

import (
	"fmt"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
)

// Route values. Each one has exactly one outgoing edge declared in graph.go,
// so the set of runbooks this agent can reach is fixed at wiring time and
// visible in a diff.
const (
	routeRollback = "rollback"
	routeScaleOut = "scale_out"
	routeEscalate = "escalate"
)

// Decision thresholds. They live here as named constants rather than inline
// magic numbers so a review of "when does this agent roll production back?"
// is a review of five lines.
const (
	// rollbackWindowMinutes bounds how recent a deploy has to be for the
	// error rate to be attributed to it.
	rollbackWindowMinutes = 30
	// errorRateThreshold is the fraction of failing requests above which
	// the service is considered to be in a bad state.
	errorRateThreshold = 0.05
	// cpuSaturationThreshold is the utilisation above which added capacity
	// is the plausible remedy.
	cpuSaturationThreshold = 0.85
)

// chooseRoute is the entire decision policy: a pure function of the collected
// evidence. It takes no context, performs no I/O and never consults the model,
// which is what makes it unit-testable and makes the same incident always
// produce the same runbook.
func chooseRoute(ev Evidence) string {
	switch {
	case ev.Deploy.MinutesSinceDeploy <= rollbackWindowMinutes && ev.Metrics.ErrorRate > errorRateThreshold:
		return routeRollback
	case ev.Metrics.CPUSaturation > cpuSaturationThreshold && ev.Metrics.ErrorRate <= errorRateThreshold:
		return routeScaleOut
	default:
		return routeEscalate
	}
}

// decideAction is the boundary between the model and the machinery. It takes
// the LLM's diagnosis as its graph input but passes it only to the human: the
// branch itself is computed by chooseRoute from the evidence in session state.
//
// Emitting an event with Routes set and returning (nil, nil) suppresses the
// node's default terminal event, so the scheduler follows exactly one of the
// routed edges.
func decideAction(ctx agent.Context, diagnosis any, emit func(*session.Event) error) (any, error) {
	raw, err := ctx.State().Get(stateEvidence)
	if err != nil {
		return nil, fmt.Errorf("reading evidence: %w", err)
	}
	ev, err := decodeInto[Evidence](raw)
	if err != nil {
		return nil, fmt.Errorf("decoding evidence: %w", err)
	}

	route := chooseRoute(ev)

	out := session.NewEvent(ctx, ctx.InvocationID())
	out.Routes = []string{route}
	// The successor nodes take Evidence, not prose — the diagnosis rides
	// along in Content purely so the responder can read it.
	out.Output = ev
	out.Content = &genai.Content{
		Role: "model",
		Parts: []*genai.Part{{
			Text: fmt.Sprintf("decision=%s\nLLM diagnosis: %v", route, diagnosis),
		}},
	}
	if err := emit(out); err != nil {
		return nil, fmt.Errorf("emitting decision: %w", err)
	}
	return nil, nil
}

package main

import (
	"fmt"
	"sync"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
)

// Limits are the guardrails of the investigation, in two layers:
//
//   - loop layer, enforced by the Go orchestrator between rounds:
//     MaxRounds, NoProgressRounds, TokenBudget
//   - agent-loop layer, enforced by llmagent callbacks inside a round:
//     MaxModelCallsPerRound, TokenBudget, the tool allowlist, MaxWindowMinutes
//
// ADK's llmagent has no turn or budget cap of its own: its tool loop runs
// until the model answers without calling a tool. Everything here exists
// because nothing else bounds it.
type Limits struct {
	MaxRounds             int
	NoProgressRounds      int
	MaxModelCallsPerRound int
	TokenBudget           int
	MaxWindowMinutes      int
}

var defaultLimits = Limits{
	MaxRounds:             6,
	NoProgressRounds:      2,
	MaxModelCallsPerRound: 8,
	TokenBudget:           30_000,
	MaxWindowMinutes:      60,
}

// cutoffAnswer is what the model-call guard returns instead of calling the
// model. It is a valid round result with nothing in it, so the round ends
// cleanly and the orchestrator counts it as a round without progress.
const cutoffAnswer = `{"findings": [], "done": false}`

// guard holds the per-run guardrail state shared by the callbacks and the
// orchestrator. Runs are keyed by invocation ID: the investigator runs as a
// child of the orchestrator inside the same invocation.
//
// ponytail: in-memory state, lost on process restart, so a run resumed in a
// new process starts with a fresh token meter. Move it to session state if
// cross-process resume matters.
type guard struct {
	limits Limits

	mu   sync.Mutex
	runs map[string]*runState
}

type runState struct {
	roundCalls int
	tokens     int
	cutoffs    []string // why a round was cut short, one entry per cutoff
	denied     []string // tool calls refused by the allowlist
}

func newGuard(l Limits) *guard {
	return &guard{limits: l, runs: map[string]*runState{}}
}

// state returns the run's state, creating it on first use. Callers hold g.mu.
func (g *guard) state(invocationID string) *runState {
	s, ok := g.runs[invocationID]
	if !ok {
		s = &runState{}
		g.runs[invocationID] = s
	}
	return s
}

func (g *guard) beginRound(invocationID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.state(invocationID).roundCalls = 0
}

// snapshot returns a copy of the run's state for the orchestrator to read.
func (g *guard) snapshot(invocationID string) runState {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := *g.state(invocationID)
	s.cutoffs = append([]string(nil), s.cutoffs...)
	s.denied = append([]string(nil), s.denied...)
	return s
}

func (g *guard) finish(invocationID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.runs, invocationID)
}

// beforeModel caps the agent loop: once the round has used its model calls,
// or the run has used its token budget, it answers in place of the model.
// The answer has no tool call, which is exactly what ends llmagent's loop.
func (g *guard) beforeModel(ctx agent.Context, _ *model.LLMRequest) (*model.LLMResponse, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.state(ctx.InvocationID())

	var reason string
	switch {
	case s.tokens >= g.limits.TokenBudget:
		reason = fmt.Sprintf("token budget %d exhausted", g.limits.TokenBudget)
	case s.roundCalls >= g.limits.MaxModelCallsPerRound:
		reason = fmt.Sprintf("%d model calls in one round", g.limits.MaxModelCallsPerRound)
	default:
		s.roundCalls++
		return nil, nil
	}
	s.cutoffs = append(s.cutoffs, reason)
	return &model.LLMResponse{
		Content:      genai.NewContentFromText(cutoffAnswer, genai.RoleModel),
		TurnComplete: true,
	}, nil
}

// afterModel meters tokens. Partial stream chunks are skipped so a streamed
// response is counted once.
func (g *guard) afterModel(ctx agent.Context, resp *model.LLMResponse, _ error) (*model.LLMResponse, error) {
	if resp == nil || resp.Partial || resp.UsageMetadata == nil {
		return nil, nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.state(ctx.InvocationID()).tokens += int(resp.UsageMetadata.TotalTokenCount)
	return nil, nil
}

// beforeTool enforces the read-only allowlist and clamps the query window.
// A refused call is answered with an error result rather than a Go error:
// the model sees the refusal and can carry on with what it is allowed to do.
func (g *guard) beforeTool(ctx agent.Context, t tool.Tool, args map[string]any) (map[string]any, error) {
	if !readOnlyTools[t.Name()] {
		g.mu.Lock()
		s := g.state(ctx.InvocationID())
		s.denied = append(s.denied, t.Name())
		g.mu.Unlock()
		return map[string]any{"error": fmt.Sprintf("tool %q is not allowed: this agent is read-only", t.Name())}, nil
	}
	// JSON numbers from a real model arrive as float64; Go callers may pass int.
	switch w := args["window_minutes"].(type) {
	case float64:
		if int(w) > g.limits.MaxWindowMinutes {
			args["window_minutes"] = float64(g.limits.MaxWindowMinutes)
		}
	case int:
		if w > g.limits.MaxWindowMinutes {
			args["window_minutes"] = g.limits.MaxWindowMinutes
		}
	}
	return nil, nil
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"strconv"
	"strings"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/model"
)

// scenarioEnv selects how the stub model misbehaves, so that every guardrail
// can be seen firing without an API key.
const scenarioEnv = "INVESTIGATION_SCENARIO"

const (
	scenarioConverge = "converge"         // finds three facts, then nothing new
	scenarioRunaway  = "runaway-tools"    // calls tools forever, never answers
	scenarioEndless  = "endless-findings" // reports a "new" finding every round
)

// stubTokensPerCall is the usage every stub response reports, so the token
// meter has something to count.
const stubTokensPerCall = 1500

type toolCall struct {
	name string
	args map[string]any
}

type roundPlan struct {
	calls    []toolCall
	findings []string
}

// convergePlan is one plan per round; rounds past the end repeat the last.
// Round 2 tries restart_pod (refused by the allowlist) and asks for a
// four-hour log window (clamped to an hour).
var convergePlan = []roundPlan{
	{
		calls:    []toolCall{{toolMetrics, map[string]any{"service": "checkout-api", "window_minutes": 30}}},
		findings: []string{"checkout-api error rate rose to 18% at 10:07"},
	},
	{
		calls: []toolCall{
			{toolLogs, map[string]any{"service": "checkout-api", "window_minutes": 240}},
			{toolRestart, map[string]any{"service": "checkout-api"}},
		},
		findings: []string{"payments-db is refusing connections: too many clients"},
	},
	{
		calls:    []toolCall{{toolDeploys, map[string]any{"service": "checkout-api"}}},
		findings: []string{"v1.42 deployed at 10:00 lowered DB_POOL_SIZE from 50 to 20"},
	},
	{
		findings: []string{"v1.42 deployed at 10:00 lowered DB_POOL_SIZE from 50 to 20"},
	},
}

// stubModel is a scripted stand-in for the investigator LLM. It reads the
// round number from the prompt and counts the tool calls already made in this
// round, which is all it needs to drive llmagent's tool loop deterministically.
type stubModel struct{ scenario string }

var _ model.LLM = stubModel{}

func newStubModel() stubModel {
	s := os.Getenv(scenarioEnv)
	switch s {
	case scenarioConverge, scenarioRunaway, scenarioEndless:
		return stubModel{scenario: s}
	default:
		return stubModel{scenario: scenarioConverge}
	}
}

func (m stubModel) Name() string { return "stub-investigator-" + m.scenario }

func (m stubModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(m.respond(roundOf(req), functionCallsIn(req)), nil)
	}
}

func (m stubModel) respond(round, calls int) *model.LLMResponse {
	switch m.scenario {
	case scenarioRunaway:
		return callTool(toolCall{toolLogs, map[string]any{"service": "checkout-api"}})
	case scenarioEndless:
		plan := []toolCall{
			{toolMetrics, map[string]any{"service": "checkout-api"}},
			{toolLogs, map[string]any{"service": "checkout-api"}},
			{toolDeploys, map[string]any{"service": "checkout-api"}},
		}
		if calls < len(plan) {
			return callTool(plan[calls])
		}
		return answer([]string{fmt.Sprintf("possible anomaly #%d worth a closer look", round)})
	default:
		plan := convergePlan[min(max(round, 1), len(convergePlan))-1]
		if calls < len(plan.calls) {
			return callTool(plan.calls[calls])
		}
		return answer(plan.findings)
	}
}

func callTool(c toolCall) *model.LLMResponse {
	return withUsage(&genai.Content{
		Role:  genai.RoleModel,
		Parts: []*genai.Part{genai.NewPartFromFunctionCall(c.name, c.args)},
	})
}

func answer(findings []string) *model.LLMResponse {
	b, _ := json.Marshal(roundResult{Findings: findings})
	return withUsage(genai.NewContentFromText(string(b), genai.RoleModel))
}

func withUsage(c *genai.Content) *model.LLMResponse {
	return &model.LLMResponse{
		Content:       c,
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{TotalTokenCount: stubTokensPerCall},
		TurnComplete:  true,
	}
}

// roundOf reads the "Round: N" line the orchestrator writes into the prompt.
func roundOf(req *model.LLMRequest) int {
	for _, c := range req.Contents {
		if c == nil {
			continue
		}
		for _, p := range c.Parts {
			for line := range strings.Lines(p.Text) {
				if v, ok := strings.CutPrefix(strings.TrimSpace(line), "Round: "); ok {
					if n, err := strconv.Atoi(v); err == nil {
						return n
					}
				}
			}
		}
	}
	return 1
}

// functionCallsIn counts the tool calls the model already made this round.
// A single-turn node only sees the current round, so that is all of them.
func functionCallsIn(req *model.LLMRequest) int {
	n := 0
	for _, c := range req.Contents {
		if c == nil {
			continue
		}
		for _, p := range c.Parts {
			if p.FunctionCall != nil {
				n++
			}
		}
	}
	return n
}

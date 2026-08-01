package main

import (
	"context"
	"iter"
	"strings"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/model"
)

// stubModel is a deterministic stand-in for the diagnosis LLM. It echoes the
// evidence it was handed, which is enough to exercise the whole graph —
// fan-out, join, state hand-off, routing and the HITL pause — without an API
// key, and keeps the end-to-end output reproducible.
type stubModel struct{}

var _ model.LLM = stubModel{}

func (stubModel) Name() string { return "stub-diagnosis-model" }

func (stubModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{
			Content: &genai.Content{
				Role:  "model",
				Parts: []*genai.Part{{Text: "STUB DIAGNOSIS for:\n" + lastUserText(req)}},
			},
			TurnComplete: true,
		}, nil)
	}
}

// lastUserText returns the text of the most recent user turn in the request,
// which for a single-turn node is the upstream node's output.
func lastUserText(req *model.LLMRequest) string {
	if req == nil {
		return ""
	}
	for i := len(req.Contents) - 1; i >= 0; i-- {
		c := req.Contents[i]
		if c == nil || c.Role != "user" {
			continue
		}
		var sb strings.Builder
		for _, p := range c.Parts {
			sb.WriteString(p.Text)
		}
		if text := strings.TrimSpace(sb.String()); text != "" {
			return text
		}
	}
	return ""
}

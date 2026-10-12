// Command investigation-loop runs an incident investigation as an ADK Go 2.x
// dynamic workflow: a Go orchestrator runs an LLM agent round after round
// until it stops learning anything new, with guardrails at both levels of
// the loop.
//
//	START ─> investigate (dynamic, Go) ──RunNode × N──> investigator (llmagent + tools)
//
// Run it against Gemini:
//
//	export GOOGLE_API_KEY=...
//	go run . console
//
// Or without any credentials, using the scripted stub model:
//
//	go run . console
package main

import (
	"context"
	"log"
	"os"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/cmd/launcher"
	"google.golang.org/adk/v2/cmd/launcher/full"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/gemini"
)

const modelName = "gemini-flash-latest"

func main() {
	ctx := context.Background()

	m, err := newInvestigatorModel(ctx)
	if err != nil {
		log.Fatalf("failed to create investigator model: %v", err)
	}

	rootAgent, err := newInvestigationWorkflow(m, defaultLimits)
	if err != nil {
		log.Fatalf("failed to build investigation workflow: %v", err)
	}

	log.Printf("investigation loop ready — describe an incident, e.g. %q", "checkout-api のエラー率が上がっている")

	cfg := &launcher.Config{AgentLoader: agent.NewSingleLoader(rootAgent)}
	l := full.NewLauncher()
	if err := l.Execute(ctx, cfg, os.Args[1:]); err != nil {
		log.Fatalf("run failed: %v\n\n%s", err, l.CommandLineSyntax())
	}
}

// newInvestigatorModel returns Gemini when GOOGLE_API_KEY is set and the
// scripted stub otherwise, so the loop and its guardrails run out of the box.
func newInvestigatorModel(ctx context.Context) (model.LLM, error) {
	apiKey := os.Getenv("GOOGLE_API_KEY")
	if apiKey == "" {
		m := newStubModel()
		log.Printf("GOOGLE_API_KEY is not set — using the stub model (%s=%s)", scenarioEnv, m.scenario)
		return m, nil
	}
	return gemini.NewModel(ctx, modelName, &genai.ClientConfig{APIKey: apiKey})
}

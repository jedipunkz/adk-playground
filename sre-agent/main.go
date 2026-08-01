// Command sre-agent is an incident-triage agent built on the ADK Go 2.0 graph
// workflow engine.
//
// It demonstrates one split: the LLM reads the evidence and writes a
// diagnosis, and deterministic Go code picks the runbook. Every action the
// agent can take is a routed edge declared in graph.go, and the one action
// that mutates production is gated behind a human-in-the-loop approval.
//
//	START ─┬─> fetch_metrics ─┐
//	       ├─> fetch_logs ────┼─> gather ─> build_evidence ─> diagnose ─> decide_action ─┬─> propose_rollback (HITL)
//	       └─> fetch_deploys ─┘  (Join)        (Go)             (LLM)          (Go)      ├─> scale_out_service
//	                                                                                    └─> page_oncall
//
// Run it against Gemini:
//
//	export GOOGLE_API_KEY=...
//	go run . console
//
// Or without any credentials, using the deterministic stub diagnosis model:
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

	m, err := newDiagnosisModel(ctx)
	if err != nil {
		log.Fatalf("failed to create diagnosis model: %v", err)
	}

	rootAgent, err := newIncidentWorkflow(m)
	if err != nil {
		log.Fatalf("failed to build incident workflow: %v", err)
	}

	log.Printf("incident triage ready — send a message naming a service, e.g. %q", defaultService)
	log.Printf("%s selects the canned incident; available: %s", scenarioEnv, scenarioNames())

	cfg := &launcher.Config{AgentLoader: agent.NewSingleLoader(rootAgent)}
	l := full.NewLauncher()
	if err := l.Execute(ctx, cfg, os.Args[1:]); err != nil {
		log.Fatalf("run failed: %v\n\n%s", err, l.CommandLineSyntax())
	}
}

// newDiagnosisModel returns the Gemini model when GOOGLE_API_KEY is set and
// the deterministic stub otherwise, so the graph is runnable out of the box.
func newDiagnosisModel(ctx context.Context) (model.LLM, error) {
	apiKey := os.Getenv("GOOGLE_API_KEY")
	if apiKey == "" {
		log.Printf("GOOGLE_API_KEY is not set — falling back to the stub diagnosis model")
		return stubModel{}, nil
	}
	return gemini.NewModel(ctx, modelName, &genai.ClientConfig{APIKey: apiKey})
}

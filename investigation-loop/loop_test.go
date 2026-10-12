package main

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
)

// run drives the whole workflow through a real ADK runner with the stub model
// and returns the final report plus the window_minutes query_logs received.
func run(t *testing.T, scenario string) (string, []float64) {
	t.Helper()
	a, err := newInvestigationWorkflow(stubModel{scenario: scenario}, defaultLimits)
	if err != nil {
		t.Fatalf("building workflow: %v", err)
	}
	r, err := runner.NewInMemory("investigation-loop-test", a)
	if err != nil {
		t.Fatalf("creating runner: %v", err)
	}

	var reportText string
	var logWindows []float64
	msg := genai.NewContentFromText("checkout-api のエラー率が上がっている", genai.RoleUser)
	for ev, err := range r.Run(context.Background(), "user", "session-"+scenario, msg, agent.RunConfig{}) {
		if err != nil {
			t.Fatalf("run failed: %v", err)
		}
		if ev.Content == nil {
			continue
		}
		for _, p := range ev.Content.Parts {
			if strings.HasPrefix(p.Text, "Investigation stopped") {
				reportText = p.Text
			}
			if fr := p.FunctionResponse; fr != nil && fr.Name == toolLogs {
				if w, ok := fr.Response["window_minutes"].(float64); ok {
					logWindows = append(logWindows, w)
				}
			}
		}
	}
	if reportText == "" {
		t.Fatal("no report was emitted")
	}
	return reportText, logWindows
}

func TestConvergeStopsWhenNothingNewTurnsUp(t *testing.T) {
	restartCalls.Store(0)
	got, windows := run(t, scenarioConverge)

	for _, want := range []string{
		"after 5 round(s): " + stopNoProgress,
		"error rate rose to 18%",
		"too many clients",
		"DB_POOL_SIZE from 50 to 20",
		"- " + toolRestart, // listed under refused tool calls
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report is missing %q:\n%s", want, got)
		}
	}
	if n := restartCalls.Load(); n != 0 {
		t.Errorf("restart_pod ran %d time(s); the allowlist must refuse it", n)
	}
	if len(windows) != 1 || windows[0] != float64(defaultLimits.MaxWindowMinutes) {
		t.Errorf("query_logs saw window_minutes %v, want it clamped to [%d]", windows, defaultLimits.MaxWindowMinutes)
	}
}

func TestRunawayToolLoopIsCutPerRound(t *testing.T) {
	got, _ := run(t, scenarioRunaway)

	if !strings.Contains(got, "after 2 round(s): "+stopNoProgress) {
		t.Errorf("want the loop to stop after two empty rounds:\n%s", got)
	}
	if n := strings.Count(got, "model calls in one round"); n != 2 {
		t.Errorf("want both rounds cut by the model-call cap, got %d:\n%s", n, got)
	}
}

func TestEndlessFindingsHitTheTokenBudget(t *testing.T) {
	got, _ := run(t, scenarioEndless)

	// Four model calls per round at 1500 tokens: five rounds use 30000,
	// which is the budget, so round six never starts.
	if !strings.Contains(got, "after 5 round(s): "+stopBudget) {
		t.Errorf("want the token budget to stop the loop:\n%s", got)
	}
}

func TestParseRound(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"bare json", `{"findings": ["a", "b"]}`, 2},
		{"fenced json with prose", "Here you go:\n```json\n{\"findings\": [\"a\"], \"done\": true}\n```", 1},
		{"not json", "I could not find anything.", 0},
		{"broken json", `{"findings": ["a"`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := len(parseRound(tt.in).Findings); got != tt.want {
				t.Errorf("parseRound(%q) has %d findings, want %d", tt.in, got, tt.want)
			}
		})
	}
}

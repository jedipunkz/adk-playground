package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"
)

const (
	nodeInvestigate  = "investigate"
	nodeInvestigator = "investigator"
)

// roundTimeout bounds one round of the agent loop at the scheduler level, as
// a backstop for a model or tool that hangs rather than loops.
const roundTimeout = 2 * time.Minute

// Why the loop stopped. Exactly one applies to every run.
const (
	stopModelDone  = "model reported done"
	stopNoProgress = "no new findings"
	stopBudget     = "token budget exhausted"
	stopMaxRounds  = "round limit reached"
)

const investigatorInstruction = `You are an SRE investigating a live incident with read-only tools.

Each message is one investigation round. It lists what is already known. Use the
tools to find something that is NOT on that list, then reply with JSON only:

{"findings": ["<one fact per entry, grounded in tool output>"], "done": <true if the root cause is established>}

Never repeat a known finding. Never invent data that no tool returned.`

// roundResult is the contract between the investigator and the orchestrator.
type roundResult struct {
	Findings []string `json:"findings"`
	Done     bool     `json:"done"`
}

// report is the orchestrator's output.
type report struct {
	Rounds   int
	Stop     string
	Findings []string
	Tokens   int
	Cutoffs  []string
	Denied   []string
}

// newInvestigationWorkflow builds a one-node workflow: a dynamic orchestrator
// that runs the investigator agent round after round until a stop condition
// fires.
//
//	START ─> investigate (dynamic, Go) ──RunNode × N──> investigator (llmagent + tools)
func newInvestigationWorkflow(m model.LLM, l Limits) (agent.Agent, error) {
	g := newGuard(l)

	tools, err := newTools()
	if err != nil {
		return nil, err
	}
	investigator, err := llmagent.New(llmagent.Config{
		Name:                 nodeInvestigator,
		Model:                m,
		Description:          "investigates one round of an incident with read-only tools",
		Instruction:          investigatorInstruction,
		Tools:                tools,
		BeforeModelCallbacks: []llmagent.BeforeModelCallback{g.beforeModel},
		AfterModelCallbacks:  []llmagent.AfterModelCallback{g.afterModel},
		BeforeToolCallbacks:  []llmagent.BeforeToolCallback{g.beforeTool},
	})
	if err != nil {
		return nil, fmt.Errorf("creating investigator agent: %w", err)
	}
	// As a workflow node the agent runs single-turn: it sees only the round
	// prompt the orchestrator hands it, not the earlier rounds' transcripts.
	// What carries over between rounds is decided in Go (the findings list).
	investigatorNode, err := workflow.NewAgentNode(investigator, workflow.NodeConfig{Timeout: roundTimeout})
	if err != nil {
		return nil, fmt.Errorf("creating investigator node: %w", err)
	}

	orchestrator := workflow.NewDynamicNode[string, string](nodeInvestigate,
		func(ctx agent.Context, question string, emit func(*session.Event) error) (string, error) {
			r, err := investigate(ctx, question, investigatorNode, g)
			if err != nil {
				return "", err
			}
			text := r.String()
			// Content is what the console and Dev UI render; the return value
			// is the node's output for anything downstream.
			ev := session.NewEvent(ctx, ctx.InvocationID())
			ev.Content = genai.NewContentFromText(text, genai.RoleModel)
			if err := emit(ev); err != nil {
				return "", err
			}
			return text, nil
		},
		workflow.NodeConfig{})

	return workflowagent.New(workflowagent.Config{
		Name:        "investigation_loop",
		Description: "investigates an incident round by round until it stops learning anything new, under explicit guardrails",
		Edges:       workflow.Chain(workflow.Start, orchestrator),
		SubAgents:   []agent.Agent{investigator},
	})
}

// investigate is the loop-until-done orchestrator. The loop, the stop
// conditions and the state carried between rounds are all Go; the model only
// ever sees one round.
//
// It must stay deterministic: on resume ADK re-runs this function from the
// top and replays completed rounds from their checkpoints, which only works
// if the same inputs produce the same RunNode calls. Hence no clocks or
// randomness here.
func investigate(ctx agent.Context, question string, investigator workflow.Node, g *guard) (report, error) {
	id := ctx.InvocationID()
	defer g.finish(id)

	var (
		findings []string
		seen     = map[string]bool{}
		idle     int
		r        = report{Stop: stopMaxRounds}
	)
	for r.Rounds < g.limits.MaxRounds {
		if g.snapshot(id).tokens >= g.limits.TokenBudget {
			r.Stop = stopBudget
			break
		}
		r.Rounds++
		g.beginRound(id)

		out, err := workflow.RunNode[string](ctx, investigator, roundPrompt(question, r.Rounds, findings))
		if err != nil {
			return report{}, fmt.Errorf("round %d: %w", r.Rounds, err)
		}

		res := parseRound(out)
		added := 0
		for _, f := range res.Findings {
			key := strings.ToLower(strings.TrimSpace(f))
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			findings = append(findings, strings.TrimSpace(f))
			added++
		}
		if added == 0 {
			idle++
		} else {
			idle = 0
		}

		// The model may propose to stop, but only once it has something to
		// show for it. Every other stop condition is decided here, not by it.
		if res.Done && len(findings) > 0 {
			r.Stop = stopModelDone
			break
		}
		if idle >= g.limits.NoProgressRounds {
			r.Stop = stopNoProgress
			break
		}
	}

	s := g.snapshot(id)
	r.Findings, r.Tokens, r.Cutoffs, r.Denied = findings, s.tokens, s.cutoffs, s.denied
	return r, nil
}

func roundPrompt(question string, round int, known []string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Incident: %s\nRound: %d\n", question, round)
	if len(known) == 0 {
		sb.WriteString("Known findings: none yet.\n")
		return sb.String()
	}
	sb.WriteString("Known findings:\n")
	for _, f := range known {
		fmt.Fprintf(&sb, "- %s\n", f)
	}
	return sb.String()
}

// parseRound extracts the JSON object from the model's reply, tolerating
// code fences and prose around it. Anything unparseable yields an empty
// result, so a malformed reply can never add findings; it just counts as a
// round without progress.
func parseRound(out string) roundResult {
	start, end := strings.Index(out, "{"), strings.LastIndex(out, "}")
	if start < 0 || end <= start {
		return roundResult{}
	}
	var res roundResult
	if err := json.Unmarshal([]byte(out[start:end+1]), &res); err != nil {
		return roundResult{}
	}
	return res
}

func (r report) String() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Investigation stopped after %d round(s): %s. Tokens used: %d.\n", r.Rounds, r.Stop, r.Tokens)
	sb.WriteString("\nFindings:\n")
	if len(r.Findings) == 0 {
		sb.WriteString("- none\n")
	}
	for _, f := range r.Findings {
		fmt.Fprintf(&sb, "- %s\n", f)
	}
	if len(r.Cutoffs) > 0 {
		sb.WriteString("\nRounds cut short by guardrails:\n")
		for _, c := range r.Cutoffs {
			fmt.Fprintf(&sb, "- %s\n", c)
		}
	}
	if len(r.Denied) > 0 {
		sb.WriteString("\nTool calls refused:\n")
		for _, d := range r.Denied {
			fmt.Fprintf(&sb, "- %s\n", d)
		}
	}
	return sb.String()
}

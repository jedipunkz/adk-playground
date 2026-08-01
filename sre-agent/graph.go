package main

import (
	"fmt"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"
)

// Node names. The three collector names double as the keys of the JoinNode's
// output map, so buildEvidence looks its inputs up by these same constants.
const (
	nodeMetrics  = "fetch_metrics"
	nodeLogs     = "fetch_logs"
	nodeDeploys  = "fetch_deploys"
	nodeGather   = "gather"
	nodeEvidence = "build_evidence"
	nodeDiagnose = "diagnose"
	nodeDecide   = "decide_action"
	nodeRollback = "propose_rollback"
	nodeScaleOut = "scale_out_service"
	nodeEscalate = "page_oncall"
)

// collectTimeout bounds a single attempt at one collector. Combined with the
// default retry policy this keeps a stalled observability backend from
// wedging the whole triage.
const collectTimeout = 15 * time.Second

// diagnosisInstruction is the only prompt in the system. The explicit ban on
// proposing actions is the point: the model describes, the graph decides.
const diagnosisInstruction = `You are an SRE assistant reading the evidence for a live incident.

Write at most three sentences describing the most likely cause, grounded only in the
evidence you were given. Do not propose, recommend, or rank any remediation — the
runbook is chosen elsewhere by deterministic policy. Do not invent metrics or log
lines that are not in the evidence.`

// newActionNode wraps a terminal runbook so its result actually reaches the
// operator. A plain FunctionNode puts a string return in Event.Output only;
// the emitting variant additionally fills Event.Content, which is what the
// console and Dev UI render.
func newActionNode(name string, fn func(agent.Context, Evidence) (string, error)) *workflow.FunctionNode {
	return workflow.NewEmittingFunctionNode(name,
		func(ctx agent.Context, ev Evidence, _ func(*session.Event) error) (string, error) {
			return fn(ctx, ev)
		},
		workflow.NodeConfig{})
}

// newIncidentWorkflow assembles the triage graph:
//
//	START ─┬─> fetch_metrics ─┐
//	       ├─> fetch_logs ────┼─> gather ─> build_evidence ─> diagnose ─> decide_action ─┬─> propose_rollback (HITL)
//	       └─> fetch_deploys ─┘  (Join)        (Go)             (LLM)          (Go)      ├─> scale_out_service
//	                                                                                    └─> page_oncall
//
// The LLM node sits upstream of the branch but is not an input to it:
// decide_action reads the evidence build_evidence wrote to session state.
func newIncidentWorkflow(m model.LLM) (agent.Agent, error) {
	// The collectors talk to external APIs, so they are the only nodes that
	// need scheduler-level resilience.
	collectCfg := workflow.NodeConfig{
		RetryConfig: workflow.DefaultRetryConfig(),
		Timeout:     collectTimeout,
	}
	metricsNode := workflow.NewFunctionNode(nodeMetrics, fetchMetrics, collectCfg)
	logsNode := workflow.NewFunctionNode(nodeLogs, fetchLogs, collectCfg)
	deploysNode := workflow.NewFunctionNode(nodeDeploys, fetchDeploys, collectCfg)

	// gather fires once, after all three collectors finish, and hands
	// build_evidence a map[nodeName]output.
	gatherNode := workflow.NewJoinNode(nodeGather)
	evidenceNode := workflow.NewFunctionNode(nodeEvidence, buildEvidence, workflow.NodeConfig{})

	diagnoser, err := llmagent.New(llmagent.Config{
		Name:        nodeDiagnose,
		Model:       m,
		Description: "summarises incident evidence for a human responder",
		Instruction: diagnosisInstruction,
	})
	if err != nil {
		return nil, fmt.Errorf("creating diagnosis agent: %w", err)
	}
	// Wrapping an LlmAgent in an AgentNode defaults it to single-turn mode,
	// so it consumes build_evidence's output rather than the chat history.
	diagnoseNode, err := workflow.NewAgentNode(diagnoser, workflow.NodeConfig{})
	if err != nil {
		return nil, fmt.Errorf("creating diagnosis node: %w", err)
	}

	decideNode := workflow.NewEmittingFunctionNode(nodeDecide, decideAction, workflow.NodeConfig{})

	// Rollback mutates production, so it stays gated on a human. re-entry
	// mode (RerunOnResume=&true) re-runs proposeRollback on resume, which is
	// what lets ResumeOrRequestInput hand back the operator's answer.
	rerunOnResume := true
	rollbackNode := workflow.NewEmittingFunctionNode(nodeRollback, proposeRollback,
		workflow.NodeConfig{RerunOnResume: &rerunOnResume})
	scaleNode := newActionNode(nodeScaleOut, scaleOut)
	escalateNode := newActionNode(nodeEscalate, pageOncall)

	eb := workflow.NewEdgeBuilder()
	eb.AddFanOut(workflow.Start, metricsNode, logsNode, deploysNode)
	eb.AddFanIn(gatherNode, metricsNode, logsNode, deploysNode)
	eb.Add(gatherNode, evidenceNode)
	eb.Add(evidenceNode, diagnoseNode)
	eb.Add(diagnoseNode, decideNode)
	// Every action this agent can take is on this map. Adding a runbook
	// means adding a line here, which is what makes the blast radius
	// reviewable.
	eb.AddRoutes(decideNode, map[string]workflow.Node{
		routeRollback: rollbackNode,
		routeScaleOut: scaleNode,
		routeEscalate: escalateNode,
	})

	return workflowagent.New(workflowagent.Config{
		Name:        "incident_triage",
		Description: "collects incident evidence in parallel, has an LLM diagnose it, and picks a runbook by deterministic policy",
		Edges:       eb.Build(),
		// Registering the wrapped LLM agent lets the runner resolve the
		// author of its events.
		SubAgents: []agent.Agent{diagnoser},
	})
}

package main

import (
	"fmt"
	"sync/atomic"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// Tool names. readOnlyTools is the allowlist the tool guardrail enforces;
// restart_pod is deliberately left off it.
const (
	toolMetrics = "query_metrics"
	toolLogs    = "query_logs"
	toolDeploys = "list_deploys"
	toolRestart = "restart_pod"
)

var readOnlyTools = map[string]bool{
	toolMetrics: true,
	toolLogs:    true,
	toolDeploys: true,
}

// QueryArgs is shared by every tool. WindowMinutes is clamped by the tool
// guardrail before the tool sees it.
type QueryArgs struct {
	Service       string `json:"service"`
	WindowMinutes int    `json:"window_minutes,omitempty"`
}

// restartCalls counts real executions of restart_pod. The guardrail must keep
// it at zero; the end-to-end test asserts exactly that.
var restartCalls atomic.Int64

// newTools returns the investigator's toolbox. The tools answer from canned
// data so the sample runs without an observability backend.
//
// restart_pod is included on purpose. With an MCP toolset you get whatever
// the server exposes, write operations included, so the agent cannot rely on
// "the dangerous tool is simply not wired in". The allowlist in guardrails.go
// is what actually stops it.
func newTools() ([]tool.Tool, error) {
	specs := []struct {
		name, desc string
		fn         functiontool.Func[QueryArgs, map[string]any]
	}{
		{toolMetrics, "Returns error rate, p99 latency and CPU saturation for a service.", queryMetrics},
		{toolLogs, "Returns the top error log signatures for a service within a time window.", queryLogs},
		{toolDeploys, "Lists recent deploys of a service.", listDeploys},
		{toolRestart, "Restarts every pod of a service. Mutates production.", restartPod},
	}
	tools := make([]tool.Tool, 0, len(specs))
	for _, s := range specs {
		t, err := functiontool.New(functiontool.Config{Name: s.name, Description: s.desc}, s.fn)
		if err != nil {
			return nil, fmt.Errorf("creating tool %s: %w", s.name, err)
		}
		tools = append(tools, t)
	}
	return tools, nil
}

func queryMetrics(_ agent.Context, a QueryArgs) (map[string]any, error) {
	return map[string]any{
		"service":        a.Service,
		"window_minutes": a.WindowMinutes,
		"error_rate":     0.18,
		"latency_p99_ms": 2400,
		"cpu_saturation": 0.41,
		"errors_since":   "10:07",
	}, nil
}

func queryLogs(_ agent.Context, a QueryArgs) (map[string]any, error) {
	return map[string]any{
		"service":        a.Service,
		"window_minutes": a.WindowMinutes,
		"top_errors": []string{
			"pq: sorry, too many clients already (payments-db)",
			"context deadline exceeded calling payments-db",
		},
	}, nil
}

func listDeploys(_ agent.Context, a QueryArgs) (map[string]any, error) {
	return map[string]any{
		"service": a.Service,
		"deploys": []map[string]any{
			{"id": "v1.42", "at": "10:00", "diff": "DB_POOL_SIZE 50 -> 20"},
			{"id": "v1.41", "at": "yesterday 15:12", "diff": "copy changes only"},
		},
	}, nil
}

func restartPod(_ agent.Context, a QueryArgs) (map[string]any, error) {
	restartCalls.Add(1)
	return map[string]any{"service": a.Service, "restarted": true}, nil
}

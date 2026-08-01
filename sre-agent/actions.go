package main

import (
	"fmt"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"
)

// proposeRollback is the L2 runbook: the agent may propose the rollback but a
// human has to approve it before anything reaches production.
//
// The node is configured with RerunOnResume=&true, so on resume the whole
// function runs again and ResumeOrRequestInput returns the operator's answer
// instead of pausing a second time. Because the pause is reconstructed from
// session history, an approval that arrives after the process restarted still
// lands in the right place.
func proposeRollback(ctx agent.Context, ev Evidence, emit func(*session.Event) error) (string, error) {
	reply, err := workflow.ResumeOrRequestInput(ctx, emit, session.RequestInput{
		// Mixing in the invocation ID keeps the ID stable within one run
		// but unique across runs; a fixed literal would make clients treat
		// the request as already answered and skip the prompt.
		InterruptID: "approve_rollback-" + ctx.InvocationID(),
		Message: fmt.Sprintf(
			"Roll back %s to the previous release? (deploy %s, %d min ago, error rate %.1f%%) [yes/no]",
			ev.Service, ev.Deploy.LastDeployID, ev.Deploy.MinutesSinceDeploy, ev.Metrics.ErrorRate*100),
		Payload: ev,
	})
	if err != nil {
		return "", err
	}

	if !isApproval(reply) {
		return fmt.Sprintf("rollback of %s declined by operator; escalating to on-call", ev.Service), nil
	}
	// A real implementation would call the deployment API here.
	return fmt.Sprintf("rolled back %s from %s", ev.Service, ev.Deploy.LastDeployID), nil
}

// isApproval treats only an explicit affirmative as consent: anything the
// operator types that is not clearly "yes" leaves production untouched.
func isApproval(reply any) bool {
	if reply == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(fmt.Sprint(reply))) {
	case "yes", "y":
		return true
	default:
		return false
	}
}

// scaleOut is the L3 runbook: adding capacity is reversible and cheap, so it
// runs without an approval gate. Dropping the gate is the whole shape of an
// autonomy promotion — one node's NodeConfig, not a prompt rewrite.
func scaleOut(_ agent.Context, ev Evidence) (string, error) {
	return fmt.Sprintf("scaled out %s (cpu saturation %.0f%%, error rate %.1f%%)",
		ev.Service, ev.Metrics.CPUSaturation*100, ev.Metrics.ErrorRate*100), nil
}

// pageOncall is the terminal fallback for everything the policy does not
// recognise. Handing an unmatched incident to a human is the default, not an
// error path.
func pageOncall(_ agent.Context, ev Evidence) (string, error) {
	return fmt.Sprintf("paged on-call for %s: no runbook matched (error rate %.1f%%, p99 %dms, %d min since %s)",
		ev.Service, ev.Metrics.ErrorRate*100, ev.Metrics.LatencyP99Ms,
		ev.Deploy.MinutesSinceDeploy, ev.Deploy.LastDeployID), nil
}

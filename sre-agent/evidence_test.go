package main

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"testing"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
)

// mapState is the minimal session.State a node needs in a test.
type mapState map[string]any

func (s mapState) Get(key string) (any, error) {
	v, ok := s[key]
	if !ok {
		return nil, session.ErrStateKeyNotExist
	}
	return v, nil
}

func (s mapState) Set(key string, value any) error {
	s[key] = value
	return nil
}

func (s mapState) All() iter.Seq2[string, any] {
	return func(yield func(string, any) bool) {
		for k, v := range s {
			if !yield(k, v) {
				return
			}
		}
	}
}

// fakeContext is an agent.Context that supports only the handful of methods
// the pure nodes use. StrictContextMock panics on anything else, so a node
// that starts reaching for the model or the session service fails loudly here.
type fakeContext struct {
	agent.StrictContextMock
	state       session.State
	userContent *genai.Content
	invocation  string
}

func newFakeContext(t *testing.T, message string, state mapState) *fakeContext {
	t.Helper()
	return &fakeContext{
		StrictContextMock: agent.NewStrictContextMock(context.Background()),
		state:             state,
		userContent:       &genai.Content{Role: "user", Parts: []*genai.Part{{Text: message}}},
		invocation:        "inv-test",
	}
}

func (c *fakeContext) State() session.State        { return c.state }
func (c *fakeContext) UserContent() *genai.Content { return c.userContent }
func (c *fakeContext) InvocationID() string        { return c.invocation }

func TestNormalizeService(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"exact match", "payment-api", "payment-api"},
		{"embedded in a sentence", "checkout-api is throwing 500s", "checkout-api"},
		{"case insensitive", "SEARCH-API latency spike", "search-api"},
		{"unknown service falls back", "billing-api is down", defaultService},
		{"empty message falls back", "", defaultService},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeService(tc.in); got != tc.want {
				t.Errorf("normalizeService(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestDecodeInto(t *testing.T) {
	want := Metrics{ErrorRate: 0.18, LatencyP99Ms: 2400, CPUSaturation: 0.42}

	t.Run("already typed", func(t *testing.T) {
		got, err := decodeInto[Metrics](want)
		if err != nil {
			t.Fatalf("decodeInto: %v", err)
		}
		if got != want {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})

	// Values that survive session persistence come back as map[string]any,
	// which is the case decodeInto exists for.
	t.Run("round-tripped through json", func(t *testing.T) {
		b, err := json.Marshal(want)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var loose map[string]any
		if err := json.Unmarshal(b, &loose); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		got, err := decodeInto[Metrics](loose)
		if err != nil {
			t.Fatalf("decodeInto: %v", err)
		}
		if got != want {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})

	t.Run("missing value", func(t *testing.T) {
		if _, err := decodeInto[Metrics](nil); err == nil {
			t.Error("expected an error for a nil input")
		}
	})
}

func TestBuildEvidenceStoresRawEvidence(t *testing.T) {
	state := mapState{}
	ctx := newFakeContext(t, "payment-api error rate is up", state)

	gathered := map[string]any{
		nodeMetrics: Metrics{ErrorRate: 0.18, LatencyP99Ms: 2400, CPUSaturation: 0.42},
		nodeLogs:    LogDigest{TopErrors: []string{"panic: nil map write"}, Volume: 18342},
		nodeDeploys: DeployInfo{LastDeployID: "rel-2026-07-31.3", MinutesSinceDeploy: 7},
	}

	prompt, err := buildEvidence(ctx, gathered)
	if err != nil {
		t.Fatalf("buildEvidence: %v", err)
	}
	if !strings.Contains(prompt, "payment-api") {
		t.Errorf("prompt does not mention the target service:\n%s", prompt)
	}

	raw, err := state.Get(stateEvidence)
	if err != nil {
		t.Fatalf("evidence was not stored: %v", err)
	}
	ev, err := decodeInto[Evidence](raw)
	if err != nil {
		t.Fatalf("decoding stored evidence: %v", err)
	}
	if ev.Service != "payment-api" {
		t.Errorf("Service = %q, want %q", ev.Service, "payment-api")
	}
	if ev.Deploy.MinutesSinceDeploy != 7 {
		t.Errorf("MinutesSinceDeploy = %d, want 7", ev.Deploy.MinutesSinceDeploy)
	}
}

func TestBuildEvidenceFailsOnMissingCollector(t *testing.T) {
	ctx := newFakeContext(t, defaultService, mapState{})

	// fetch_logs never reported: the node must fail rather than route on a
	// zero-valued digest.
	gathered := map[string]any{
		nodeMetrics: Metrics{ErrorRate: 0.18},
		nodeDeploys: DeployInfo{LastDeployID: "rel-1", MinutesSinceDeploy: 7},
	}
	if _, err := buildEvidence(ctx, gathered); err == nil {
		t.Fatal("expected an error when a collector's output is missing")
	}
}

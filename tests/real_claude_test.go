//go:build e2e_real

// Package e2e real-CLI tests. Gated behind the `e2e_real` build tag so they run
// only when explicitly requested (they spawn real agent CLIs and may cost
// tokens). Run with, e.g.:
//
//	go test -tags e2e_real -run TestReal_Claude ./tests/...
//
// These prove unio drives a genuine coding-agent end to end via the facade.
package tests

import (
	"context"
	"strings"
	"testing"

	"github.com/Fullstop000/unio"
)

// Real Claude E2E proves Agent -> Session -> Run drives a live CLI end to end.
func TestReal_Claude_Run(t *testing.T) {
	agent, err := unio.New(unio.Claude)
	if err != nil {
		t.Skip("claude CLI not installed; skipping real E2E")
	}
	defer agent.Close()
	session, err := agent.NewSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	res, err := session.Run(context.Background(), "Reply with exactly one word: ping")
	if err != nil {
		skipClaudeEnvError(t, err)
		t.Fatalf("run: %v", err)
	}
	if strings.TrimSpace(res.Text) == "" {
		t.Fatal("expected non-empty assistant text")
	}
	if res.SessionID == "" {
		t.Fatal("expected a runtime session id")
	}
	t.Logf("claude said %q", res.Text)
	for model, u := range res.Usage {
		t.Logf("usage[%s]: in=%d out=%d cost=$%.4f", model, u.InputTokens, u.OutputTokens, u.CostUSD)
	}
}

// Real Claude streaming via the facade Stream handle.
func TestReal_Claude_Stream(t *testing.T) {
	agent, err := unio.New(unio.Claude)
	if err != nil {
		t.Skip("claude CLI not installed; skipping real E2E")
	}
	defer agent.Close()
	s, err := agent.NewSession(context.Background())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var text strings.Builder
	st, err := s.Stream(context.Background(), "Reply with exactly one word: ping")
	if err != nil {
		t.Fatal(err)
	}
	for st.Next() {
		if ev := st.Event(); ev.Kind == unio.KindText {
			text.WriteString(ev.Text)
		}
	}
	if _, err := st.Result(); err != nil {
		skipClaudeEnvError(t, err)
		t.Fatalf("result: %v", err)
	}
	if strings.TrimSpace(text.String()) == "" {
		t.Fatal("stream produced no text")
	}
	t.Logf("streamed: %q", text.String())
}

func skipClaudeEnvError(t *testing.T, err error) {
	t.Helper()
	msg := err.Error()
	for _, needle := range []string{
		"does not have a valid CodingPlan subscription",
		"subscription has expired",
		"Please login",
		"not authenticated",
		"unauthorized",
	} {
		if strings.Contains(msg, needle) {
			t.Skipf("claude runtime unavailable in this environment: %v", err)
		}
	}
}

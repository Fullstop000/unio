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

// Real Claude E2E via the high-level facade. Proves unio.Run drives a live
// claude CLI end to end in one call.
func TestReal_Claude_Run(t *testing.T) {
	if !unio.Installed(unio.Claude) {
		t.Skip("claude CLI not installed; skipping real E2E")
	}
	res, err := unio.Run(context.Background(), unio.Claude, "Reply with exactly one word: ping")
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
	t.Logf("claude said %q; finish=%s", res.Text, res.FinishReason)
	for model, u := range res.Usage {
		t.Logf("usage[%s]: in=%d out=%d cost=$%.4f", model, u.InputTokens, u.OutputTokens, u.CostUSD)
	}
}

// Real Claude streaming via the facade Stream handle.
func TestReal_Claude_Stream(t *testing.T) {
	if !unio.Installed(unio.Claude) {
		t.Skip("claude CLI not installed; skipping real E2E")
	}
	s, err := unio.Start(context.Background(), unio.Claude)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	var text strings.Builder
	st := s.Prompt(context.Background(), "Reply with exactly one word: ping")
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

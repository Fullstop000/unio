//go:build e2e_real

package tests

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Fullstop000/unio"
)

// Real Codex E2E via the facade — proves unio.Run drives the app-server
// protocol end to end.
func TestReal_Codex_Run(t *testing.T) {
	if !unio.Installed(unio.Codex) {
		t.Skip("codex CLI not installed; skipping real E2E")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	res, err := unio.Run(ctx, unio.Codex, "Reply with exactly one word: pong", unio.WithModel("gpt-5.5"))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.TrimSpace(res.Text) == "" {
		t.Fatal("expected non-empty assistant text")
	}
	t.Logf("codex said %q; finish=%s", res.Text, res.FinishReason)
	for model, u := range res.Usage {
		t.Logf("usage[%s]: in=%d out=%d cacheRead=%d", model, u.InputTokens, u.OutputTokens, u.CacheReadTokens)
	}
}

// Real Codex graceful mid-turn interrupt via the facade — the capability Claude
// headless lacks. Start a long turn, Cancel it, then prove the session survives
// and completes a follow-up turn.
func TestReal_Codex_Interrupt(t *testing.T) {
	if !unio.Installed(unio.Codex) {
		t.Skip("codex CLI not installed; skipping real E2E")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	s, err := unio.Start(ctx, unio.Codex, unio.WithModel("gpt-5.5"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	// Fire a long turn; the stream drains when the turn ends (naturally or
	// interrupted).
	st := s.Prompt(ctx, "Count slowly from 1 to 500, one number per line.")
	done := make(chan struct{})
	go func() {
		for st.Next() {
		}
		_, _ = st.Result()
		close(done)
	}()

	time.Sleep(3 * time.Second)
	aborted, err := s.Cancel(ctx)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	t.Logf("cancel aborted=%v", aborted)

	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("interrupted turn did not finalise")
	}

	// Session must survive the interrupt and answer a follow-up.
	res, err := s.Prompt(ctx, "Reply with exactly one word: ok").Result()
	if err != nil {
		t.Fatalf("follow-up prompt after interrupt: %v", err)
	}
	if strings.TrimSpace(res.Text) == "" {
		t.Fatal("session did not answer after interrupt")
	}
	t.Logf("session survived interrupt; follow-up said %q", res.Text)
}

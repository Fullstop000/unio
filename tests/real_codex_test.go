//go:build e2e_real

package tests

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Fullstop000/unio"
)

// Real Codex E2E proves Agent -> Session -> Run drives app-server end to end.
func TestReal_Codex_Run(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	agent, err := unio.New(ctx, unio.Codex, unio.WithModel("gpt-5.5"))
	if err != nil {
		t.Skip("codex CLI not installed; skipping real E2E")
	}
	defer agent.Close()
	session, err := agent.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	res, err := session.Run("Reply with exactly one word: pong")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.TrimSpace(res.Text) == "" {
		t.Fatal("expected non-empty assistant text")
	}
	t.Logf("codex said %q", res.Text)
	for model, u := range res.Usage {
		t.Logf("usage[%s]: in=%d out=%d cacheRead=%d", model, u.InputTokens, u.OutputTokens, u.CacheReadTokens)
	}
	raw, err := session.Raw()
	if err != nil || raw.Format != unio.SessionDataJSONL || len(raw.Data) == 0 {
		t.Fatalf("raw session data: format=%q bytes=%d error=%v", raw.Format, len(raw.Data), err)
	}
	stats, err := session.TokenStatistics()
	if err != nil || stats.InputTokens == 0 || stats.OutputTokens == 0 {
		t.Fatalf("session token statistics = %+v, error = %v", stats, err)
	}
}

// Real Codex graceful mid-turn interrupt via the facade — the capability Claude
// headless lacks. Start a long turn, Cancel it, then prove the session survives
// and completes a follow-up turn.
func TestReal_Codex_Interrupt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	agent, err := unio.New(ctx, unio.Codex, unio.WithModel("gpt-5.5"))
	if err != nil {
		t.Skip("codex CLI not installed; skipping real E2E")
	}
	defer agent.Close()
	s, err := agent.NewSession()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Fire a long turn; the stream drains when the turn ends (naturally or
	// interrupted).
	st, err := s.Stream("Count slowly from 1 to 500, one number per line.")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		for st.Next() {
		}
		_, _ = st.Result()
		close(done)
	}()

	time.Sleep(3 * time.Second)
	if err := s.Interrupt(); err != nil {
		t.Fatalf("interrupt: %v", err)
	}

	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("interrupted turn did not finalise")
	}

	// Session must survive the interrupt and answer a follow-up.
	res, err := s.Run("Reply with exactly one word: ok")
	if err != nil {
		t.Fatalf("follow-up prompt after interrupt: %v", err)
	}
	if strings.TrimSpace(res.Text) == "" {
		t.Fatal("session did not answer after interrupt")
	}
	t.Logf("session survived interrupt; follow-up said %q", res.Text)
}

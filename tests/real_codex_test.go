//go:build e2e_real

package tests

import (
	"context"
	"os"
	"path/filepath"
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

// forceApprovalCodexHome builds a throwaway CODEX_HOME that reuses the caller's
// credentials but forces codex to escalate untrusted commands for approval,
// regardless of the user's real config. The auth file is symlinked, not copied,
// so no credential bytes ever land in the temp dir. Returns "" if codex has no
// auth to link.
func forceApprovalCodexHome(t *testing.T) string {
	t.Helper()
	real, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	authSrc := filepath.Join(real, ".codex", "auth.json")
	if _, err := os.Stat(authSrc); err != nil {
		return ""
	}
	home := t.TempDir()
	if err := os.Symlink(authSrc, filepath.Join(home, "auth.json")); err != nil {
		t.Fatalf("symlink auth.json: %v", err)
	}
	config := "approval_policy = \"untrusted\"\nsandbox_mode = \"read-only\"\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	return home
}

// Real Codex block E2E: with approval forced on, prompting codex to run an
// untrusted shell command must surface a tool-approval Block through the facade,
// and Continue("allow_once") must resume the same session to completion.
func TestReal_Codex_BlockAndContinue(t *testing.T) {
	home := forceApprovalCodexHome(t)
	if home == "" {
		t.Skip("codex auth.json not found; skipping real block E2E")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	agent, err := unio.New(ctx, unio.Codex, unio.WithEnv("CODEX_HOME="+home))
	if err != nil {
		t.Skipf("codex unavailable: %v", err)
	}
	defer agent.Close()
	session, err := agent.NewSession()
	if err != nil {
		t.Fatal(err)
	}

	// curl is not in codex's trusted set, so an untrusted policy escalates it.
	blocked, err := session.Run("Use your shell tool to run exactly this command: `curl -s https://example.com`. Actually execute it via the shell tool; do not just describe it.")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if blocked.Blocked == nil {
		t.Fatalf("expected a tool-approval block; state=%q text=%q", session.State(), blocked.Text)
	}
	if blocked.Blocked.Kind != unio.BlockedToolApproval {
		t.Fatalf("blocked kind = %q; want %q", blocked.Blocked.Kind, unio.BlockedToolApproval)
	}
	if session.State() != unio.Blocked {
		t.Fatalf("state after block = %q; want blocked", session.State())
	}
	t.Logf("codex blocked: kind=%s message=%q options=%+v", blocked.Blocked.Kind, blocked.Blocked.Message, blocked.Blocked.Options)

	continued, err := session.Continue("allow_once")
	if err != nil {
		t.Fatalf("continue: %v", err)
	}
	if session.State() != unio.Idle {
		t.Fatalf("state after continue = %q; want idle", session.State())
	}
	t.Logf("codex resumed after approval; text=%q", continued.Text)
}

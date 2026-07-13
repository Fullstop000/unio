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
	"github.com/Fullstop000/unio/driver"
	"github.com/Fullstop000/unio/driver/acp"
)

const openCodeDeepSeekV4Flash = "deepseek/deepseek-v4-flash"

// TestReal_ACP_TraeXCore is the primary real-runtime proof for the shared ACP
// state machine. The other runtimes have protocol-only tests below so local
// provider credentials do not masquerade as transport failures.
func TestReal_ACP_TraeXCore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	agent, err := unio.New(unio.TraeX)
	if err != nil {
		t.Skipf("traex unavailable: %v", err)
	}
	defer agent.Close()
	if sessions, err := agent.ListSessions(ctx); err != nil {
		t.Fatalf("session/list: %v", err)
	} else {
		t.Logf("traex listed %d sessions", len(sessions))
	}
	session, err := agent.NewSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.Run(ctx, "Reply with exactly one word: ok")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.TrimSpace(result.Text) == "" || result.SessionID == "" {
		t.Fatalf("result = %+v", result)
	}
	stats, err := session.TokenStatistics(ctx)
	if err != nil {
		t.Fatalf("session token statistics: %v", err)
	}
	if stats.InputTokens == 0 || stats.OutputTokens == 0 {
		t.Fatalf("session token statistics = %+v", stats)
	}
	raw, err := session.Raw(ctx)
	if err != nil || raw.Format != unio.SessionDataJSONL || len(raw.Data) == 0 {
		t.Fatalf("raw session data: format=%q bytes=%d error=%v", raw.Format, len(raw.Data), err)
	}
}

func TestReal_ACP_KimiSessionTokenStatistics(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	d := acp.New(acp.Kimi)
	probe, err := d.Probe(ctx)
	if err != nil || probe == driver.AuthNotInstalled {
		t.Skipf("kimi unavailable: probe=%+v err=%v", probe, err)
	}
	defer d.Close()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	var sessionIDs []string
	for _, root := range []string{filepath.Join(home, ".kimi-code", "sessions"), filepath.Join(home, ".kimi", "sessions")} {
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() || entry.Name() != "wire.jsonl" {
				return nil
			}
			sessionDir := filepath.Dir(path)
			if filepath.Base(sessionDir) == "main" && filepath.Base(filepath.Dir(sessionDir)) == "agents" {
				sessionDir = filepath.Dir(filepath.Dir(sessionDir))
			}
			sessionIDs = append(sessionIDs, filepath.Base(sessionDir))
			return nil
		})
	}
	for _, sessionID := range sessionIDs {
		attachment, openErr := d.OpenSession(ctx, "kimi-statistics", driver.AgentSpec{}, driver.OpenParams{ResumeSessionID: driver.SessionID(sessionID)})
		if openErr != nil {
			continue
		}
		raw, rawErr := attachment.Session.Raw(ctx)
		if rawErr != nil || raw.Format != driver.SessionDataJSONL || len(raw.Data) == 0 {
			_ = attachment.Session.Close(context.Background())
			continue
		}
		stats, statsErr := attachment.Session.TokenStatistics(ctx)
		_ = attachment.Session.Close(context.Background())
		if statsErr == nil && stats.InputTokens > 0 && stats.OutputTokens > 0 {
			t.Logf("kimi session usage: input=%d output=%d cache_read=%d cache_write=%d",
				stats.InputTokens, stats.OutputTokens, stats.CacheReadTokens, stats.CacheWriteTokens)
			return
		}
	}
	t.Skipf("no Kimi session with persisted token statistics found among %d session files", len(sessionIDs))
}

func TestReal_ACP_KimiProtocol(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	d := acp.New(acp.Kimi)
	probe, err := d.Probe(ctx)
	if err != nil || probe == driver.AuthNotInstalled {
		t.Skipf("kimi unavailable: probe=%+v err=%v", probe, err)
	}
	defer d.Close()
	spec := driver.AgentSpec{Cwd: cwd}
	listed, err := d.ListSessions(ctx, driver.ListSessionsParams{Cwd: cwd, Spec: spec})
	if err != nil {
		t.Fatalf("session/list: %v", err)
	}
	t.Logf("kimi listed %d sessions", len(listed))
	if len(listed) > 0 {
		resumeSpec := spec
		if listed[0].Cwd != "" {
			resumeSpec.Cwd = listed[0].Cwd
		}
		resumed, err := d.OpenSession(ctx, "kimi-resume", resumeSpec, driver.OpenParams{ResumeSessionID: listed[0].SessionID})
		if err != nil {
			t.Fatal(err)
		}
		_ = resumed.Events.Subscribe()
		if err := resumed.Session.Run(ctx, nil); err != nil {
			t.Fatalf("session/resume: %v", err)
		}
		if resumed.Session.SessionID() != listed[0].SessionID {
			t.Fatalf("resumed ID = %q, want %q", resumed.Session.SessionID(), listed[0].SessionID)
		}
		defer resumed.Session.Close(ctx)
	}

	att, err := d.OpenSession(ctx, "kimi-protocol", spec, driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	_ = att.Events.Subscribe()
	if err := att.Session.Run(ctx, nil); err != nil {
		t.Fatalf("session/new: %v", err)
	}
	if att.Session.SessionID() == "" {
		t.Fatal("session/new returned no ID")
	}
	if err := att.Session.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestReal_ACP_OpenCodeProtocol(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	d := acp.New(acp.OpenCode)
	probe, err := d.Probe(ctx)
	if err != nil || probe == driver.AuthNotInstalled {
		t.Skipf("opencode unavailable: probe=%+v err=%v", probe, err)
	}
	defer d.Close()
	spec := driver.AgentSpec{Cwd: cwd, Model: openCodeDeepSeekV4Flash, ExtraArgs: []string{"--pure"}}
	listed, err := d.ListSessions(ctx, driver.ListSessionsParams{Cwd: cwd, Spec: spec})
	if err != nil {
		t.Fatalf("session/list: %v", err)
	}
	t.Logf("opencode listed %d sessions", len(listed))
	att, err := d.OpenSession(ctx, "opencode-protocol", spec, driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	_ = att.Events.Subscribe()
	if err := att.Session.Run(ctx, nil); err != nil {
		t.Fatalf("session/new: %v", err)
	}
	if att.Session.SessionID() == "" {
		t.Fatal("session/new returned no ID")
	}
	if err := att.Session.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if len(listed) > 0 {
		resumeSpec := spec
		if listed[0].Cwd != "" {
			resumeSpec.Cwd = listed[0].Cwd
		}
		resumeDriver := acp.New(acp.OpenCode)
		defer resumeDriver.Close()
		resumed, err := resumeDriver.OpenSession(ctx, "opencode-resume", resumeSpec, driver.OpenParams{ResumeSessionID: listed[0].SessionID})
		if err != nil {
			t.Fatal(err)
		}
		_ = resumed.Events.Subscribe()
		if err := resumed.Session.Run(ctx, nil); err != nil {
			t.Fatalf("session/resume: %v", err)
		}
		if resumed.Session.SessionID() != listed[0].SessionID {
			t.Fatalf("resumed ID = %q, want %q", resumed.Session.SessionID(), listed[0].SessionID)
		}
		if err := resumed.Session.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReal_ACP_OpenCodeDeepSeekV4Flash(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	agent, err := unio.New(
		unio.OpenCode,
		unio.WithModel(openCodeDeepSeekV4Flash),
		unio.WithExtraArgs("--pure"),
	)
	if err != nil {
		t.Skipf("opencode unavailable: %v", err)
	}
	defer agent.Close()
	session, err := agent.NewSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.Run(ctx, "Reply with exactly one word: ok")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(result.Text) == "" || result.SessionID == "" {
		t.Fatalf("result = %+v", result)
	}
	t.Logf("opencode %s said %q", openCodeDeepSeekV4Flash, strings.TrimSpace(result.Text))
}

func TestReal_ACP_TraeXResume(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	agent, err := unio.New(unio.TraeX)
	if err != nil {
		t.Skipf("traex unavailable: %v", err)
	}
	session, err := agent.NewSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first, err := session.Run(ctx, "Reply with exactly one word: first")
	if err != nil {
		t.Fatal(err)
	}
	id := first.SessionID
	if id == "" {
		t.Fatal("TraeX returned no session ID")
	}
	if err := agent.Close(); err != nil {
		t.Fatal(err)
	}

	resumingAgent, err := unio.New(unio.TraeX)
	if err != nil {
		t.Fatal(err)
	}
	defer resumingAgent.Close()
	resumed, err := resumingAgent.GetSession(ctx, id)
	if err != nil {
		t.Fatalf("GetSession(%q): %v", id, err)
	}
	result, err := resumed.Run(ctx, "Reply with exactly one word: resumed")
	if err != nil {
		t.Fatal(err)
	}
	if result.SessionID != id || strings.TrimSpace(result.Text) == "" {
		t.Fatalf("resumed result = %+v", result)
	}
}

func TestReal_ACP_TraeXInterrupt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	agent, err := unio.New(unio.TraeX)
	if err != nil {
		t.Skipf("traex unavailable: %v", err)
	}
	defer agent.Close()
	session, err := agent.NewSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := session.Stream(ctx, "Write every integer from 1 to 10000, one per line.")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	if err := session.Interrupt(ctx); err != nil {
		t.Fatal(err)
	}
	result, err := stream.Result()
	if err != nil {
		t.Fatal(err)
	}
	if !result.Interrupted {
		t.Fatalf("interrupt result = %+v", result)
	}
	followup, err := session.Run(ctx, "Reply with exactly one word: alive")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(followup.Text) == "" {
		t.Fatalf("follow-up result = %+v", followup)
	}
}

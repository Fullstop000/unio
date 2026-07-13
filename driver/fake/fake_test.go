package fake

import (
	"context"
	"testing"
	"time"

	"github.com/Fullstop000/unio/driver"
	"github.com/Fullstop000/unio/errs"
)

func collect(t *testing.T, ch <-chan driver.AgentEvent, want int, timeout time.Duration) []driver.AgentEvent {
	t.Helper()
	var out []driver.AgentEvent
	deadline := time.After(timeout)
	for len(out) < want {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-deadline:
			return out
		}
	}
	return out
}

func TestFakeLifecycleNewSession(t *testing.T) {
	d := New(context.Background(), driver.AgentSpec{})
	d.ScriptNextSession(Script{
		Items: []driver.AgentEventItem{
			{Kind: driver.ItemThinking, Text: "hmm"},
			{Kind: driver.ItemText, Text: "hello"},
		},
		Result: driver.RunResult{
			FinishReason: driver.FinishNatural,
			Usage:        map[string]driver.TokenUsage{"m": {InputTokens: 4, OutputTokens: 2}},
		},
	})

	att, err := d.OpenSession(driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	ch := att.Events.Subscribe()

	if err := att.Session.Run(nil); err != nil {
		t.Fatal(err)
	}
	if att.Session.SessionID() == "" {
		t.Fatal("Run should attach a session id")
	}
	if st := att.Session.ProcessState(); st.Phase != driver.PhaseActive {
		t.Fatalf("expected Active after Run, got %s", st.Phase)
	}

	runID, err := att.Session.Prompt(driver.PromptReq{Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}

	// Expected stream: Starting, SessionAttached, Active (from Run),
	// then PromptInFlight, Output(thinking), Output(text), Output(turn_end),
	// Completed, Active (from Prompt).
	evs := collect(t, ch, 9, 2*time.Second)

	var sawAttached, sawCompleted, sawThinking, sawText, sawTurnEnd bool
	for _, ev := range evs {
		switch ev.Type {
		case driver.EventSessionAttached:
			sawAttached = true
		case driver.EventCompleted:
			sawCompleted = true
			if ev.RunID != runID {
				t.Fatalf("completed run id %q != prompt run id %q", ev.RunID, runID)
			}
			if ev.Result.Usage["m"].InputTokens != 4 {
				t.Fatalf("usage not propagated: %+v", ev.Result.Usage)
			}
		case driver.EventOutput:
			switch ev.Item.Kind {
			case driver.ItemThinking:
				sawThinking = true
			case driver.ItemText:
				sawText = true
			case driver.ItemTurnEnd:
				sawTurnEnd = true
			}
		}
	}
	if !sawAttached || !sawCompleted || !sawThinking || !sawText || !sawTurnEnd {
		t.Fatalf("missing expected events: attached=%v completed=%v thinking=%v text=%v turnEnd=%v (%d events)",
			sawAttached, sawCompleted, sawThinking, sawText, sawTurnEnd, len(evs))
	}

	if err := att.Session.Close(); err != nil {
		t.Fatal(err)
	}
	if st := att.Session.ProcessState(); st.Phase != driver.PhaseClosed {
		t.Fatalf("expected Closed, got %s", st.Phase)
	}
}

func TestFakeResumeReusesSessionID(t *testing.T) {
	d := New(context.Background(), driver.AgentSpec{})

	att, err := d.OpenSession(driver.OpenParams{ResumeSessionID: "prior-abc"})
	if err != nil {
		t.Fatal(err)
	}
	_ = att.Events.Subscribe()
	if err := att.Session.Run(nil); err != nil {
		t.Fatal(err)
	}
	if got := att.Session.SessionID(); got != "prior-abc" {
		t.Fatalf("resume should reuse the prior id, got %q", got)
	}
}

func TestFakeFailScript(t *testing.T) {
	d := New(context.Background(), driver.AgentSpec{})
	d.ScriptNextSession(Script{FailWith: driver.NewRuntimeReportedError("boom")})

	att, _ := d.OpenSession(driver.OpenParams{})
	ch := att.Events.Subscribe()
	_ = att.Session.Run(nil)
	_, _ = att.Session.Prompt(driver.PromptReq{Text: "x"})

	evs := collect(t, ch, 12, time.Second)
	var failed bool
	for _, ev := range evs {
		if ev.Type == driver.EventFailed && ev.Err != nil && ev.Err.Kind == driver.ErrRuntimeReported {
			failed = true
		}
	}
	if !failed {
		t.Fatalf("expected a Failed event with runtime_reported kind, got %d events", len(evs))
	}
}

func TestFakeInterruptNotInFlight(t *testing.T) {
	d := New(context.Background(), driver.AgentSpec{})
	att, _ := d.OpenSession(driver.OpenParams{})
	_ = att.Events.Subscribe()
	_ = att.Session.Run(nil)

	if err := att.Session.Interrupt(); err != nil {
		t.Fatal(err)
	}
}

func TestFakeInterruptIdleIsNoop(t *testing.T) {
	d := New(context.Background(), driver.AgentSpec{})
	att, err := d.OpenSession(driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	_ = att.Events.Subscribe()
	if err := att.Session.Run(nil); err != nil {
		t.Fatal(err)
	}
	if err := att.Session.Interrupt(); err != nil {
		t.Fatalf("idle interrupt: %v", err)
	}
}

func TestFakeBlockedTurnContinues(t *testing.T) {
	d := New(context.Background(), driver.AgentSpec{})
	d.ScriptNextSession(
		Script{Blocked: &driver.BlockedReason{
			Kind: driver.BlockedToolApproval,
			Options: []driver.BlockOption{
				{Value: "allow_once", Label: "Allow once"},
				{Value: "deny", Label: "Deny"},
			},
		}},
		Script{Items: []driver.AgentEventItem{{Kind: driver.ItemText, Text: "continued"}}},
	)
	att, _ := d.OpenSession(driver.OpenParams{})
	events := att.Events.Subscribe()
	_ = att.Session.Run(nil)
	run, _ := att.Session.Prompt(driver.PromptReq{Text: "go"})
	ev := waitRunEvent(t, events, run, driver.EventBlocked)
	if ev.Blocked == nil || ev.Blocked.Kind != driver.BlockedToolApproval {
		t.Fatalf("unexpected block: %+v", ev)
	}

	continued, err := att.Session.Continue("allow_once")
	if err != nil {
		t.Fatal(err)
	}
	waitRunEvent(t, events, continued, driver.EventCompleted)
}

func TestFakeInterruptHeldTurn(t *testing.T) {
	gate := make(chan struct{})
	d := New(context.Background(), driver.AgentSpec{})
	d.ScriptNextSession(Script{Wait: gate})
	att, _ := d.OpenSession(driver.OpenParams{})
	events := att.Events.Subscribe()
	_ = att.Session.Run(nil)
	run, _ := att.Session.Prompt(driver.PromptReq{Text: "long"})
	if err := att.Session.Interrupt(); err != nil {
		t.Fatal(err)
	}
	ev := waitRunEvent(t, events, run, driver.EventCompleted)
	if ev.Result.FinishReason != driver.FinishCancelled {
		t.Fatalf("finish = %q", ev.Result.FinishReason)
	}
	close(gate)
}

func waitRunEvent(t *testing.T, events <-chan driver.AgentEvent, run driver.RunID, typ driver.EventType) driver.AgentEvent {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case ev := <-events:
			if ev.RunID == run && ev.Type == typ {
				return ev
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %s on run %s", typ, run)
		}
	}
}

func TestFakeNotInstalled(t *testing.T) {
	d := New(context.Background(), driver.AgentSpec{ExecutablePath: "no-such-binary-xyz"})
	d.SetRequireInstall(true)

	_, err := d.OpenSession(driver.OpenParams{})
	if err == nil {
		t.Fatal("OpenSession should fail when the executable is not installed")
	}
	kind, ok := errs.KindOf(err)
	if !ok || kind != errs.KindNotInstalled {
		t.Fatalf("expected not_installed at OpenSession, got %v", err)
	}
}

func TestFakeProbeAndListSessions(t *testing.T) {
	d := New(context.Background(), driver.AgentSpec{})
	pr, err := d.Probe()
	if err != nil || pr != driver.AuthAuthed {
		t.Fatalf("unexpected probe: %+v err=%v", pr, err)
	}
	d.SetStoredSessions([]driver.StoredSessionMeta{
		{SessionID: "old-1", Title: "t", Cwd: "/repo/a"},
		{SessionID: "old-2", Title: "t", Cwd: "/repo/b"},
	})
	metas, err := d.ListSessions(driver.ListSessionsParams{Cwd: "/repo/a"})
	if err != nil || len(metas) != 1 || metas[0].SessionID != "old-1" {
		t.Fatalf("unexpected ListSessions: %+v err=%v", metas, err)
	}
}

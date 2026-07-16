// Package e2e holds unio's end-to-end tests: full-lifecycle scenarios that drive
// a real Driver through open → start → send → consume the event stream →
// cancel → resume (in a fresh session) → close, asserting the SDK does something
// genuinely useful rather than merely compiling.
//
// The scenario body is driver-agnostic. It is parameterised by a Harness so the
// same assertions run against:
//
//   - the in-memory fake driver (default build; CI-runnable, no external CLI);
//   - a real coding-agent CLI (build tag `e2e_real`) — proves the SDK truly
//     drives a live agent. Those are opt-in/manual because they require the CLI.
package tests

import (
	"context"
	"testing"
	"time"

	"github.com/Fullstop000/unio/driver"
)

// Harness supplies everything a lifecycle scenario needs to run against one
// concrete driver, so the fake and real backends share identical assertions.
type Harness struct {
	// Name labels the harness in test output.
	Name string
	// NewDriver returns the driver-under-test.
	NewDriver func(t *testing.T, ctx context.Context, spec driver.AgentSpec) driver.Driver
	// Spec is injected when the concrete driver is constructed.
	Spec driver.AgentSpec
	// FirstPrompt / SecondPrompt are the prompts sent across the two turns.
	FirstPrompt  string
	SecondPrompt string
	// ResponseOption is the advertised option supplied in BlockedScenario.
	ResponseOption string
	// Timeout bounds each wait for a terminal event.
	Timeout time.Duration
}

// collectUntil drains events until a terminal (Completed/Failed) for runID is
// seen or the timeout fires, returning all events observed.
func collectUntil(t *testing.T, ch <-chan driver.AgentEvent, runID driver.RunID, timeout time.Duration) []driver.AgentEvent {
	t.Helper()
	var out []driver.AgentEvent
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
			if (ev.Type == driver.EventCompleted || ev.Type == driver.EventFailed) && ev.RunID == runID {
				return out
			}
		case <-deadline:
			t.Fatalf("[%s] timed out waiting for terminal event of run %s", t.Name(), runID)
			return out
		}
	}
}

// RunLifecycle exercises the full stateful lifecycle end to end against h.
func RunLifecycle(t *testing.T, h Harness) {
	if h.Timeout == 0 {
		h.Timeout = 10 * time.Second
	}
	ctx := context.Background()
	d := h.NewDriver(t, ctx, h.Spec)

	// --- open + subscribe before run (no early events missed) ---
	att, err := d.OpenSession(driver.OpenParams{Cwd: h.Spec.Cwd})
	if err != nil {
		t.Fatalf("[%s] open: %v", h.Name, err)
	}
	events := att.Events.Subscribe()

	// --- run brings the session online and attaches a runtime session id ---
	if err := att.Session.Start(); err != nil {
		t.Fatalf("[%s] run: %v", h.Name, err)
	}
	firstID := att.Session.SessionID()
	if firstID == "" {
		t.Fatalf("[%s] Start should attach a runtime session id", h.Name)
	}

	// --- first prompt: consume the stream through to Completed ---
	runID, err := att.Session.Send(driver.UserMessage{Text: h.FirstPrompt})
	if err != nil {
		t.Fatalf("[%s] prompt: %v", h.Name, err)
	}
	evs := collectUntil(t, events, runID, h.Timeout)
	assertProducedOutputAndCompleted(t, h.Name, evs, runID)

	// --- close the first session ---
	if err := att.Session.Close(); err != nil {
		t.Fatalf("[%s] close: %v", h.Name, err)
	}

	// --- resume: open a fresh session with the prior runtime id ---
	att2, err := d.OpenSession(driver.OpenParams{ResumeSessionID: firstID, Cwd: h.Spec.Cwd})
	if err != nil {
		t.Fatalf("[%s] reopen(resume): %v", h.Name, err)
	}
	events2 := att2.Events.Subscribe()
	if err := att2.Session.Start(); err != nil {
		t.Fatalf("[%s] run(resume): %v", h.Name, err)
	}
	resumedID := att2.Session.SessionID()
	if resumedID != firstID {
		t.Fatalf("[%s] resume should reattach the prior session id: got %q want %q", h.Name, resumedID, firstID)
	}

	// --- second prompt on the resumed session ---
	runID2, err := att2.Session.Send(driver.UserMessage{Text: h.SecondPrompt})
	if err != nil {
		t.Fatalf("[%s] prompt(resume): %v", h.Name, err)
	}
	evs2 := collectUntil(t, events2, runID2, h.Timeout)
	assertProducedOutputAndCompleted(t, h.Name, evs2, runID2)

	if err := att2.Session.Close(); err != nil {
		t.Fatalf("[%s] final close: %v", h.Name, err)
	}
}

func assertProducedOutputAndCompleted(t *testing.T, name string, evs []driver.AgentEvent, runID driver.RunID) {
	t.Helper()
	var sawOutput, sawCompleted bool
	for _, ev := range evs {
		switch ev.Type {
		case driver.EventOutput:
			if ev.RunID == runID {
				sawOutput = true
			}
		case driver.EventCompleted:
			if ev.RunID == runID {
				sawCompleted = true
			}
		case driver.EventFailed:
			if ev.RunID == runID && ev.Err != nil {
				t.Fatalf("[%s] run %s failed: %v", name, runID, ev.Err)
			}
		}
	}
	if !sawOutput {
		t.Fatalf("[%s] run %s produced no output items", name, runID)
	}
	if !sawCompleted {
		t.Fatalf("[%s] run %s never completed", name, runID)
	}
}

// CancelScenario exercises idle interrupt semantics against h.
func CancelScenario(t *testing.T, h Harness) {
	if h.Timeout == 0 {
		h.Timeout = 10 * time.Second
	}
	ctx := context.Background()
	d := h.NewDriver(t, ctx, h.Spec)

	att, err := d.OpenSession(driver.OpenParams{Cwd: h.Spec.Cwd})
	if err != nil {
		t.Fatalf("[%s] open: %v", h.Name, err)
	}
	_ = att.Events.Subscribe()
	if err := att.Session.Start(); err != nil {
		t.Fatalf("[%s] run: %v", h.Name, err)
	}
	defer att.Session.Close()

	if err := att.Session.Interrupt(); err != nil {
		t.Fatalf("[%s] interrupt(idle): %v", h.Name, err)
	}
}

// collectUntilBlockedOrDone drains events until a Blocked, Completed, or Failed
// for runID is seen (or the timeout fires), returning every event observed.
func collectUntilBlockedOrDone(t *testing.T, ch <-chan driver.AgentEvent, runID driver.RunID, timeout time.Duration) []driver.AgentEvent {
	t.Helper()
	var out []driver.AgentEvent
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
			if ev.RunID == runID && (ev.Type == driver.EventBlocked || ev.Type == driver.EventCompleted || ev.Type == driver.EventFailed) {
				return out
			}
		case <-deadline:
			t.Fatalf("[%s] timed out waiting for blocked/terminal event of run %s", t.Name(), runID)
			return out
		}
	}
}

// BlockedScenario drives a turn that blocks awaiting external input, supplies it
// via Respond, and asserts the resumed turn runs to completion. It is only
// meaningful for drivers that can be made to block deterministically (the fake);
// real agents cannot be reliably scripted to request approval.
func BlockedScenario(t *testing.T, h Harness) {
	if h.Timeout == 0 {
		h.Timeout = 10 * time.Second
	}
	if h.ResponseOption == "" {
		t.Fatalf("[%s] BlockedScenario requires Harness.ResponseOption", h.Name)
	}
	ctx := context.Background()
	d := h.NewDriver(t, ctx, h.Spec)

	att, err := d.OpenSession(driver.OpenParams{Cwd: h.Spec.Cwd})
	if err != nil {
		t.Fatalf("[%s] open: %v", h.Name, err)
	}
	events := att.Events.Subscribe()
	if err := att.Session.Start(); err != nil {
		t.Fatalf("[%s] run: %v", h.Name, err)
	}
	defer att.Session.Close()

	runID, err := att.Session.Send(driver.UserMessage{Text: h.FirstPrompt})
	if err != nil {
		t.Fatalf("[%s] prompt: %v", h.Name, err)
	}
	evs := collectUntilBlockedOrDone(t, events, runID, h.Timeout)
	var blocked *driver.AgentEvent
	for i := range evs {
		if evs[i].Type == driver.EventBlocked && evs[i].RunID == runID {
			blocked = &evs[i]
		}
	}
	if blocked == nil {
		t.Fatalf("[%s] first turn never blocked; events=%+v", h.Name, evs)
	}
	if blocked.Blocked == nil {
		t.Fatalf("[%s] blocked event carried no reason", h.Name)
	}
	if state := att.Session.ProcessState().Phase; state != driver.PhaseBlocked {
		t.Fatalf("[%s] phase after block = %q; want blocked", h.Name, state)
	}

	resumeRun, err := att.Session.Respond(driver.OptionSelection{Value: h.ResponseOption})
	if err != nil {
		t.Fatalf("[%s] respond: %v", h.Name, err)
	}
	resumeEvs := collectUntil(t, events, resumeRun, h.Timeout)
	assertProducedOutputAndCompleted(t, h.Name, resumeEvs, resumeRun)
}

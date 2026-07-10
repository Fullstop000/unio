package unio

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Fullstop000/unio/driver"
	"github.com/Fullstop000/unio/driver/fake"
	"github.com/Fullstop000/unio/errs"
)

func newAgentWithDriver(t *testing.T, fd *fake.Driver) *Agent {
	t.Helper()
	prev := driverOverride
	driverOverride = func(AgentKind) (driver.ProtocolDriver, bool) { return fd, true }
	t.Cleanup(func() { driverOverride = prev })
	agent, err := New(Claude)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	return agent
}

func newFakeAgent(t *testing.T) *Agent {
	t.Helper()
	return newAgentWithDriver(t, fake.New())
}

func TestNewSessionStartsIdleWithoutRuntimeID(t *testing.T) {
	agent := newFakeAgent(t)
	session, err := agent.NewSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if session.ID() != "" || session.State() != Idle {
		t.Fatalf("new session: id=%q state=%q", session.ID(), session.State())
	}
}

func TestRunSetsRuntimeIDAndReturnsToIdle(t *testing.T) {
	agent := newFakeAgent(t)
	session, _ := agent.NewSession(context.Background())
	result, err := session.Run(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "echo: hello" || session.ID() == "" || session.State() != Idle {
		t.Fatalf("result=%+v id=%q state=%q", result, session.ID(), session.State())
	}
}

func TestStreamSubmissionErrorIsDirect(t *testing.T) {
	agent := newFakeAgent(t)
	session, _ := agent.NewSession(context.Background())
	gate := make(chan struct{})
	agent.driver.(*fake.Driver).ScriptSession(session.key, fake.Script{Wait: gate})
	stream, err := session.Stream(context.Background(), "one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Stream(context.Background(), "two"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("second Stream error = %v", err)
	}
	if err := session.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, _ = stream.Result()
	close(gate)
}

func TestNewReportsUnavailableAgent(t *testing.T) {
	fd := fake.New()
	fd.SetProbe(driver.RuntimeProbe{Auth: driver.AuthNotInstalled, Transport: driver.TransportFake}, nil)
	prev := driverOverride
	driverOverride = func(AgentKind) (driver.ProtocolDriver, bool) { return fd, true }
	t.Cleanup(func() { driverOverride = prev })
	_, err := New(Claude)
	if kind, ok := errs.KindOf(err); !ok || kind != errs.KindNotInstalled {
		t.Fatalf("error = %v; want not_installed", err)
	}
}

func TestListAndGetSessionMaintainsIdentity(t *testing.T) {
	fd := fake.New()
	fd.SetStoredSessions([]driver.StoredSessionMeta{{SessionID: "stored-1", Title: "auth", Cwd: "/repo"}})
	agent := newAgentWithDriver(t, fd)
	infos, err := agent.ListSessions(context.Background())
	if err != nil || len(infos) != 1 || infos[0].ID != "stored-1" {
		t.Fatalf("infos=%+v err=%v", infos, err)
	}
	first, err := agent.GetSession(context.Background(), "stored-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := agent.GetSession(context.Background(), "stored-1")
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first.ID() != "stored-1" || first.State() != Idle {
		t.Fatal("GetSession must return the maintained idle handle")
	}
	result, err := first.Run(context.Background(), "continue")
	if err != nil || result.Text == "" || first.ID() != "stored-1" {
		t.Fatalf("result=%+v id=%q err=%v", result, first.ID(), err)
	}
}

func TestGetSessionRejectsUnknownID(t *testing.T) {
	agent := newFakeAgent(t)
	_, err := agent.GetSession(context.Background(), "missing")
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("error = %v; want session_not_found", err)
	}
}

func TestBlockedContinueReturnsToIdle(t *testing.T) {
	fd := fake.New()
	agent := newAgentWithDriver(t, fd)
	session, _ := agent.NewSession(context.Background())
	fd.ScriptSession(session.key,
		fake.Script{Blocked: &driver.BlockedReason{
			Kind:    driver.BlockedToolApproval,
			Options: []driver.BlockOption{{Value: "allow_once", Label: "Allow once"}},
		}},
		fake.Script{Items: []driver.AgentEventItem{{Kind: driver.ItemText, Text: "continued"}}},
	)
	blocked, err := session.Run(context.Background(), "needs approval")
	if err != nil || blocked.Blocked == nil || session.State() != Blocked {
		t.Fatalf("result=%+v state=%q err=%v", blocked, session.State(), err)
	}
	continued, err := session.Continue(context.Background(), "allow_once")
	if err != nil || continued.Text != "continued" || session.State() != Idle {
		t.Fatalf("result=%+v state=%q err=%v", continued, session.State(), err)
	}
}

func TestInterruptReturnsPartialResult(t *testing.T) {
	fd := fake.New()
	agent := newAgentWithDriver(t, fd)
	session, _ := agent.NewSession(context.Background())
	gate := make(chan struct{})
	fd.ScriptSession(session.key, fake.Script{
		Items: []driver.AgentEventItem{{Kind: driver.ItemText, Text: "partial"}},
		Wait:  gate,
	})
	stream, err := session.Stream(context.Background(), "long")
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := stream.Result()
	if err != nil || !result.Interrupted || session.State() != Idle {
		t.Fatalf("result=%+v state=%q err=%v", result, session.State(), err)
	}
	close(gate)
}

func TestContextCancellationWaitsForInterruptConfirmation(t *testing.T) {
	turnGate := make(chan struct{})
	interruptGate := make(chan struct{})
	fd := fake.New()
	agent := newAgentWithDriver(t, fd)
	session, _ := agent.NewSession(context.Background())
	fd.ScriptSession(session.key, fake.Script{Wait: turnGate, InterruptWait: interruptGate})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := session.Run(ctx, "long")
		done <- err
	}()
	waitDriverPhase(t, session, driver.PhasePromptInFlight)
	cancel()
	time.Sleep(20 * time.Millisecond)
	if session.State() != Running {
		t.Fatalf("state changed before interrupt confirmation: %q", session.State())
	}
	close(interruptGate)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("run error = %v; want context.Canceled", err)
	}
	if session.State() != Idle {
		t.Fatalf("state after confirmation = %q", session.State())
	}
	close(turnGate)
}

func waitState(t *testing.T, session *Session, want SessionState) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if session.State() == want {
			return
		}
	}
	t.Fatalf("state = %q; want %q", session.State(), want)
}

func waitDriverPhase(t *testing.T, session *Session, want driver.Phase) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		session.mu.Lock()
		inner := session.inner
		session.mu.Unlock()
		if inner != nil && inner.ProcessState().Phase == want {
			return
		}
	}
	t.Fatalf("driver did not reach phase %q", want)
}

type blockingOpenDriver struct {
	driver.ProtocolDriver
	started chan struct{}
	release chan struct{}
}

func (d *blockingOpenDriver) OpenSession(ctx context.Context, key driver.SessionKey, spec driver.AgentSpec, params driver.OpenParams) (*driver.SessionAttachment, error) {
	close(d.started)
	select {
	case <-d.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return d.ProtocolDriver.OpenSession(ctx, key, spec, params)
}

func TestAgentCloseCannotRaceInANewAttachment(t *testing.T) {
	fd := fake.New()
	blocking := &blockingOpenDriver{ProtocolDriver: fd, started: make(chan struct{}), release: make(chan struct{})}
	prev := driverOverride
	driverOverride = func(AgentKind) (driver.ProtocolDriver, bool) { return blocking, true }
	t.Cleanup(func() { driverOverride = prev })
	agent, err := New(Claude)
	if err != nil {
		t.Fatal(err)
	}
	session, _ := agent.NewSession(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := session.Run(context.Background(), "hello")
		done <- err
	}()
	<-blocking.started
	if err := agent.Close(); err != nil {
		t.Fatal(err)
	}
	close(blocking.release)
	if err := <-done; !errors.Is(err, ErrInvalidState) {
		t.Fatalf("run error = %v; want invalid_state", err)
	}
	session.mu.Lock()
	inner := session.inner
	session.mu.Unlock()
	if inner != nil {
		t.Fatal("closed agent retained an attachment opened by a racing Run")
	}
}

func TestTransportClosedTurnReturnsError(t *testing.T) {
	fd := fake.New()
	agent := newAgentWithDriver(t, fd)
	session, _ := agent.NewSession(context.Background())
	fd.ScriptSession(session.key, fake.Script{Result: driver.RunResult{FinishReason: driver.FinishTransportClosed}})
	_, err := session.Run(context.Background(), "hello")
	if kind, ok := errs.KindOf(err); !ok || kind != errs.KindTransport {
		t.Fatalf("error = %v; want transport", err)
	}
}

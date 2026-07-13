package unio

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Fullstop000/unio/driver"
	"github.com/Fullstop000/unio/driver/fake"
	"github.com/Fullstop000/unio/errs"
)

type statisticsDriver struct {
	*fake.Driver
	raw   driver.RawSessionData
	usage driver.TokenUsage
}

func (d *statisticsDriver) NewSessionData(ctx context.Context, _ driver.AgentSpec, _ driver.SessionID) driver.SessionData {
	return driver.NewSessionData(
		ctx,
		func(context.Context) (driver.RawSessionData, error) {
			return d.raw, nil
		},
		func(_ context.Context, raw driver.RawSessionData) (driver.TokenUsage, error) {
			if raw.Format != d.raw.Format || string(raw.Data) != string(d.raw.Data) {
				return driver.TokenUsage{}, driver.NewProtocolError("statistics parser did not receive raw session data")
			}
			return d.usage, nil
		},
	)
}

func newAgentWithDriver(t *testing.T, fd *fake.Driver) *Agent {
	return newAgentWithDriverOptions(t, fd)
}

func newAgentWithDriverOptions(t *testing.T, fd *fake.Driver, opts ...Option) *Agent {
	return newAgentWithProtocolDriverOptions(t, fd, opts...)
}

func newAgentWithProtocolDriverOptions(t *testing.T, d driver.ProtocolDriver, opts ...Option) *Agent {
	t.Helper()
	prev := driverOverride
	driverOverride = func(AgentKind) (driver.ProtocolDriver, bool) { return d, true }
	t.Cleanup(func() { driverOverride = prev })
	agent, err := New(Claude, opts...)
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

func TestACPAgentKindsUseTheSharedDriver(t *testing.T) {
	for _, kind := range []AgentKind{Kimi, TraeX, OpenCode} {
		d, err := driverFor(kind)
		if err != nil {
			t.Fatalf("driverFor(%q): %v", kind, err)
		}
		if d.Transport() != driver.TransportACPNative {
			t.Fatalf("driverFor(%q) transport = %q", kind, d.Transport())
		}
	}
}

func TestListAndGetSessionMaintainsIdentity(t *testing.T) {
	fd := fake.New()
	fd.SetStoredSessions([]driver.StoredSessionMeta{{SessionID: "stored-1", Title: "auth", Cwd: "/repo"}})
	agent := newAgentWithProtocolDriverOptions(t, fd, WithCwd("/repo"))
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

func TestListSessionsFiltersByWorkspace(t *testing.T) {
	fd := fake.New()
	fd.SetStoredSessions([]driver.StoredSessionMeta{
		{SessionID: "repo-a", Cwd: "/repo/a"},
		{SessionID: "repo-b", Cwd: "/repo/b"},
	})
	agent := newAgentWithDriverOptions(t, fd, WithCwd("/repo/a"))

	current, err := agent.ListSessions(context.Background())
	if err != nil || len(current) != 1 || current[0].ID != "repo-a" {
		t.Fatalf("current=%+v err=%v", current, err)
	}
	other, err := agent.ListSessions(context.Background(), SessionsIn("/repo/b"))
	if err != nil || len(other) != 1 || other[0].ID != "repo-b" {
		t.Fatalf("other=%+v err=%v", other, err)
	}
	all, err := agent.ListSessions(context.Background(), AllSessions())
	if err != nil || len(all) != 2 {
		t.Fatalf("all=%+v err=%v", all, err)
	}
}

func TestListSessionsFiltersMaintainedHandles(t *testing.T) {
	agent := newAgentWithDriverOptions(t, fake.New(), WithCwd("/repo/a"))
	session, err := agent.NewSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Run(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}

	current, err := agent.ListSessions(context.Background())
	if err != nil || len(current) != 1 || current[0].Cwd != "/repo/a" {
		t.Fatalf("current=%+v err=%v", current, err)
	}
	other, err := agent.ListSessions(context.Background(), SessionsIn("/repo/b"))
	if err != nil || len(other) != 0 {
		t.Fatalf("other=%+v err=%v", other, err)
	}
}

func TestListSessionsEmptySessionsInKeepsAgentWorkspace(t *testing.T) {
	fd := fake.New()
	fd.SetStoredSessions([]driver.StoredSessionMeta{
		{SessionID: "repo-a", Cwd: "/repo/a"},
		{SessionID: "repo-b", Cwd: "/repo/b"},
	})
	agent := newAgentWithDriverOptions(t, fd, WithCwd("/repo/a"))
	got, err := agent.ListSessions(context.Background(), SessionsIn(""))
	if err != nil || len(got) != 1 || got[0].ID != "repo-a" {
		t.Fatalf("sessions=%+v err=%v", got, err)
	}
}

func TestListSessionsMaxSessions(t *testing.T) {
	fd := fake.New()
	fd.SetStoredSessions([]driver.StoredSessionMeta{
		{SessionID: "one", Cwd: "/repo"},
		{SessionID: "two", Cwd: "/repo"},
		{SessionID: "three", Cwd: "/repo"},
	})
	agent := newAgentWithProtocolDriverOptions(t, fd, WithCwd("/repo"))

	got, err := agent.ListSessions(context.Background(), MaxSessions(2))
	if err != nil || len(got) != 2 || got[0].ID != "one" || got[1].ID != "two" {
		t.Fatalf("sessions=%+v err=%v", got, err)
	}

	all, err := agent.ListSessions(context.Background(), MaxSessions(0))
	if err != nil || len(all) != 3 {
		t.Fatalf("sessions=%+v err=%v", all, err)
	}

	all, err = agent.ListSessions(context.Background(), MaxSessions(3))
	if err != nil || len(all) != 3 {
		t.Fatalf("sessions=%+v err=%v", all, err)
	}
}

func TestGetSessionRejectsUnknownID(t *testing.T) {
	agent := newFakeAgent(t)
	_, err := agent.GetSession(context.Background(), "missing")
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("error = %v; want session_not_found", err)
	}
}

func TestSessionTokenStatistics(t *testing.T) {
	fd := &statisticsDriver{Driver: fake.New(), raw: driver.RawSessionData{
		Format: driver.SessionDataJSONL, Data: []byte("raw data"),
	}, usage: driver.TokenUsage{
		InputTokens: 100, OutputTokens: 20, CacheReadTokens: 60, CacheWriteTokens: 5,
	}}
	fd.SetStoredSessions([]driver.StoredSessionMeta{{SessionID: "stored", Cwd: "/repo"}})
	agent := newAgentWithProtocolDriverOptions(t, fd, WithCwd("/repo"))
	session, err := agent.GetSession(context.Background(), "stored")
	if err != nil {
		t.Fatal(err)
	}
	got, err := session.TokenStatistics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.InputTokens != 100 || got.OutputTokens != 20 || got.CacheReadTokens != 60 || got.CacheWriteTokens != 5 {
		t.Fatalf("statistics = %+v", got)
	}
	raw, err := session.Raw(context.Background())
	if err != nil || raw.Format != SessionDataJSONL || string(raw.Data) != "raw data" {
		t.Fatalf("raw = %+v, error = %v", raw, err)
	}
}

func TestSessionTokenStatisticsUnsupported(t *testing.T) {
	fd := fake.New()
	fd.SetStoredSessions([]driver.StoredSessionMeta{{SessionID: "stored", Cwd: "/repo"}})
	agent := newAgentWithDriverOptions(t, fd, WithCwd("/repo"))
	session, err := agent.GetSession(context.Background(), "stored")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.TokenStatistics(context.Background()); !errors.Is(err, driver.NewUnsupportedError("")) {
		t.Fatalf("error = %v; want unsupported", err)
	}
	if _, err := session.Raw(context.Background()); !errors.Is(err, driver.NewUnsupportedError("")) {
		t.Fatalf("raw error = %v; want unsupported", err)
	}
}

func TestSessionRawDataRequiresRuntimeID(t *testing.T) {
	agent := newAgentWithProtocolDriverOptions(t, &statisticsDriver{Driver: fake.New()})
	session, err := agent.NewSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Raw(context.Background()); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("raw error = %v; want invalid_state", err)
	}
	if _, err := session.TokenStatistics(context.Background()); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("statistics error = %v; want invalid_state", err)
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

type blockingPromptDriver struct {
	driver.ProtocolDriver
	started chan struct{}
	release chan struct{}
}

type recordingSpecDriver struct {
	driver.ProtocolDriver
	mu   sync.Mutex
	spec driver.AgentSpec
}

type staleAttachmentDriver struct {
	driver.ProtocolDriver
	mu              sync.Mutex
	opens           int
	failFirstPrompt bool
	latest          *staleAttachmentSession
}

type staleAttachmentSession struct {
	driver.Session
	mu         sync.Mutex
	stale      bool
	failPrompt bool
}

func (d *staleAttachmentDriver) OpenSession(ctx context.Context, key driver.SessionKey, spec driver.AgentSpec, params driver.OpenParams) (*driver.SessionAttachment, error) {
	att, err := d.ProtocolDriver.OpenSession(ctx, key, spec, params)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	failPrompt := d.failFirstPrompt && d.opens == 0
	d.opens++
	d.mu.Unlock()
	wrapped := &staleAttachmentSession{Session: att.Session, failPrompt: failPrompt}
	att.Session = wrapped
	d.mu.Lock()
	d.latest = wrapped
	d.mu.Unlock()
	return att, nil
}

func (s *staleAttachmentSession) ProcessState() driver.ProcessState {
	s.mu.Lock()
	stale := s.stale
	s.mu.Unlock()
	if stale {
		return driver.ProcessState{Phase: driver.PhaseClosed, SessionID: s.SessionID()}
	}
	return s.Session.ProcessState()
}

func (s *staleAttachmentSession) markStale() {
	s.mu.Lock()
	s.stale = true
	s.mu.Unlock()
}

func (s *staleAttachmentSession) Prompt(ctx context.Context, req driver.PromptReq) (driver.RunID, error) {
	s.mu.Lock()
	fail := s.failPrompt
	s.failPrompt = false
	if fail {
		s.stale = true
	}
	s.mu.Unlock()
	if fail {
		return driver.NewRunID(), driver.NewTransportError("forced closed transport")
	}
	return s.Session.Prompt(ctx, req)
}

func (d *recordingSpecDriver) OpenSession(ctx context.Context, key driver.SessionKey, spec driver.AgentSpec, params driver.OpenParams) (*driver.SessionAttachment, error) {
	d.mu.Lock()
	d.spec = spec
	d.mu.Unlock()
	return d.ProtocolDriver.OpenSession(ctx, key, spec, params)
}

func (d *blockingPromptDriver) OpenSession(ctx context.Context, key driver.SessionKey, spec driver.AgentSpec, params driver.OpenParams) (*driver.SessionAttachment, error) {
	att, err := d.ProtocolDriver.OpenSession(ctx, key, spec, params)
	if err != nil {
		return nil, err
	}
	att.Session = &blockingPromptSession{Session: att.Session, started: d.started, release: d.release}
	return att, nil
}

type blockingPromptSession struct {
	driver.Session
	started chan struct{}
	release chan struct{}
}

func (s *blockingPromptSession) Prompt(ctx context.Context, req driver.PromptReq) (driver.RunID, error) {
	close(s.started)
	select {
	case <-s.release:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return s.Session.Prompt(ctx, req)
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
	closeDone := make(chan error, 1)
	go func() { closeDone <- agent.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Agent.Close returned while OpenSession was in flight: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(blocking.release)
	<-done
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	session.mu.Lock()
	inner := session.inner
	session.mu.Unlock()
	if inner != nil {
		t.Fatal("closed agent retained an attachment opened by a racing Run")
	}
}

func TestAgentCloseWaitsForPromptSubmission(t *testing.T) {
	fd := fake.New()
	blocking := &blockingPromptDriver{ProtocolDriver: fd, started: make(chan struct{}), release: make(chan struct{})}
	prev := driverOverride
	driverOverride = func(AgentKind) (driver.ProtocolDriver, bool) { return blocking, true }
	t.Cleanup(func() { driverOverride = prev })
	agent, err := New(Claude)
	if err != nil {
		t.Fatal(err)
	}
	session, _ := agent.NewSession(context.Background())
	streamDone := make(chan error, 1)
	go func() {
		_, err := session.Stream(context.Background(), "hello")
		streamDone <- err
	}()
	<-blocking.started
	closeDone := make(chan error, 1)
	go func() { closeDone <- agent.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Agent.Close returned during Prompt submission: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(blocking.release)
	if err := <-streamDone; err != nil {
		t.Fatalf("Stream submission: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
}

func TestInterruptWaitsForPromptSubmission(t *testing.T) {
	fd := fake.New()
	blocking := &blockingPromptDriver{ProtocolDriver: fd, started: make(chan struct{}), release: make(chan struct{})}
	prev := driverOverride
	driverOverride = func(AgentKind) (driver.ProtocolDriver, bool) { return blocking, true }
	t.Cleanup(func() { driverOverride = prev })
	agent, err := New(Claude)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	session, _ := agent.NewSession(context.Background())
	streamDone := make(chan *Stream, 1)
	go func() {
		stream, _ := session.Stream(context.Background(), "hello")
		streamDone <- stream
	}()
	<-blocking.started
	interruptDone := make(chan error, 1)
	go func() { interruptDone <- session.Interrupt(context.Background()) }()
	var early error
	returnedEarly := false
	select {
	case err := <-interruptDone:
		early = err
		returnedEarly = true
	case <-time.After(20 * time.Millisecond):
	}
	close(blocking.release)
	stream := <-streamDone
	if returnedEarly {
		t.Fatalf("Interrupt returned before Prompt submission: %v", early)
	}
	if !returnedEarly {
		if err := <-interruptDone; err != nil {
			t.Fatal(err)
		}
	}
	if stream == nil {
		t.Fatal("Stream submission failed")
	}
	_, _ = stream.Result()
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

func TestInterruptedStreamMustFinishBeforeNextTurn(t *testing.T) {
	gate := make(chan struct{})
	fd := fake.New()
	agent := newAgentWithDriver(t, fd)
	session, _ := agent.NewSession(context.Background())
	fd.ScriptSession(session.key, fake.Script{Wait: gate})
	first, err := session.Stream(context.Background(), "long")
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Stream(context.Background(), "too early"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("new turn before interrupted stream terminal = %v", err)
	}
	if _, err := first.Result(); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Run(context.Background(), "now allowed"); err != nil {
		t.Fatal(err)
	}
	close(gate)
}

func TestListSessionsAfterAgentCloseFails(t *testing.T) {
	agent := newFakeAgent(t)
	if err := agent.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.ListSessions(context.Background()); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("ListSessions after Close error = %v", err)
	}
}

func TestSessionRejectsRuntimeIDChange(t *testing.T) {
	agent := newFakeAgent(t)
	session, _ := agent.NewSession(context.Background())
	if err := session.setID("original"); err != nil {
		t.Fatal(err)
	}
	if err := session.setID("replacement"); err == nil {
		t.Fatal("session accepted a different runtime ID")
	}
}

func TestGetSessionUsesPersistedCwdForResume(t *testing.T) {
	fd := fake.New()
	fd.SetStoredSessions([]driver.StoredSessionMeta{{SessionID: "stored", Cwd: "/other/repo"}})
	recording := &recordingSpecDriver{ProtocolDriver: fd}
	prev := driverOverride
	driverOverride = func(AgentKind) (driver.ProtocolDriver, bool) { return recording, true }
	t.Cleanup(func() { driverOverride = prev })
	agent, err := New(Claude, WithCwd("/default/repo"))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	session, err := agent.GetSession(context.Background(), "stored")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Run(context.Background(), "continue"); err != nil {
		t.Fatal(err)
	}
	recording.mu.Lock()
	cwd := recording.spec.Cwd
	recording.mu.Unlock()
	if cwd != "/other/repo" {
		t.Fatalf("resume cwd = %q; want persisted cwd", cwd)
	}
}

func TestRunReattachesAClosedDriverSession(t *testing.T) {
	fd := fake.New()
	staleDriver := &staleAttachmentDriver{ProtocolDriver: fd}
	prev := driverOverride
	driverOverride = func(AgentKind) (driver.ProtocolDriver, bool) { return staleDriver, true }
	t.Cleanup(func() { driverOverride = prev })
	agent, err := New(Codex)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	session, _ := agent.NewSession(context.Background())
	if _, err := session.Run(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	staleDriver.mu.Lock()
	first := staleDriver.latest
	staleDriver.mu.Unlock()
	first.markStale()
	if _, err := session.Run(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	staleDriver.mu.Lock()
	opens := staleDriver.opens
	staleDriver.mu.Unlock()
	if opens != 2 {
		t.Fatalf("OpenSession calls = %d; want 2 after stale attachment", opens)
	}
}

func TestContinueDropsAClosedBlockedAttachment(t *testing.T) {
	fd := fake.New()
	staleDriver := &staleAttachmentDriver{ProtocolDriver: fd}
	prev := driverOverride
	driverOverride = func(AgentKind) (driver.ProtocolDriver, bool) { return staleDriver, true }
	t.Cleanup(func() { driverOverride = prev })
	agent, err := New(Codex)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	session, _ := agent.NewSession(context.Background())
	fd.ScriptSession(session.key, fake.Script{Blocked: &driver.BlockedReason{Kind: driver.BlockedToolApproval}})
	if result, err := session.Run(context.Background(), "block"); err != nil || result.Blocked == nil {
		t.Fatalf("blocked result=%+v err=%v", result, err)
	}
	staleDriver.mu.Lock()
	first := staleDriver.latest
	staleDriver.mu.Unlock()
	first.markStale()
	if _, err := session.Continue(context.Background(), "allow_once"); err == nil {
		t.Fatal("Continue accepted a closed blocked attachment")
	}
	if session.State() != Idle {
		t.Fatalf("state after stale Continue = %q; want idle", session.State())
	}
	if _, err := session.Run(context.Background(), "recover"); err != nil {
		t.Fatal(err)
	}
}

func TestPromptErrorDropsAClosedAttachment(t *testing.T) {
	fd := fake.New()
	staleDriver := &staleAttachmentDriver{ProtocolDriver: fd, failFirstPrompt: true}
	prev := driverOverride
	driverOverride = func(AgentKind) (driver.ProtocolDriver, bool) { return staleDriver, true }
	t.Cleanup(func() { driverOverride = prev })
	agent, err := New(Codex)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	session, _ := agent.NewSession(context.Background())
	if _, err := session.Run(context.Background(), "fails"); err == nil {
		t.Fatal("first Run unexpectedly succeeded")
	}
	if _, err := session.Run(context.Background(), "recovers"); err != nil {
		t.Fatal(err)
	}
	staleDriver.mu.Lock()
	opens := staleDriver.opens
	staleDriver.mu.Unlock()
	if opens != 2 {
		t.Fatalf("OpenSession calls = %d; want closed attachment replaced", opens)
	}
}

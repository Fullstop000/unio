package unio

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Fullstop000/unio/driver"
	acpdrv "github.com/Fullstop000/unio/driver/acp"
	"github.com/Fullstop000/unio/driver/fake"
	"github.com/Fullstop000/unio/errs"
)

type statisticsDriver struct {
	*fake.Driver
	raw   driver.RawSessionData
	usage driver.TokenUsage
}

func (d *statisticsDriver) OpenSession(params driver.OpenParams) (*driver.SessionAttachment, error) {
	attachment, err := d.Driver.OpenSession(params)
	if err != nil {
		return nil, err
	}
	attachment.Session = &statisticsSession{Session: attachment.Session, raw: d.raw, usage: d.usage}
	return attachment, nil
}

type statisticsSession struct {
	driver.Session
	raw   driver.RawSessionData
	usage driver.TokenUsage
}

type nonActiveStateDriver struct {
	*fake.Driver
	runCalls atomic.Uint64
}

func (d *nonActiveStateDriver) OpenSession(params driver.OpenParams) (*driver.SessionAttachment, error) {
	attachment, err := d.Driver.OpenSession(params)
	if err != nil {
		return nil, err
	}
	attachment.Session = &nonActiveStateSession{Session: attachment.Session, runCalls: &d.runCalls}
	return attachment, nil
}

type nonActiveStateSession struct {
	driver.Session
	runCalls *atomic.Uint64
}

func (s *nonActiveStateSession) Start() error {
	s.runCalls.Add(1)
	return s.Session.Start()
}

func (s *nonActiveStateSession) ProcessState() driver.ProcessState {
	state := s.Session.ProcessState()
	if state.Phase == driver.PhaseActive {
		state.Phase = driver.PhasePromptInFlight
	}
	return state
}

func (s *statisticsSession) Raw() (driver.RawSessionData, error) {
	return s.raw, nil
}

func (s *statisticsSession) TokenStatistics() (driver.TokenUsage, error) {
	raw, err := s.Raw()
	if err != nil {
		return driver.TokenUsage{}, err
	}
	if raw.Format != s.raw.Format || string(raw.Data) != string(s.raw.Data) {
		return driver.TokenUsage{}, driver.NewProtocolError("statistics parser did not receive raw session data")
	}
	return s.usage, nil
}

func newAgentWithDriver(t *testing.T, fd *fake.Driver) *Agent {
	return newAgentWithDriverOptions(t, fd)
}

func newAgentWithDriverOptions(t *testing.T, d driver.Driver, opts ...Option) *Agent {
	t.Helper()
	prev := driverOverride
	driverOverride = func(context.Context, AgentKind, driver.AgentSpec) (driver.Driver, bool) { return d, true }
	t.Cleanup(func() { driverOverride = prev })
	agent, err := New(context.Background(), Claude, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	return agent
}

func newFakeAgent(t *testing.T) *Agent {
	t.Helper()
	return newAgentWithDriver(t, fake.New(context.Background(), driver.AgentSpec{}))
}

func TestNewSessionStartsIdleWithoutRuntimeID(t *testing.T) {
	agent := newFakeAgent(t)
	session, err := agent.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if session.ID() != "" || session.State() != Idle {
		t.Fatalf("new session: id=%q state=%q", session.ID(), session.State())
	}
}

func TestRunSetsRuntimeIDAndReturnsToIdle(t *testing.T) {
	agent := newFakeAgent(t)
	session, _ := agent.NewSession()
	result, err := session.Run(Message("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "echo: hello" || session.ID() == "" || session.State() != Idle {
		t.Fatalf("result=%+v id=%q state=%q", result, session.ID(), session.State())
	}
}

func TestRunDoesNotRestartAnAttachedSession(t *testing.T) {
	fd := &nonActiveStateDriver{Driver: fake.New(context.Background(), driver.AgentSpec{})}
	agent := newAgentWithDriverOptions(t, fd)
	session, err := agent.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	for _, prompt := range []string{"one", "two"} {
		if _, err := session.Run(Message(prompt)); err != nil {
			t.Fatalf("Run(%q): %v", prompt, err)
		}
	}
	if got := fd.runCalls.Load(); got != 1 {
		t.Fatalf("handle Run calls = %d; want 1", got)
	}
}

func TestStreamSubmissionErrorIsDirect(t *testing.T) {
	agent := newFakeAgent(t)
	session, _ := agent.NewSession()
	gate := make(chan struct{})
	agent.driver.(*fake.Driver).ScriptNextSession(fake.Script{Wait: gate})
	stream, err := session.Stream(Message("one"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Stream(Message("two")); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("second Stream error = %v", err)
	}
	if err := session.Interrupt(); err != nil {
		t.Fatal(err)
	}
	_, _ = stream.Result()
	close(gate)
}

func TestNewReportsUnavailableAgent(t *testing.T) {
	fd := fake.New(context.Background(), driver.AgentSpec{})
	fd.SetProbe(driver.AuthNotInstalled, nil)
	prev := driverOverride
	driverOverride = func(context.Context, AgentKind, driver.AgentSpec) (driver.Driver, bool) { return fd, true }
	t.Cleanup(func() { driverOverride = prev })
	_, err := New(context.Background(), Claude)
	if kind, ok := errs.KindOf(err); !ok || kind != errs.KindNotInstalled {
		t.Fatalf("error = %v; want not_installed", err)
	}
}

func TestACPAgentKindsUseTheSharedDriver(t *testing.T) {
	for _, kind := range []AgentKind{Kimi, TraeX, OpenCode} {
		d, err := driverFor(context.Background(), kind, driver.AgentSpec{})
		if err != nil {
			t.Fatalf("driverFor(%q): %v", kind, err)
		}
		if _, ok := d.(*acpdrv.Driver); !ok {
			t.Fatalf("driverFor(%q) = %T; want *acp.Driver", kind, d)
		}
	}
}

func TestListAndGetSessionMaintainsIdentity(t *testing.T) {
	fd := fake.New(context.Background(), driver.AgentSpec{})
	fd.SetStoredSessions([]driver.StoredSessionMeta{{SessionID: "stored-1", Title: "auth", Cwd: "/repo"}})
	agent := newAgentWithDriverOptions(t, fd, WithCwd("/repo"))
	infos, err := agent.ListSessions()
	if err != nil || len(infos) != 1 || infos[0].ID != "stored-1" {
		t.Fatalf("infos=%+v err=%v", infos, err)
	}
	first, err := agent.GetSession("stored-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := agent.GetSession("stored-1")
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first.ID() != "stored-1" || first.State() != Idle {
		t.Fatal("GetSession must return the maintained idle handle")
	}
	result, err := first.Run(Message("continue"))
	if err != nil || result.Text == "" || first.ID() != "stored-1" {
		t.Fatalf("result=%+v id=%q err=%v", result, first.ID(), err)
	}
}

func TestListSessionsFiltersByWorkspace(t *testing.T) {
	fd := fake.New(context.Background(), driver.AgentSpec{})
	fd.SetStoredSessions([]driver.StoredSessionMeta{
		{SessionID: "repo-a", Cwd: "/repo/a"},
		{SessionID: "repo-b", Cwd: "/repo/b"},
	})
	agent := newAgentWithDriverOptions(t, fd, WithCwd("/repo/a"))

	current, err := agent.ListSessions()
	if err != nil || len(current) != 1 || current[0].ID != "repo-a" {
		t.Fatalf("current=%+v err=%v", current, err)
	}
	other, err := agent.ListSessions(SessionsIn("/repo/b"))
	if err != nil || len(other) != 1 || other[0].ID != "repo-b" {
		t.Fatalf("other=%+v err=%v", other, err)
	}
	all, err := agent.ListSessions(AllSessions())
	if err != nil || len(all) != 2 {
		t.Fatalf("all=%+v err=%v", all, err)
	}
}

func TestListSessionsFiltersMaintainedHandles(t *testing.T) {
	agent := newAgentWithDriverOptions(t, fake.New(context.Background(), driver.AgentSpec{}), WithCwd("/repo/a"))
	session, err := agent.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Run(Message("hello")); err != nil {
		t.Fatal(err)
	}

	current, err := agent.ListSessions()
	if err != nil || len(current) != 1 || current[0].Cwd != "/repo/a" {
		t.Fatalf("current=%+v err=%v", current, err)
	}
	other, err := agent.ListSessions(SessionsIn("/repo/b"))
	if err != nil || len(other) != 0 {
		t.Fatalf("other=%+v err=%v", other, err)
	}
}

func TestListSessionsEmptySessionsInKeepsAgentWorkspace(t *testing.T) {
	fd := fake.New(context.Background(), driver.AgentSpec{})
	fd.SetStoredSessions([]driver.StoredSessionMeta{
		{SessionID: "repo-a", Cwd: "/repo/a"},
		{SessionID: "repo-b", Cwd: "/repo/b"},
	})
	agent := newAgentWithDriverOptions(t, fd, WithCwd("/repo/a"))
	got, err := agent.ListSessions(SessionsIn(""))
	if err != nil || len(got) != 1 || got[0].ID != "repo-a" {
		t.Fatalf("sessions=%+v err=%v", got, err)
	}
}

func TestListSessionsMaxSessions(t *testing.T) {
	fd := fake.New(context.Background(), driver.AgentSpec{})
	fd.SetStoredSessions([]driver.StoredSessionMeta{
		{SessionID: "one", Cwd: "/repo"},
		{SessionID: "two", Cwd: "/repo"},
		{SessionID: "three", Cwd: "/repo"},
	})
	agent := newAgentWithDriverOptions(t, fd, WithCwd("/repo"))

	got, err := agent.ListSessions(MaxSessions(2))
	if err != nil || len(got) != 2 || got[0].ID != "one" || got[1].ID != "two" {
		t.Fatalf("sessions=%+v err=%v", got, err)
	}

	all, err := agent.ListSessions(MaxSessions(0))
	if err != nil || len(all) != 3 {
		t.Fatalf("sessions=%+v err=%v", all, err)
	}

	all, err = agent.ListSessions(MaxSessions(3))
	if err != nil || len(all) != 3 {
		t.Fatalf("sessions=%+v err=%v", all, err)
	}
}

func TestGetSessionRejectsUnknownID(t *testing.T) {
	agent := newFakeAgent(t)
	_, err := agent.GetSession("missing")
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("error = %v; want session_not_found", err)
	}
}

func TestSessionTokenStatistics(t *testing.T) {
	fd := &statisticsDriver{Driver: fake.New(context.Background(), driver.AgentSpec{}), raw: driver.RawSessionData{
		Format: driver.SessionDataJSONL, Data: []byte("raw data"),
	}, usage: driver.TokenUsage{
		InputTokens: 100, OutputTokens: 20, CacheReadTokens: 60, CacheWriteTokens: 5,
	}}
	fd.SetStoredSessions([]driver.StoredSessionMeta{{SessionID: "stored", Cwd: "/repo"}})
	agent := newAgentWithDriverOptions(t, fd, WithCwd("/repo"))
	session, err := agent.GetSession("stored")
	if err != nil {
		t.Fatal(err)
	}
	got, err := session.TokenStatistics()
	if err != nil {
		t.Fatal(err)
	}
	if got.InputTokens != 100 || got.OutputTokens != 20 || got.CacheReadTokens != 60 || got.CacheWriteTokens != 5 {
		t.Fatalf("statistics = %+v", got)
	}
	raw, err := session.Raw()
	if err != nil || raw.Format != SessionDataJSONL || string(raw.Data) != "raw data" {
		t.Fatalf("raw = %+v, error = %v", raw, err)
	}
	session.mu.Lock()
	inner := session.inner
	session.mu.Unlock()
	if inner == nil {
		t.Fatal("reading persisted data did not create a session handle")
	}
	if state := inner.ProcessState(); state.Phase != driver.PhaseIdle {
		t.Fatalf("reading persisted data started runtime: state=%+v", state)
	}
	result, err := session.Run(Message("continue"))
	if err != nil || result.SessionID != "stored" {
		t.Fatalf("run after persisted data read: result=%+v error=%v", result, err)
	}
}

func TestSessionTokenStatisticsUnsupported(t *testing.T) {
	fd := fake.New(context.Background(), driver.AgentSpec{})
	fd.SetStoredSessions([]driver.StoredSessionMeta{{SessionID: "stored", Cwd: "/repo"}})
	agent := newAgentWithDriverOptions(t, fd, WithCwd("/repo"))
	session, err := agent.GetSession("stored")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.TokenStatistics(); !errors.Is(err, driver.NewUnsupportedError("")) {
		t.Fatalf("error = %v; want unsupported", err)
	}
	if _, err := session.Raw(); !errors.Is(err, driver.NewUnsupportedError("")) {
		t.Fatalf("raw error = %v; want unsupported", err)
	}
}

func TestSessionRawDataRequiresRuntimeID(t *testing.T) {
	agent := newAgentWithDriverOptions(t, &statisticsDriver{Driver: fake.New(context.Background(), driver.AgentSpec{})})
	session, err := agent.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Raw(); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("raw error = %v; want invalid_state", err)
	}
	if _, err := session.TokenStatistics(); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("statistics error = %v; want invalid_state", err)
	}
}

func TestBlockedRunResponseReturnsToIdle(t *testing.T) {
	fd := fake.New(context.Background(), driver.AgentSpec{})
	agent := newAgentWithDriver(t, fd)
	session, _ := agent.NewSession()
	fd.ScriptNextSession(
		fake.Script{Blocked: &driver.BlockedReason{
			Kind:    driver.BlockedToolApproval,
			Options: []driver.BlockOption{{Value: "allow_once", Label: "Allow once"}},
		}},
		fake.Script{Items: []driver.AgentEventItem{{Kind: driver.ItemText, Text: "continued"}}},
	)
	blocked, err := session.Run(Message("needs approval"))
	if err != nil || blocked.Blocked == nil || session.State() != Blocked {
		t.Fatalf("result=%+v state=%q err=%v", blocked, session.State(), err)
	}
	stream, err := session.Stream(SelectOption("allow_once"))
	if err != nil {
		t.Fatal(err)
	}
	var streamedText string
	for stream.Next() {
		if event := stream.Event(); event.Kind == KindText {
			streamedText += event.Text
		}
	}
	continued, err := stream.Result()
	if err != nil || continued.Text != "continued" || session.State() != Idle {
		t.Fatalf("result=%+v state=%q err=%v", continued, session.State(), err)
	}
	if streamedText != continued.Text {
		t.Fatalf("streamed text %q != result text %q", streamedText, continued.Text)
	}
}

func TestBlockedUserMessageReturnsToIdle(t *testing.T) {
	fd := fake.New(context.Background(), driver.AgentSpec{})
	agent := newAgentWithDriver(t, fd)
	session, _ := agent.NewSession()
	fd.ScriptNextSession(
		fake.Script{Blocked: &driver.BlockedReason{
			Kind:    driver.BlockedUserInput,
			Message: "Which database?",
		}},
		fake.Script{Items: []driver.AgentEventItem{{Kind: driver.ItemText, Text: "using PostgreSQL"}}},
	)
	blocked, err := session.Run(Message("change the database"))
	if err != nil || blocked.Blocked == nil || session.State() != Blocked {
		t.Fatalf("result=%+v state=%q err=%v", blocked, session.State(), err)
	}
	continued, err := session.Run(Message("PostgreSQL"))
	if err != nil || continued.Text != "using PostgreSQL" || session.State() != Idle {
		t.Fatalf("result=%+v state=%q err=%v", continued, session.State(), err)
	}
}

// A failed blocked response whose transport is still alive must leave the session
// Blocked so the caller can retry, not silently drop to Idle.
func TestBlockedResponseErrorStaysBlocked(t *testing.T) {
	fd := fake.New(context.Background(), driver.AgentSpec{})
	agent := newAgentWithDriver(t, fd)
	session, _ := agent.NewSession()
	fd.ScriptNextSession(
		fake.Script{Blocked: &driver.BlockedReason{
			Kind:    driver.BlockedToolApproval,
			Options: []driver.BlockOption{{Value: "allow_once", Label: "Allow once"}},
		}},
		fake.Script{Items: []driver.AgentEventItem{{Kind: driver.ItemText, Text: "continued"}}},
	)
	if blocked, err := session.Run(Message("needs approval")); err != nil || blocked.Blocked == nil {
		t.Fatalf("blocked result=%+v err=%v", blocked, err)
	}
	// An invalid option makes the driver reject the response without tearing down
	// the still-blocked transport.
	if _, err := session.Run(SelectOption("not_an_option")); err == nil {
		t.Fatal("Run accepted an invalid blocked option")
	}
	if session.State() != Blocked {
		t.Fatalf("state after failed response = %q; want blocked", session.State())
	}
	// The turn is still resumable with a valid option.
	continued, err := session.Run(SelectOption("allow_once"))
	if err != nil || continued.Text != "continued" || session.State() != Idle {
		t.Fatalf("retry result=%+v state=%q err=%v", continued, session.State(), err)
	}
}

// A turn may block, be answered, and block again before finally completing.
func TestBlockedResponseBlocksAgain(t *testing.T) {
	fd := fake.New(context.Background(), driver.AgentSpec{})
	agent := newAgentWithDriver(t, fd)
	session, _ := agent.NewSession()
	block := fake.Script{Blocked: &driver.BlockedReason{
		Kind:    driver.BlockedToolApproval,
		Options: []driver.BlockOption{{Value: "allow_once", Label: "Allow once"}},
	}}
	fd.ScriptNextSession(
		block,
		block,
		fake.Script{Items: []driver.AgentEventItem{{Kind: driver.ItemText, Text: "done"}}},
	)
	if first, err := session.Run(Message("step one")); err != nil || first.Blocked == nil || session.State() != Blocked {
		t.Fatalf("first block result=%+v state=%q err=%v", first, session.State(), err)
	}
	second, err := session.Run(SelectOption("allow_once"))
	if err != nil || second.Blocked == nil || session.State() != Blocked {
		t.Fatalf("second block result=%+v state=%q err=%v", second, session.State(), err)
	}
	final, err := session.Run(SelectOption("allow_once"))
	if err != nil || final.Text != "done" || session.State() != Idle {
		t.Fatalf("final result=%+v state=%q err=%v", final, session.State(), err)
	}
}

// Input validation rejects an option on an idle session and any input after close.
func TestRunInputGuards(t *testing.T) {
	t.Run("idle session", func(t *testing.T) {
		agent := newFakeAgent(t)
		session, _ := agent.NewSession()
		if _, err := session.Run(SelectOption("anything")); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("option on idle session error = %v; want invalid state", err)
		}
		if session.State() != Idle {
			t.Fatalf("state after rejected idle option = %q; want idle", session.State())
		}
		if session.ID() != "" {
			t.Fatalf("rejected idle option attached runtime session %q", session.ID())
		}
		if result, err := session.Run(Message("retry")); err != nil || result.Text != "echo: retry" {
			t.Fatalf("retry after rejected idle option: result=%+v err=%v", result, err)
		}
	})
	t.Run("blocked option requires selection", func(t *testing.T) {
		fd := fake.New(context.Background(), driver.AgentSpec{})
		agent := newAgentWithDriver(t, fd)
		session, _ := agent.NewSession()
		fd.ScriptNextSession(fake.Script{Blocked: &driver.BlockedReason{
			Kind:    driver.BlockedToolApproval,
			Options: []driver.BlockOption{{Value: "allow_once", Label: "Allow once"}},
		}})
		if blocked, err := session.Run(Message("block")); err != nil || blocked.Blocked == nil {
			t.Fatalf("blocked result=%+v err=%v", blocked, err)
		}
		if _, err := session.Run(Message("allow_once")); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("message for blocked option error = %v; want invalid state", err)
		}
		if session.State() != Blocked {
			t.Fatalf("state after rejected blocked message = %q; want blocked", session.State())
		}
	})
	t.Run("blocked user input requires message", func(t *testing.T) {
		fd := fake.New(context.Background(), driver.AgentSpec{})
		agent := newAgentWithDriver(t, fd)
		session, _ := agent.NewSession()
		fd.ScriptNextSession(fake.Script{Blocked: &driver.BlockedReason{Kind: driver.BlockedUserInput}})
		if blocked, err := session.Run(Message("block")); err != nil || blocked.Blocked == nil {
			t.Fatalf("blocked result=%+v err=%v", blocked, err)
		}
		if _, err := session.Run(SelectOption("reply")); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("option for blocked user input error = %v; want invalid state", err)
		}
		if session.State() != Blocked {
			t.Fatalf("state after rejected blocked option = %q; want blocked", session.State())
		}
	})
	t.Run("closed agent", func(t *testing.T) {
		fd := fake.New(context.Background(), driver.AgentSpec{})
		agent := newAgentWithDriver(t, fd)
		session, _ := agent.NewSession()
		fd.ScriptNextSession(fake.Script{Blocked: &driver.BlockedReason{Kind: driver.BlockedToolApproval}})
		if blocked, err := session.Run(Message("block")); err != nil || blocked.Blocked == nil {
			t.Fatalf("blocked result=%+v err=%v", blocked, err)
		}
		if err := agent.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := session.Run(SelectOption("allow_once")); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("Run after agent close error = %v; want invalid state", err)
		}
	})
}

func TestInterruptReturnsPartialResult(t *testing.T) {
	fd := fake.New(context.Background(), driver.AgentSpec{})
	agent := newAgentWithDriver(t, fd)
	session, _ := agent.NewSession()
	gate := make(chan struct{})
	fd.ScriptNextSession(fake.Script{
		Items: []driver.AgentEventItem{{Kind: driver.ItemText, Text: "partial"}},
		Wait:  gate,
	})
	stream, err := session.Stream(Message("long"))
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Interrupt(); err != nil {
		t.Fatal(err)
	}
	result, err := stream.Result()
	if err != nil || !result.Interrupted || session.State() != Idle {
		t.Fatalf("result=%+v state=%q err=%v", result, session.State(), err)
	}
	close(gate)
}

func TestAgentContextCancellationStopsDerivedSession(t *testing.T) {
	turnGate := make(chan struct{})
	parent, cancel := context.WithCancel(context.Background())
	var driverCtx context.Context
	var driverSpec driver.AgentSpec
	var fd *fake.Driver
	prev := driverOverride
	driverOverride = func(ctx context.Context, kind AgentKind, spec driver.AgentSpec) (driver.Driver, bool) {
		if kind != Claude {
			t.Fatalf("driver kind = %q; want %q", kind, Claude)
		}
		driverCtx = ctx
		driverSpec = spec
		fd = fake.New(ctx, spec)
		return fd, true
	}
	t.Cleanup(func() { driverOverride = prev })
	agent, err := New(parent, Claude, WithCwd("/repo"), WithModel("model"))
	if err != nil {
		t.Fatal(err)
	}
	if driverCtx == nil {
		t.Fatal("New did not pass the Agent lifecycle context to its driver")
	}
	if driverSpec.Cwd != "/repo" || driverSpec.Model != "model" {
		t.Fatalf("driver spec = %+v", driverSpec)
	}
	t.Cleanup(func() { _ = agent.Close() })
	session, _ := agent.NewSession()
	fd.ScriptNextSession(fake.Script{Wait: turnGate})

	done := make(chan error, 1)
	go func() {
		_, err := session.Run(Message("long"))
		done <- err
	}()
	waitDriverPhase(t, session, driver.PhasePromptInFlight)
	cancel()
	select {
	case <-driverCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("driver lifecycle context was not cancelled")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("run error = %v; want context.Canceled", err)
	}
	if session.State() != Idle {
		t.Fatalf("state after confirmation = %q", session.State())
	}
	close(turnGate)
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
	driver.Driver
	started chan struct{}
	release chan struct{}
}

type blockingPromptDriver struct {
	driver.Driver
	started chan struct{}
	release chan struct{}
}

type recordingOpenDriver struct {
	driver.Driver
	mu     sync.Mutex
	params driver.OpenParams
}

type staleAttachmentDriver struct {
	driver.Driver
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

func (d *staleAttachmentDriver) OpenSession(params driver.OpenParams) (*driver.SessionAttachment, error) {
	att, err := d.Driver.OpenSession(params)
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

func (s *staleAttachmentSession) Send(input driver.UserInput) (driver.RunID, error) {
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
	return s.Session.Send(input)
}

func (d *recordingOpenDriver) OpenSession(params driver.OpenParams) (*driver.SessionAttachment, error) {
	d.mu.Lock()
	d.params = params
	d.mu.Unlock()
	return d.Driver.OpenSession(params)
}

func (d *blockingPromptDriver) OpenSession(params driver.OpenParams) (*driver.SessionAttachment, error) {
	att, err := d.Driver.OpenSession(params)
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

func (s *blockingPromptSession) Send(input driver.UserInput) (driver.RunID, error) {
	close(s.started)
	<-s.release
	return s.Session.Send(input)
}

func (d *blockingOpenDriver) OpenSession(params driver.OpenParams) (*driver.SessionAttachment, error) {
	close(d.started)
	<-d.release
	return d.Driver.OpenSession(params)
}

func TestAgentCloseCannotRaceInANewAttachment(t *testing.T) {
	fd := fake.New(context.Background(), driver.AgentSpec{})
	blocking := &blockingOpenDriver{Driver: fd, started: make(chan struct{}), release: make(chan struct{})}
	prev := driverOverride
	driverOverride = func(context.Context, AgentKind, driver.AgentSpec) (driver.Driver, bool) { return blocking, true }
	t.Cleanup(func() { driverOverride = prev })
	agent, err := New(context.Background(), Claude)
	if err != nil {
		t.Fatal(err)
	}
	session, _ := agent.NewSession()
	done := make(chan error, 1)
	go func() {
		_, err := session.Run(Message("hello"))
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
	fd := fake.New(context.Background(), driver.AgentSpec{})
	blocking := &blockingPromptDriver{Driver: fd, started: make(chan struct{}), release: make(chan struct{})}
	prev := driverOverride
	driverOverride = func(context.Context, AgentKind, driver.AgentSpec) (driver.Driver, bool) { return blocking, true }
	t.Cleanup(func() { driverOverride = prev })
	agent, err := New(context.Background(), Claude)
	if err != nil {
		t.Fatal(err)
	}
	session, _ := agent.NewSession()
	streamDone := make(chan error, 1)
	go func() {
		_, err := session.Stream(Message("hello"))
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
	fd := fake.New(context.Background(), driver.AgentSpec{})
	blocking := &blockingPromptDriver{Driver: fd, started: make(chan struct{}), release: make(chan struct{})}
	prev := driverOverride
	driverOverride = func(context.Context, AgentKind, driver.AgentSpec) (driver.Driver, bool) { return blocking, true }
	t.Cleanup(func() { driverOverride = prev })
	agent, err := New(context.Background(), Claude)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	session, _ := agent.NewSession()
	streamDone := make(chan *Stream, 1)
	go func() {
		stream, _ := session.Stream(Message("hello"))
		streamDone <- stream
	}()
	<-blocking.started
	interruptDone := make(chan error, 1)
	go func() { interruptDone <- session.Interrupt() }()
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
	fd := fake.New(context.Background(), driver.AgentSpec{})
	agent := newAgentWithDriver(t, fd)
	session, _ := agent.NewSession()
	fd.ScriptNextSession(fake.Script{Result: driver.RunResult{FinishReason: driver.FinishTransportClosed}})
	_, err := session.Run(Message("hello"))
	if kind, ok := errs.KindOf(err); !ok || kind != errs.KindTransport {
		t.Fatalf("error = %v; want transport", err)
	}
}

func TestInterruptedStreamMustFinishBeforeNextTurn(t *testing.T) {
	gate := make(chan struct{})
	fd := fake.New(context.Background(), driver.AgentSpec{})
	agent := newAgentWithDriver(t, fd)
	session, _ := agent.NewSession()
	fd.ScriptNextSession(fake.Script{Wait: gate})
	first, err := session.Stream(Message("long"))
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Interrupt(); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Stream(Message("too early")); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("new turn before interrupted stream terminal = %v", err)
	}
	if _, err := first.Result(); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Run(Message("now allowed")); err != nil {
		t.Fatal(err)
	}
	close(gate)
}

func TestListSessionsAfterAgentCloseFails(t *testing.T) {
	agent := newFakeAgent(t)
	if err := agent.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.ListSessions(); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("ListSessions after Close error = %v", err)
	}
}

func TestSessionRejectsRuntimeIDChange(t *testing.T) {
	agent := newFakeAgent(t)
	session, _ := agent.NewSession()
	if err := session.setID("original"); err != nil {
		t.Fatal(err)
	}
	if err := session.setID("replacement"); err == nil {
		t.Fatal("session accepted a different runtime ID")
	}
}

func TestGetSessionUsesPersistedCwdForResume(t *testing.T) {
	fd := fake.New(context.Background(), driver.AgentSpec{})
	fd.SetStoredSessions([]driver.StoredSessionMeta{{SessionID: "stored", Cwd: "/other/repo"}})
	recording := &recordingOpenDriver{Driver: fd}
	prev := driverOverride
	driverOverride = func(context.Context, AgentKind, driver.AgentSpec) (driver.Driver, bool) { return recording, true }
	t.Cleanup(func() { driverOverride = prev })
	agent, err := New(context.Background(), Claude, WithCwd("/default/repo"))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	session, err := agent.GetSession("stored")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Run(Message("continue")); err != nil {
		t.Fatal(err)
	}
	recording.mu.Lock()
	cwd := recording.params.Cwd
	recording.mu.Unlock()
	if cwd != "/other/repo" {
		t.Fatalf("resume cwd = %q; want persisted cwd", cwd)
	}
}

func TestRunReattachesAClosedDriverSession(t *testing.T) {
	fd := fake.New(context.Background(), driver.AgentSpec{})
	staleDriver := &staleAttachmentDriver{Driver: fd}
	prev := driverOverride
	driverOverride = func(context.Context, AgentKind, driver.AgentSpec) (driver.Driver, bool) { return staleDriver, true }
	t.Cleanup(func() { driverOverride = prev })
	agent, err := New(context.Background(), Codex)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	session, _ := agent.NewSession()
	if _, err := session.Run(Message("first")); err != nil {
		t.Fatal(err)
	}
	staleDriver.mu.Lock()
	first := staleDriver.latest
	staleDriver.mu.Unlock()
	first.markStale()
	if _, err := session.Run(Message("second")); err != nil {
		t.Fatal(err)
	}
	staleDriver.mu.Lock()
	opens := staleDriver.opens
	staleDriver.mu.Unlock()
	if opens != 2 {
		t.Fatalf("OpenSession calls = %d; want 2 after stale attachment", opens)
	}
}

func TestBlockedResponseDropsAClosedAttachment(t *testing.T) {
	fd := fake.New(context.Background(), driver.AgentSpec{})
	staleDriver := &staleAttachmentDriver{Driver: fd}
	prev := driverOverride
	driverOverride = func(context.Context, AgentKind, driver.AgentSpec) (driver.Driver, bool) { return staleDriver, true }
	t.Cleanup(func() { driverOverride = prev })
	agent, err := New(context.Background(), Codex)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	session, _ := agent.NewSession()
	fd.ScriptNextSession(fake.Script{Blocked: &driver.BlockedReason{Kind: driver.BlockedToolApproval}})
	if result, err := session.Run(Message("block")); err != nil || result.Blocked == nil {
		t.Fatalf("blocked result=%+v err=%v", result, err)
	}
	staleDriver.mu.Lock()
	first := staleDriver.latest
	staleDriver.mu.Unlock()
	first.markStale()
	if _, err := session.Run(SelectOption("allow_once")); err == nil {
		t.Fatal("Run accepted input for a closed blocked attachment")
	}
	if session.State() != Idle {
		t.Fatalf("state after stale blocked response = %q; want idle", session.State())
	}
	if _, err := session.Run(Message("recover")); err != nil {
		t.Fatal(err)
	}
}

func TestPromptErrorDropsAClosedAttachment(t *testing.T) {
	fd := fake.New(context.Background(), driver.AgentSpec{})
	staleDriver := &staleAttachmentDriver{Driver: fd, failFirstPrompt: true}
	prev := driverOverride
	driverOverride = func(context.Context, AgentKind, driver.AgentSpec) (driver.Driver, bool) { return staleDriver, true }
	t.Cleanup(func() { driverOverride = prev })
	agent, err := New(context.Background(), Codex)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	session, _ := agent.NewSession()
	if _, err := session.Run(Message("fails")); err == nil {
		t.Fatal("first Run unexpectedly succeeded")
	}
	if _, err := session.Run(Message("recovers")); err != nil {
		t.Fatal(err)
	}
	staleDriver.mu.Lock()
	opens := staleDriver.opens
	staleDriver.mu.Unlock()
	if opens != 2 {
		t.Fatalf("OpenSession calls = %d; want closed attachment replaced", opens)
	}
}

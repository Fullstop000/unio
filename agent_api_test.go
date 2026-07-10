package unio

import (
	"context"
	"errors"
	"testing"

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

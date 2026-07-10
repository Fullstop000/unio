package unio

import (
	"context"
	"testing"
	"time"

	"github.com/Fullstop000/unio/driver"
	"github.com/Fullstop000/unio/driver/fake"
	"github.com/Fullstop000/unio/errs"
)

// withFakeDriver installs a fake-backed driverOverride for the test and returns
// the fake so the test can script sessions. It restores the override on cleanup.
func withFakeDriver(t *testing.T) *fake.Driver {
	t.Helper()
	fd := fake.New()
	prev := driverOverride
	driverOverride = func(a Agent) (driver.ProtocolDriver, bool) { return fd, true }
	t.Cleanup(func() { driverOverride = prev })
	return fd
}

func TestRunOneShot(t *testing.T) {
	fd := withFakeDriver(t)
	// autoKey mints "claude-N"; script by not caring about the exact key —
	// the fake echoes when unscripted, but we want deterministic usage, so
	// script the next key. autoKey is sequential, so open once to learn it is
	// awkward; instead rely on the fake's default echo + assert structure.
	_ = fd

	res, err := Run(context.Background(), Claude, "hello")
	if err != nil {
		t.Fatal(err)
	}
	// Fake's default unscripted behaviour echoes "echo: <prompt>".
	if res.Text != "echo: hello" {
		t.Fatalf("unexpected text: %q", res.Text)
	}
	if res.SessionID == "" {
		t.Fatal("expected a session id")
	}
	if res.FinishReason != FinishNatural {
		t.Fatalf("expected natural finish, got %q", res.FinishReason)
	}
}

func TestStreamAndResult(t *testing.T) {
	withFakeDriver(t)
	s, err := Start(context.Background(), Claude)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// One Prompt returns a Stream: range it for events, then Result() for the
	// terminal outcome — streaming and final usage from the same handle.
	st := s.Prompt(context.Background(), "stream me")
	var sawText bool
	for st.Next() {
		if ev := st.Event(); ev.Kind == KindText && ev.Text == "echo: stream me" {
			sawText = true
		}
	}
	if !sawText {
		t.Fatal("stream should carry the echoed text")
	}
	res, err := st.Result()
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "echo: stream me" || res.FinishReason != FinishNatural {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestResultOnlyNoIteration(t *testing.T) {
	withFakeDriver(t)
	s, err := Start(context.Background(), Claude)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// One-shot path: skip Next(), block straight for Result.
	res, err := s.Prompt(context.Background(), "hi").Result()
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "echo: hi" {
		t.Fatalf("unexpected: %+v", res)
	}
}

func TestSerialPromptGuard(t *testing.T) {
	withFakeDriver(t)
	s, err := Start(context.Background(), Claude)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// First turn started but NOT drained → second Prompt must be rejected.
	first := s.Prompt(context.Background(), "one")
	second := s.Prompt(context.Background(), "two")
	if _, err := second.Result(); err == nil {
		t.Fatal("concurrent second Prompt should error (serial guard)")
	}
	// Draining the first frees the session for the next turn.
	if _, err := first.Result(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prompt(context.Background(), "three").Result(); err != nil {
		t.Fatalf("prompt after draining should succeed, got %v", err)
	}
}

func TestRunEnumsAreTyped(t *testing.T) {
	// Compile-time + value checks that the facade exposes typed enums, not bare
	// strings, so callers switch on constants.
	var k EventKind = KindToolCall
	if string(k) != "tool_call" {
		t.Fatalf("EventKind value drift: %q", k)
	}
	var f FinishReason = FinishCancelled
	if string(f) != "cancelled" {
		t.Fatalf("FinishReason value drift: %q", f)
	}
}

func TestOptionsApply(t *testing.T) {
	cfg := buildConfig([]Option{
		WithModel("m1"),
		WithCwd("/tmp/x"),
		WithSystemPrompt("be brief"),
		WithResume("prior-123"),
		WithExtraArgs("--flag"),
		WithEnv("K=V"),
	})
	if cfg.model != "m1" || cfg.cwd != "/tmp/x" || cfg.systemPrompt != "be brief" || cfg.resume != "prior-123" {
		t.Fatalf("options not applied: %+v", cfg)
	}
	if len(cfg.extraArgs) != 1 || cfg.extraArgs[0] != "--flag" || len(cfg.env) != 1 {
		t.Fatalf("slice options not applied: %+v", cfg)
	}
	// resume flows into OpenParams via spec()/Open; spec carries the rest.
	sp := cfg.spec()
	if sp.Model != "m1" || sp.Cwd != "/tmp/x" || sp.SystemPrompt != "be brief" {
		t.Fatalf("spec not built from config: %+v", sp)
	}
}

func TestRunNotInstalledSurfacesTypedError(t *testing.T) {
	fd := fake.New()
	fd.SetRequireInstall(true) // makes OpenSession run the PATH check
	prev := driverOverride
	driverOverride = func(a Agent) (driver.ProtocolDriver, bool) { return fd, true }
	t.Cleanup(func() { driverOverride = prev })
	t.Setenv("PATH", t.TempDir())

	_, err := Run(context.Background(), Claude, "hi", WithExtraArgs())
	if err == nil {
		t.Fatal("expected not_installed error")
	}
	kind, ok := errs.KindOf(err)
	if !ok || kind != errs.KindNotInstalled {
		t.Fatalf("expected not_installed typed error, got %v", err)
	}
}

func TestResumeThreadsThroughOption(t *testing.T) {
	withFakeDriver(t)
	s, err := Start(context.Background(), Claude, WithResume("resume-xyz"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.SessionID() != "resume-xyz" {
		t.Fatalf("WithResume should reattach the prior id, got %q", s.SessionID())
	}
}

func TestOpenAliasesStart(t *testing.T) {
	withFakeDriver(t)
	s, err := Open(context.Background(), Claude)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.SessionID() == "" {
		t.Fatal("expected session id")
	}
}

// guard against slow hang in CI.
func TestRunRespectsContext(t *testing.T) {
	withFakeDriver(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := Run(ctx, Claude, "hi"); err != nil {
		t.Fatalf("run under generous deadline should succeed, got %v", err)
	}
}

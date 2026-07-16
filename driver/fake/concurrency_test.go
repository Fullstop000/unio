package fake

import (
	"context"
	"sync"
	"testing"

	"github.com/Fullstop000/unio/driver"
	"github.com/Fullstop000/unio/errs"
)

// TestConcurrentSessionUseIsSafe hammers one session with concurrent
// Send/Interrupt/ProcessState/Close from many goroutines. The driver must
// guarantee safety itself (SPEC §Concurrency) — run under `go test -race`.
func TestConcurrentSessionUseIsSafe(t *testing.T) {
	d := New(context.Background(), driver.AgentSpec{})
	att, err := d.OpenSession(driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	// Drain events so the bus never blocks.
	ch := att.Events.Subscribe()
	go func() {
		for range ch {
		}
	}()
	if err := att.Session.Start(); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_, _ = att.Session.Send(driver.UserMessage{Text: "x"})
				_ = att.Session.Interrupt()
				_ = att.Session.ProcessState()
				_ = att.Session.SessionID()
			}
		}()
	}
	wg.Wait()
	if err := att.Session.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestPostCloseCallsFail verifies Start/Send after Close return a closed error
// and Close is idempotent.
func TestPostCloseCallsFail(t *testing.T) {
	d := New(context.Background(), driver.AgentSpec{})
	att, _ := d.OpenSession(driver.OpenParams{})
	_ = att.Events.Subscribe()
	_ = att.Session.Start()

	if err := att.Session.Close(); err != nil {
		t.Fatal(err)
	}
	// Idempotent.
	if err := att.Session.Close(); err != nil {
		t.Fatalf("second Close should be a no-op, got %v", err)
	}
	// Send after close fails with unsupported/closed.
	if _, err := att.Session.Send(driver.UserMessage{Text: "x"}); err == nil {
		t.Fatal("Send after Close should fail")
	} else if kind, ok := errs.KindOf(err); !ok || kind != errs.KindUnsupported {
		t.Fatalf("expected unsupported after close, got %v", err)
	}
	// Start after close fails too.
	if err := att.Session.Start(); err == nil {
		t.Fatal("Start after Close should fail")
	}
}

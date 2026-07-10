package fake

import (
	"context"
	"sync"
	"testing"

	"github.com/Fullstop000/unio/driver"
	"github.com/Fullstop000/unio/errs"
)

// TestConcurrentSessionUseIsSafe hammers one session with concurrent
// Prompt/Cancel/ProcessState/Close from many goroutines. The driver must
// guarantee safety itself (SPEC §Concurrency) — run under `go test -race`.
func TestConcurrentSessionUseIsSafe(t *testing.T) {
	d := New()
	key := driver.SessionKey("w-concurrent")
	att, err := d.OpenSession(context.Background(), key, driver.AgentSpec{}, driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	// Drain events so the bus never blocks.
	ch := att.Events.Subscribe()
	go func() {
		for range ch {
		}
	}()
	if err := att.Session.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_, _ = att.Session.Prompt(context.Background(), driver.PromptReq{Text: "x"})
				_, _ = att.Session.Cancel(context.Background(), "r")
				_ = att.Session.ProcessState()
				_ = att.Session.SessionID()
			}
		}()
	}
	wg.Wait()
	if err := att.Session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestPostCloseCallsFail verifies Run/Prompt after Close return a closed error
// and Close is idempotent.
func TestPostCloseCallsFail(t *testing.T) {
	d := New()
	key := driver.SessionKey("w-closed")
	att, _ := d.OpenSession(context.Background(), key, driver.AgentSpec{}, driver.OpenParams{})
	_ = att.Events.Subscribe()
	_ = att.Session.Run(context.Background(), nil)

	if err := att.Session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Idempotent.
	if err := att.Session.Close(context.Background()); err != nil {
		t.Fatalf("second Close should be a no-op, got %v", err)
	}
	// Prompt after close fails with unsupported/closed.
	if _, err := att.Session.Prompt(context.Background(), driver.PromptReq{Text: "x"}); err == nil {
		t.Fatal("Prompt after Close should fail")
	} else if kind, ok := errs.KindOf(err); !ok || kind != errs.KindUnsupported {
		t.Fatalf("expected unsupported after close, got %v", err)
	}
	// Run after close fails too.
	if err := att.Session.Run(context.Background(), nil); err == nil {
		t.Fatal("Run after Close should fail")
	}
}

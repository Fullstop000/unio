package driver

import (
	"testing"
	"time"
)

// --- EventBus ---

func drainOne(t *testing.T, ch <-chan AgentEvent, timeout time.Duration) (AgentEvent, bool) {
	t.Helper()
	select {
	case ev, ok := <-ch:
		return ev, ok
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for event")
		return AgentEvent{}, false
	}
}

func TestEventBusFirstSubscribeReceives(t *testing.T) {
	bus := NewEventBus()
	ch := bus.Subscribe()
	key := SessionKey("w-s")
	bus.Emit(SessionAttachedEvent(key, "sid-1"))

	ev, ok := drainOne(t, ch, 500*time.Millisecond)
	if !ok {
		t.Fatal("channel closed unexpectedly")
	}
	if ev.Type != EventSessionAttached || ev.SessionID != "sid-1" {
		t.Fatalf("unexpected event: %+v", ev)
	}
}

func TestEventBusFanOutToMultiple(t *testing.T) {
	bus := NewEventBus()
	a := bus.Subscribe()
	b := bus.Subscribe()
	key := SessionKey("w-s")
	bus.Emit(LifecycleEvent(key, ProcessState{Phase: PhaseActive}))

	for _, ch := range []<-chan AgentEvent{a, b} {
		ev, ok := drainOne(t, ch, 500*time.Millisecond)
		if !ok || ev.Type != EventLifecycle {
			t.Fatalf("both subscribers should get the event, got ok=%v ev=%+v", ok, ev)
		}
	}
}

func TestEventBusSlowObserverDoesNotStallFast(t *testing.T) {
	bus := NewEventBus()
	fast := bus.Subscribe()
	_ = bus.Subscribe() // slow: never drained
	key := SessionKey("w-s")

	total := observerCapacity + 20
	for i := 0; i < total; i++ {
		bus.Emit(LifecycleEvent(key, ProcessState{Phase: PhaseActive}))
	}

	got := 0
	for got < total {
		select {
		case _, ok := <-fast:
			if !ok {
				t.Fatalf("fast observer closed early at %d/%d", got, total)
			}
			got++
		case <-time.After(time.Second):
			t.Fatalf("fast observer stalled at %d/%d (slow peer should not block it)", got, total)
		}
	}
	if bus.Dropped() == 0 {
		t.Fatal("expected the slow observer to have dropped events")
	}
}

func TestEventBusPreservesTerminalEventForFullObserver(t *testing.T) {
	bus := NewEventBus()
	ch := bus.Subscribe()
	for i := 0; i < observerCapacity+1; i++ {
		bus.Emit(OutputEvent("key", "sid", "run", AgentEventItem{Kind: ItemText, Text: "x"}))
	}
	deadline := time.Now().Add(2 * time.Second)
	for bus.Dropped() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if bus.Dropped() == 0 {
		t.Fatal("observer did not become full")
	}
	beforeTerminal := bus.Dropped()
	bus.Emit(CompletedEvent("key", "sid", "run", RunResult{FinishReason: FinishNatural}))
	deadline = time.Now().Add(2 * time.Second)
	for bus.Dropped() == beforeTerminal && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case ev := <-ch:
			if ev.Type == EventCompleted {
				return
			}
		case <-timer.C:
			t.Fatal("completed event was dropped for a full observer")
		}
	}
}

func TestEventBusCloseDrainsAndClosesObservers(t *testing.T) {
	bus := NewEventBus()
	ch := bus.Subscribe()
	key := SessionKey("w-s")
	for i := 0; i < 3; i++ {
		bus.Emit(LifecycleEvent(key, ProcessState{Phase: PhaseActive}))
	}
	bus.Close()

	seen := 0
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				if seen < 3 {
					t.Fatalf("channel closed before draining buffered events: %d/3", seen)
				}
				return
			}
			seen++
		case <-time.After(time.Second):
			t.Fatal("channel neither delivered nor closed in time")
		}
	}
}

func TestEventBusSubscribeAfterCloseReturnsClosed(t *testing.T) {
	bus := NewEventBus()
	_ = bus.Subscribe()
	bus.Close()
	// Give the dispatcher a moment to exit.
	time.Sleep(20 * time.Millisecond)

	late := bus.Subscribe()
	select {
	case _, ok := <-late:
		if ok {
			t.Fatal("late subscribe after close should observe a closed channel")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("late subscribe hung instead of returning a closed channel")
	}
}

// --- Registry ---

type fakeProc struct {
	name  string
	stale bool
}

func (p *fakeProc) IsStale() bool      { return p.stale }
func (p *fakeProc) DriverName() string { return p.name }

func TestRegistryGetOrInitCachesAndEvictsStale(t *testing.T) {
	reg := NewRegistry[*fakeProc]()

	calls := 0
	factory := func() *fakeProc { calls++; return &fakeProc{name: "x"} }

	first := reg.GetOrInit("k", factory)
	second := reg.GetOrInit("k", factory)
	if calls != 1 {
		t.Fatalf("factory should run once for a live entry, ran %d", calls)
	}
	if first != second {
		t.Fatal("expected the same cached instance")
	}

	// Mark stale → next GetOrInit rebuilds.
	first.stale = true
	third := reg.GetOrInit("k", factory)
	if calls != 2 {
		t.Fatalf("factory should rerun after stale eviction, ran %d", calls)
	}
	if third == first {
		t.Fatal("expected a fresh instance after eviction")
	}
}

func TestRegistryGetOrEvictStale(t *testing.T) {
	reg := NewRegistry[*fakeProc]()
	reg.Insert("k", &fakeProc{name: "x"})

	if _, ok := reg.GetOrEvictStale("k"); !ok {
		t.Fatal("live entry should be returned")
	}

	reg.Insert("k", &fakeProc{name: "x", stale: true})
	if _, ok := reg.GetOrEvictStale("k"); ok {
		t.Fatal("stale entry should be evicted and report false")
	}
	if reg.Len() != 0 {
		t.Fatalf("stale entry should have been removed, len=%d", reg.Len())
	}
}

// --- AgentError ---

func TestAgentErrorFormatting(t *testing.T) {
	if got := NewTimeoutError("").Error(); got != "timeout" {
		t.Fatalf("empty-msg error should render kind only, got %q", got)
	}
	if got := NewTransportError("eof").Error(); got != "transport: eof" {
		t.Fatalf("unexpected error string %q", got)
	}
}

// --- TokenUsage ---

func TestTokenUsageAdd(t *testing.T) {
	u := TokenUsage{InputTokens: 10, OutputTokens: 5, CostUSD: 0.01}
	u.Add(TokenUsage{InputTokens: 3, CacheReadTokens: 7, CostUSD: 0.02})
	if u.InputTokens != 13 || u.CacheReadTokens != 7 || u.OutputTokens != 5 {
		t.Fatalf("unexpected accumulation: %+v", u)
	}
	if u.CostUSD < 0.0299 || u.CostUSD > 0.0301 {
		t.Fatalf("unexpected cost accumulation: %v", u.CostUSD)
	}
}

func TestBlockedEventCarriesReason(t *testing.T) {
	reason := BlockedReason{
		Kind:    BlockedToolApproval,
		Message: "Allow go test?",
		Options: []BlockOption{{Value: "allow_once", Label: "Allow once"}},
	}
	ev := BlockedEvent("key", "sid", "run", reason)
	if ev.Type != EventBlocked || ev.Blocked == nil || ev.Blocked.Kind != BlockedToolApproval {
		t.Fatalf("unexpected blocked event: %+v", ev)
	}
}

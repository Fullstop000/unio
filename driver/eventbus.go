package driver

import (
	"sync"
	"sync/atomic"
)

// Event bus channel sizing. Inbound is generous so bursts of output items from a
// runtime's reader loop don't push back on the transport goroutine. Per-observer
// queues are smaller; on overflow we drop + count rather than block the
// dispatcher, so one slow subscriber can't stall the reader for everyone.
const (
	inboundCapacity  = 512
	observerCapacity = 256
)

// EventBus is a single-inbox, multi-subscriber fan-out for AgentEvents. Drivers
// publish via Emit; consumers Subscribe to get an independent bounded channel.
//
// Back-pressure policy: a full observer queue drops an event (incrementing a
// counter) instead of blocking the dispatcher. Terminal completed, failed, and
// blocked events evict one older buffered event so a slow consumer still learns
// how its turn stopped.
//
// Lifecycle: the dispatcher goroutine starts on the first Subscribe (exactly
// once). Close signals it to drain the inbox and exit; late Subscribes after
// close return an already-closed channel rather than hanging.
type EventBus struct {
	inbound chan AgentEvent

	mu        sync.RWMutex
	observers []chan AgentEvent

	stop      chan struct{}
	started   atomic.Bool
	closing   atomic.Bool
	dropped   atomic.Uint64
	closeOnce sync.Once
}

// NewEventBus constructs an idle bus. No goroutine runs until the first
// Subscribe.
func NewEventBus() *EventBus {
	return &EventBus{
		inbound: make(chan AgentEvent, inboundCapacity),
		stop:    make(chan struct{}),
	}
}

// Emit publishes an event. It never blocks the caller and never sends on a
// closed channel: after Close it drops+counts. Drivers call this from reader
// goroutines that may still be running as Close races in.
func (b *EventBus) Emit(ev AgentEvent) {
	if b.closing.Load() {
		b.dropped.Add(1)
		return
	}
	select {
	case b.inbound <- ev:
	default:
		b.makeRoomForTerminal(b.inbound, ev)
	}
}

// Subscribe registers a new observer and returns a bounded receive channel that
// gets every event emitted from this point forward. The channel is closed when
// the bus is closed and drained. The first call starts the dispatcher.
func (b *EventBus) Subscribe() <-chan AgentEvent {
	ch := make(chan AgentEvent, observerCapacity)

	// If the bus is already closing/closed, hand back a closed channel so the
	// caller's range/recv observes completion promptly instead of hanging.
	if b.closing.Load() {
		close(ch)
		return ch
	}

	b.mu.Lock()
	b.observers = append(b.observers, ch)
	b.mu.Unlock()

	b.startOnce()
	return ch
}

// startOnce spawns the dispatcher exactly once.
func (b *EventBus) startOnce() {
	if b.started.Swap(true) {
		return
	}
	go b.dispatch()
}

// dispatch is the single fan-out goroutine. It fans events out until stop is
// signalled, then drains any buffered events and closes all observers. inbound
// is never closed, so a concurrent Emit can never send on a closed channel.
func (b *EventBus) dispatch() {
	for {
		select {
		case ev := <-b.inbound:
			b.fanout(ev)
		case <-b.stop:
			// Drain whatever is buffered, then exit.
			for {
				select {
				case ev := <-b.inbound:
					b.fanout(ev)
				default:
					b.closeObservers()
					return
				}
			}
		}
	}
}

// fanout delivers one event to every observer, dropping on a full queue.
func (b *EventBus) fanout(ev AgentEvent) {
	b.mu.RLock()
	obs := make([]chan AgentEvent, len(b.observers))
	copy(obs, b.observers)
	b.mu.RUnlock()

	for _, ch := range obs {
		select {
		case ch <- ev:
		default:
			b.makeRoomForTerminal(ch, ev)
		}
	}
}

func (b *EventBus) makeRoomForTerminal(ch chan AgentEvent, ev AgentEvent) {
	if !terminalEvent(ev.Type) {
		b.dropped.Add(1)
		return
	}
	select {
	case <-ch:
		b.dropped.Add(1)
	default:
	}
	select {
	case ch <- ev:
	default:
		b.dropped.Add(1)
	}
}

func terminalEvent(typ EventType) bool {
	return typ == EventCompleted || typ == EventFailed || typ == EventBlocked
}

// closeObservers closes and clears every observer channel.
func (b *EventBus) closeObservers() {
	b.mu.Lock()
	for _, ch := range b.observers {
		close(ch)
	}
	b.observers = nil
	b.mu.Unlock()
}

// Close signals the dispatcher to drain and exit. Idempotent. After Close, Emit
// drops+counts. If no dispatcher ever started, observers are closed directly.
func (b *EventBus) Close() {
	b.closeOnce.Do(func() {
		b.closing.Store(true)
		close(b.stop)
		if !b.started.Load() {
			b.closeObservers()
		}
	})
}

// Dropped returns the number of events dropped due to full queues. Useful in
// tests and for a host to surface a back-pressure metric.
func (b *EventBus) Dropped() uint64 { return b.dropped.Load() }

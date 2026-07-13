package unio

import (
	"context"
	"errors"

	"github.com/Fullstop000/unio/driver"
)

// Stream exposes the observable events of one Run.
type Stream struct {
	ctx     context.Context
	ctxDone <-chan struct{}
	owner   *Session
	events  <-chan driver.AgentEvent
	runID   driver.RunID

	cur       Event
	done      bool
	result    Result
	err       error
	text      []byte
	thinking  []byte
	cancelErr error
}

func newStream(ctx context.Context, owner *Session, events <-chan driver.AgentEvent, runID driver.RunID) *Stream {
	return &Stream{ctx: ctx, ctxDone: ctx.Done(), owner: owner, events: events, runID: runID}
}

// Next advances to the next text, thinking, tool-call, or tool-result event.
func (s *Stream) Next() bool {
	if s.done {
		return false
	}
	for {
		select {
		case ev, ok := <-s.events:
			if !ok {
				if err := s.ctx.Err(); err != nil {
					s.finish(Result{}, err, Idle)
					return false
				}
				s.finish(Result{}, driver.NewTransportError("unio: event stream closed before completion"), Idle)
				return false
			}
			if ev.Type == driver.EventSessionAttached {
				if err := s.owner.setID(ev.SessionID); err != nil {
					s.finish(Result{}, err, Idle)
					return false
				}
				continue
			}
			if ev.RunID != s.runID {
				continue
			}
			switch ev.Type {
			case driver.EventOutput:
				if ev.Item.Kind == driver.ItemTurnEnd {
					continue
				}
				s.cur = s.accumulate(ev.Item)
				return true
			case driver.EventCompleted:
				s.result.Text = string(s.text)
				s.result.Thinking = string(s.thinking)
				s.result.SessionID = ev.SessionID
				s.result.Usage = ev.Result.Usage
				s.result.DurationMs = ev.Result.DurationMs
				s.result.Interrupted = ev.Result.FinishReason == driver.FinishCancelled
				_ = s.owner.setID(ev.SessionID)
				if ev.Result.FinishReason == driver.FinishTransportClosed {
					s.owner.dropAttachment()
					s.finish(s.result, driver.NewTransportError("agent transport closed during turn"), Idle)
					return false
				}
				s.finish(s.result, s.cancelErr, Idle)
				return false
			case driver.EventBlocked:
				s.result.Text = string(s.text)
				s.result.Thinking = string(s.thinking)
				s.result.SessionID = ev.SessionID
				if ev.Blocked != nil {
					reason := *ev.Blocked
					s.result.Blocked = &reason
				}
				s.finish(s.result, nil, Blocked)
				return false
			case driver.EventFailed:
				s.result.Text = string(s.text)
				s.result.Thinking = string(s.thinking)
				s.finish(s.result, failedToError(ev), Idle)
				return false
			}
		case <-s.ctxDone:
			s.cancelErr = s.ctx.Err()
			s.ctxDone = nil
			err := s.owner.Interrupt()
			if err != nil {
				s.finish(s.result, errors.Join(s.cancelErr, err), Idle)
				return false
			}
		}
	}
}

// Event returns the event produced by the latest successful Next call.
func (s *Stream) Event() Event { return s.cur }

// Result drains the stream and returns its accumulated result.
func (s *Stream) Result() (Result, error) {
	for s.Next() {
	}
	return s.result, s.err
}

func (s *Stream) accumulate(item driver.AgentEventItem) Event {
	switch item.Kind {
	case driver.ItemText:
		s.text = append(s.text, item.Text...)
	case driver.ItemThinking:
		s.thinking = append(s.thinking, item.Text...)
	case driver.ItemToolCall:
		s.result.ToolCalls = append(s.result.ToolCalls, ToolCall{Name: item.Tool, Input: item.ToolInput})
	}
	return Event{Kind: EventKind(item.Kind), Text: item.Text, Tool: item.Tool, ToolInput: item.ToolInput}
}

func (s *Stream) finish(result Result, err error, state SessionState) {
	if s.done {
		return
	}
	s.done = true
	s.result = result
	s.err = err
	s.owner.setState(state)
}

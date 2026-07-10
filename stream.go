package unio

import (
	"context"

	"github.com/Fullstop000/unio/driver"
)

// ToolCall records one tool invocation observed during a turn.
type ToolCall struct {
	Name  string
	Input any
}

// Result is the outcome of a completed turn. It carries the full picture of what
// the agent did — text, thinking, and tool calls — so callers that want to
// inspect a turn don't have to stream it themselves.
type Result struct {
	// Text is the concatenated assistant-visible text.
	Text string
	// Thinking is the concatenated reasoning/thinking text (empty if none).
	Thinking string
	// ToolCalls are the tool invocations made during the turn, in order.
	ToolCalls []ToolCall
	// SessionID is the runtime-owned session id (pass to WithResume later).
	SessionID string
	// FinishReason is why the turn ended.
	FinishReason FinishReason
	// Usage is per-model token/cost usage, when the agent reported it.
	Usage map[string]driver.TokenUsage
}

// Stream is the handle returned by Session.Prompt. One type serves both usage
// styles, so streaming callers still get the final Result (usage/finish):
//
//	// streaming: range the events as they arrive
//	st := s.Prompt(ctx, "explain this repo")
//	for st.Next() {
//	    ev := st.Event()
//	    if ev.Kind == unio.KindText { fmt.Print(ev.Text) }
//	}
//	res, err := st.Result() // final outcome, after the stream drains
//
//	// one-shot: skip iteration, just block for the result
//	res, err := s.Prompt(ctx, "reply with one word: ping").Result()
type Stream struct {
	sub    <-chan driver.AgentEvent
	runID  driver.RunID
	sid    func() string
	onDone func() // released to the owning Session when the turn ends (once)

	cur      Event
	done     bool
	res      Result
	resErr   error
	acc      Result // accumulates text/thinking/tool calls as events arrive
	textBuf  []byte
	thinkBuf []byte
	ctx      context.Context
}

// Next advances to the next event of this turn. It returns false when the turn
// ends (the terminal Completed/Failed is consumed internally and turned into the
// Result). After Next returns false, call Result.
func (st *Stream) Next() bool {
	if st.done {
		return false
	}
	for {
		select {
		case ev, ok := <-st.sub:
			if !ok {
				st.finish(Result{}, driver.NewTransportError("unio: event stream closed before completion"))
				return false
			}
			if ev.RunID != st.runID {
				continue
			}
			switch ev.Type {
			case driver.EventOutput:
				st.cur = st.accumulate(ev.Item)
				return true
			case driver.EventCompleted:
				st.acc.SessionID = st.sid()
				st.acc.FinishReason = FinishReason(ev.Result.FinishReason)
				st.acc.Usage = ev.Result.Usage
				st.acc.Text = string(st.textBuf)
				st.acc.Thinking = string(st.thinkBuf)
				st.finish(st.acc, nil)
				return false
			case driver.EventFailed:
				st.acc.Text = string(st.textBuf)
				st.acc.Thinking = string(st.thinkBuf)
				st.finish(st.acc, failedToError(ev))
				return false
			}
		case <-st.ctx.Done():
			st.finish(Result{}, st.ctx.Err())
			return false
		}
	}
}

// Event returns the item surfaced by the most recent Next()==true.
func (st *Stream) Event() Event { return st.cur }

// Result drains the turn to completion (if not already) and returns the final
// outcome. Safe to call without iterating — that is the one-shot path.
func (st *Stream) Result() (Result, error) {
	for st.Next() {
	}
	return st.res, st.resErr
}

// accumulate records an item into the running Result and returns its facade view.
func (st *Stream) accumulate(item driver.AgentEventItem) Event {
	switch item.Kind {
	case driver.ItemText:
		st.textBuf = append(st.textBuf, item.Text...)
	case driver.ItemThinking:
		st.thinkBuf = append(st.thinkBuf, item.Text...)
	case driver.ItemToolCall:
		st.acc.ToolCalls = append(st.acc.ToolCalls, ToolCall{Name: item.Tool, Input: item.ToolInput})
	}
	return Event{
		Kind:      EventKind(item.Kind),
		Text:      item.Text,
		Tool:      item.Tool,
		ToolInput: item.ToolInput,
	}
}

func (st *Stream) finish(res Result, err error) {
	if st.done {
		return
	}
	st.done = true
	st.res = res
	st.resErr = err
	if st.onDone != nil {
		st.onDone()
	}
}

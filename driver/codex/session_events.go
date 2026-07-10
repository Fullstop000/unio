package codex

import "github.com/Fullstop000/unio/driver"

// onEvent handles a notification/delta routed to this session by the reader. It
// maps codex app-server events onto unio AgentEvents.
func (s *session) onEvent(ev AppServerEvent) {
	run := s.currentRun()

	switch ev.Type {
	case EvTurnStarted:
		if ev.TurnID != "" {
			s.turnID.Store(&ev.TurnID)
		}

	case EvAgentMsgDelta:
		s.emit(run, driver.AgentEventItem{Kind: driver.ItemText, Text: ev.Text})

	case EvReasoningDelta:
		s.emit(run, driver.AgentEventItem{Kind: driver.ItemThinking, Text: ev.Text})

	case EvCommandDelta:
		s.emit(run, driver.AgentEventItem{Kind: driver.ItemToolResult, Text: ev.Text})

	case EvItemCompleted:
		s.onItemCompleted(run, ev.Item)

	case EvTokenUsage:
		s.pendingUsage.Store(&ev.Usage)

	case EvCommandApproval:
		s.setBlocked(ev, driver.BlockedToolApproval, "Command requires approval")

	case EvFileChangeApproval:
		s.setBlocked(ev, driver.BlockedPermission, "File change requires approval")

	case EvTurnCompleted:
		s.finishTurn(run, ev)
	}
}

// onItemCompleted emits tool-call items for command/mcp items. agentMessage
// items are already streamed via deltas, so we don't re-emit their text.
func (s *session) onItemCompleted(run driver.RunID, item Item) {
	switch item.Kind {
	case ItemCommandExecution:
		s.emit(run, driver.AgentEventItem{
			Kind:      driver.ItemToolCall,
			Tool:      "shell",
			ToolInput: map[string]any{"command": item.Command, "cwd": item.Cwd},
		})
	case ItemMcpToolCall:
		s.emit(run, driver.AgentEventItem{
			Kind:      driver.ItemToolCall,
			Tool:      item.Server + "/" + item.Tool,
			ToolInput: item.Arguments,
		})
	}
}

// finishTurn emits TurnEnd + Completed/Failed with any accumulated usage.
func (s *session) finishTurn(run driver.RunID, ev AppServerEvent) {
	defer s.finishTurnDone()
	threadID := s.SessionID()
	s.emit(run, driver.AgentEventItem{Kind: driver.ItemTurnEnd})

	switch ev.TurnStatus {
	case TurnFailedStatus:
		s.bus.Emit(driver.FailedEvent(s.key, threadID, run, driver.NewRuntimeReportedError(ev.TurnFailMsg)))
	default:
		finish := driver.FinishNatural
		if ev.TurnStatus == TurnInterrupted {
			finish = driver.FinishCancelled
		}
		result := driver.RunResult{FinishReason: finish}
		if u := s.pendingUsage.Load(); u != nil {
			result.Usage = map[string]driver.TokenUsage{
				modelKey(s.spec.Model): {
					InputTokens:     u.InputTokens,
					OutputTokens:    u.OutputTokens,
					CacheReadTokens: u.CachedInputTokens,
				},
			}
		}
		s.bus.Emit(driver.CompletedEvent(s.key, threadID, run, result))
	}
	s.pendingUsage.Store(nil)
	s.curRun.Store(ptr(""))
	s.turnID.Store(ptr(""))
	s.setState(driver.ProcessState{Phase: driver.PhaseActive, SessionID: threadID})
}

// onTransportClosed is called when the shared child's stdout closes; fail any
// in-flight run.
func (s *session) onTransportClosed() {
	s.transportClosed.Store(true)
	run := s.currentRun()
	if run != "" {
		s.bus.Emit(driver.CompletedEvent(s.key, s.SessionID(), run, driver.RunResult{FinishReason: driver.FinishTransportClosed}))
		s.curRun.Store(ptr(""))
	}
	s.setState(driver.ProcessState{Phase: driver.PhaseClosed, SessionID: s.SessionID()})
}

func (s *session) emit(run driver.RunID, item driver.AgentEventItem) {
	s.bus.Emit(driver.OutputEvent(s.key, s.SessionID(), run, item))
}

func ptr[T any](v T) *T { return &v }

func modelKey(model string) string {
	if model == "" {
		return "codex"
	}
	return model
}

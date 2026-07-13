package acp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Fullstop000/unio/driver"
)

type session struct {
	proc   *process
	key    driver.SessionKey
	spec   driver.AgentSpec
	resume driver.SessionID
	bus    *driver.EventBus

	opMu sync.Mutex
	mu   sync.Mutex

	closed     bool
	firstTurn  bool
	active     *promptTurn
	permission *pendingPermission
	toolCalls  []pendingToolCall

	sessionID atomic.Pointer[string]
	state     atomic.Pointer[driver.ProcessState]
}

type promptTurn struct {
	runID       driver.RunID
	done        chan struct{}
	interrupted bool
}

type pendingPermission struct {
	id      json.RawMessage
	reason  driver.BlockedReason
	options map[string]struct{}
}

type pendingToolCall struct {
	id    string
	name  string
	input any
}

func newSession(proc *process, key driver.SessionKey, spec driver.AgentSpec, resume driver.SessionID, bus *driver.EventBus) *session {
	s := &session{proc: proc, key: key, spec: spec, resume: resume, bus: bus}
	s.state.Store(&driver.ProcessState{Phase: driver.PhaseIdle})
	return s
}

func (s *session) Key() driver.SessionKey { return s.key }

func (s *session) SessionID() driver.SessionID {
	if id := s.sessionID.Load(); id != nil {
		return *id
	}
	return ""
}

func (s *session) ProcessState() driver.ProcessState {
	if state := s.state.Load(); state != nil {
		return *state
	}
	return driver.ProcessState{Phase: driver.PhaseIdle}
}

func (s *session) setState(state driver.ProcessState) {
	s.state.Store(&state)
	s.bus.Emit(driver.LifecycleEvent(s.key, state))
}

func (s *session) Run(ctx context.Context, initPrompt *driver.PromptReq) error {
	s.opMu.Lock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.opMu.Unlock()
		return driver.NewInvalidStateError("acp: session is closed")
	}
	s.mu.Unlock()
	s.setState(driver.ProcessState{Phase: driver.PhaseStarting})
	if err := s.proc.ensureStarted(ctx); err != nil {
		s.opMu.Unlock()
		return err
	}

	params := map[string]any{"cwd": s.spec.Cwd, "mcpServers": []any{}}
	method := "session/new"
	if s.resume != "" {
		params["sessionId"] = s.resume
		switch {
		case s.proc.caps.Resume:
			method = "session/resume"
		case s.proc.caps.LoadSession:
			method = "session/load"
		default:
			s.opMu.Unlock()
			return driver.NewUnsupportedError("acp: runtime cannot resume sessions")
		}
	}
	result, err := s.proc.call(ctx, method, params)
	if err != nil {
		s.opMu.Unlock()
		if s.resume != "" && strings.Contains(strings.ToLower(err.Error()), "not found") {
			return driver.NewSessionNotFoundError(s.resume)
		}
		return err
	}
	var response struct {
		SessionID string `json:"sessionId"`
	}
	if len(result) != 0 && string(result) != "null" {
		if err := json.Unmarshal(result, &response); err != nil {
			s.opMu.Unlock()
			return driver.NewProtocolError("acp: invalid " + method + " response: " + err.Error())
		}
	}
	if response.SessionID == "" {
		response.SessionID = s.resume
	}
	if response.SessionID == "" {
		s.opMu.Unlock()
		return driver.NewProtocolError("acp: session/new returned no sessionId")
	}
	id := response.SessionID
	if s.spec.Model != "" && s.proc.cfg.modelConfig != "" {
		if _, err := s.proc.call(ctx, "session/set_config_option", map[string]any{
			"sessionId": id,
			"configId":  s.proc.cfg.modelConfig,
			"value":     s.spec.Model,
		}); err != nil {
			s.opMu.Unlock()
			return err
		}
	}
	s.sessionID.Store(&id)
	s.proc.registerSession(id, s)
	s.bus.Emit(driver.SessionAttachedEvent(s.key, id))
	s.setState(driver.ProcessState{Phase: driver.PhaseActive, SessionID: id})
	s.opMu.Unlock()

	if initPrompt != nil {
		_, err = s.Prompt(ctx, *initPrompt)
	}
	return err
}

func (s *session) Prompt(ctx context.Context, req driver.PromptReq) (driver.RunID, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return "", driver.NewInvalidStateError("acp: session is closed")
	}
	if s.active != nil || s.permission != nil {
		s.mu.Unlock()
		return "", driver.NewInvalidStateError("acp: session already has an active prompt")
	}
	text := req.Text
	if !s.firstTurn && s.spec.SystemPrompt != "" {
		text = s.spec.SystemPrompt + "\n\n" + text
	}
	runID := driver.NewRunID()
	turn := &promptTurn{runID: runID, done: make(chan struct{})}
	s.active = turn
	s.toolCalls = nil
	s.mu.Unlock()
	s.setState(driver.ProcessState{Phase: driver.PhasePromptInFlight, SessionID: s.SessionID(), RunID: runID})

	_, response, err := s.proc.request("session/prompt", map[string]any{
		"sessionId": s.SessionID(),
		"prompt":    []any{map[string]any{"type": "text", "text": text}},
	})
	if err != nil {
		s.mu.Lock()
		if s.active == turn {
			s.active = nil
			s.permission = nil
		}
		s.mu.Unlock()
		s.setState(driver.ProcessState{Phase: driver.PhaseActive, SessionID: s.SessionID()})
		return "", err
	}
	s.mu.Lock()
	s.firstTurn = true
	s.mu.Unlock()
	go s.awaitPrompt(turn, response)
	return runID, nil
}

func (s *session) awaitPrompt(turn *promptTurn, response <-chan rpcResponse) {
	select {
	case reply := <-response:
		s.finishPrompt(turn, reply)
	case <-s.proc.closed:
		s.onTransportClosed()
	}
}

func (s *session) finishPrompt(turn *promptTurn, response rpcResponse) {
	s.mu.Lock()
	if s.active != turn {
		s.mu.Unlock()
		return
	}
	runID := turn.runID
	interrupted := turn.interrupted
	calls := s.drainToolCallsLocked()
	s.active = nil
	s.permission = nil
	s.mu.Unlock()

	s.emitToolCalls(runID, calls)
	s.bus.Emit(driver.OutputEvent(s.key, s.SessionID(), runID, driver.AgentEventItem{Kind: driver.ItemTurnEnd}))
	if response.err != nil && !interrupted {
		s.bus.Emit(driver.FailedEvent(s.key, s.SessionID(), runID, driver.NewProtocolError("acp session/prompt: "+errorMessage(response.err))))
	} else {
		finish := driver.FinishNatural
		promptResult := parsePromptResult(response.result)
		if interrupted || promptResult.StopReason == "cancelled" {
			finish = driver.FinishCancelled
		}
		result := driver.RunResult{FinishReason: finish}
		if promptResult.Usage != nil {
			model := s.spec.Model
			if model == "" {
				model = s.proc.cfg.name
			}
			result.Usage = map[string]driver.TokenUsage{model: *promptResult.Usage}
		}
		s.bus.Emit(driver.CompletedEvent(s.key, s.SessionID(), runID, result))
	}
	s.setState(driver.ProcessState{Phase: driver.PhaseActive, SessionID: s.SessionID()})
	close(turn.done)
}

type parsedPromptResult struct {
	StopReason string
	Usage      *driver.TokenUsage
}

func parsePromptResult(result json.RawMessage) parsedPromptResult {
	var response struct {
		StopReason string `json:"stopReason"`
		Usage      *struct {
			InputTokens       int64 `json:"inputTokens"`
			OutputTokens      int64 `json:"outputTokens"`
			CachedReadTokens  int64 `json:"cachedReadTokens"`
			CachedWriteTokens int64 `json:"cachedWriteTokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(result, &response)
	parsed := parsedPromptResult{StopReason: response.StopReason}
	if response.Usage != nil {
		parsed.Usage = &driver.TokenUsage{
			InputTokens: response.Usage.InputTokens, OutputTokens: response.Usage.OutputTokens,
			CacheReadTokens:  response.Usage.CachedReadTokens,
			CacheWriteTokens: response.Usage.CachedWriteTokens,
		}
	}
	return parsed
}

func (s *session) Continue(ctx context.Context, input string) (driver.RunID, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.Lock()
	permission := s.permission
	turn := s.active
	if permission == nil || turn == nil {
		s.mu.Unlock()
		return "", driver.NewInvalidStateError("acp: session is not blocked")
	}
	if _, ok := permission.options[input]; len(permission.options) > 0 && !ok {
		s.mu.Unlock()
		return "", driver.NewInvalidStateError("acp: invalid permission response")
	}
	oldRunID := turn.runID
	runID := driver.NewRunID()
	turn.runID = runID
	s.permission = nil
	s.mu.Unlock()
	s.setState(driver.ProcessState{Phase: driver.PhasePromptInFlight, SessionID: s.SessionID(), RunID: runID})

	payload, err := marshalPermissionResponse(permission.id, "selected", input)
	if err == nil {
		err = s.proc.write(payload)
	}
	if err != nil {
		s.mu.Lock()
		turn.runID = oldRunID
		s.permission = permission
		s.mu.Unlock()
		s.setState(driver.ProcessState{Phase: driver.PhaseBlocked, SessionID: s.SessionID(), RunID: oldRunID})
		return "", err
	}
	return runID, nil
}

func (s *session) Interrupt(ctx context.Context) error {
	s.opMu.Lock()
	s.mu.Lock()
	turn := s.active
	permission := s.permission
	if turn == nil {
		s.mu.Unlock()
		s.opMu.Unlock()
		return nil
	}
	turn.interrupted = true
	s.permission = nil
	s.mu.Unlock()

	if permission != nil {
		payload, err := marshalPermissionResponse(permission.id, "cancelled", "")
		if err != nil {
			s.opMu.Unlock()
			return err
		}
		if err := s.proc.write(payload); err != nil {
			s.opMu.Unlock()
			return err
		}
	}
	if err := s.proc.notify("session/cancel", map[string]any{"sessionId": s.SessionID()}); err != nil {
		s.opMu.Unlock()
		return err
	}
	s.opMu.Unlock()

	select {
	case <-turn.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.proc.closed:
		return driver.NewTransportError(s.proc.closedMessage("ACP runtime closed during interrupt"))
	}
}

func (s *session) Close(ctx context.Context) error {
	interruptErr := s.Interrupt(ctx)
	if s.proc.dead.Load() {
		interruptErr = nil
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	id := s.SessionID()
	var closeErr error
	if id != "" && s.proc.caps.Close && !s.proc.dead.Load() {
		_, closeErr = s.proc.call(ctx, "session/close", map[string]any{"sessionId": id})
	}
	if id != "" {
		s.proc.unregisterSession(id)
	}
	s.proc.release(s)
	s.setState(driver.ProcessState{Phase: driver.PhaseClosed, SessionID: id})
	s.bus.Close()
	return errors.Join(interruptErr, closeErr)
}

func (s *session) onPermission(id, params json.RawMessage) {
	var request struct {
		ToolCall struct {
			Title string `json:"title"`
		} `json:"toolCall"`
		Options []struct {
			Kind     string `json:"kind"`
			Name     string `json:"name"`
			OptionID string `json:"optionId"`
		} `json:"options"`
	}
	if json.Unmarshal(params, &request) != nil {
		return
	}
	reason := driver.BlockedReason{Kind: driver.BlockedToolApproval, Message: request.ToolCall.Title}
	valid := make(map[string]struct{}, len(request.Options))
	for _, option := range request.Options {
		if option.OptionID == "" {
			continue
		}
		label := option.Name
		if label == "" {
			label = option.Kind
		}
		reason.Options = append(reason.Options, driver.BlockOption{Value: option.OptionID, Label: label})
		valid[option.OptionID] = struct{}{}
	}
	s.mu.Lock()
	turn := s.active
	if turn == nil || s.permission != nil {
		s.mu.Unlock()
		payload, err := marshalPermissionResponse(id, "cancelled", "")
		if err == nil {
			_ = s.proc.write(payload)
		}
		return
	}
	s.permission = &pendingPermission{id: append(json.RawMessage(nil), id...), reason: reason, options: valid}
	runID := turn.runID
	s.mu.Unlock()
	s.bus.Emit(driver.BlockedEvent(s.key, s.SessionID(), runID, reason))
	s.setState(driver.ProcessState{Phase: driver.PhaseBlocked, SessionID: s.SessionID(), RunID: runID})
}

func (s *session) onUpdate(raw json.RawMessage) {
	var update map[string]json.RawMessage
	if json.Unmarshal(raw, &update) != nil {
		return
	}
	kind := firstString(update, "sessionUpdate", "kind", "type")
	s.mu.Lock()
	turn := s.active
	if turn == nil {
		s.mu.Unlock()
		return
	}
	runID := turn.runID
	s.mu.Unlock()

	switch kind {
	case "agent_message_chunk", "agentMessageChunk":
		if text := updateText(update); text != "" {
			s.bus.Emit(driver.OutputEvent(s.key, s.SessionID(), runID, driver.AgentEventItem{Kind: driver.ItemText, Text: text}))
		}
	case "agent_thought_chunk", "agentThoughtChunk":
		if text := updateText(update); text != "" {
			s.bus.Emit(driver.OutputEvent(s.key, s.SessionID(), runID, driver.AgentEventItem{Kind: driver.ItemThinking, Text: text}))
		}
	case "tool_call", "toolCall":
		var input any
		for _, field := range []string{"rawInput", "args", "input"} {
			if rawInput := update[field]; len(rawInput) != 0 {
				_ = json.Unmarshal(rawInput, &input)
				break
			}
		}
		s.mu.Lock()
		s.toolCalls = append(s.toolCalls, pendingToolCall{
			id: firstString(update, "toolCallId"), name: firstString(update, "toolName", "title"), input: input,
		})
		s.mu.Unlock()
	case "tool_call_update", "toolCallUpdate":
		s.onToolCallUpdate(runID, update)
	}
}

func (s *session) onToolCallUpdate(runID driver.RunID, update map[string]json.RawMessage) {
	id := firstString(update, "toolCallId")
	s.mu.Lock()
	for index := len(s.toolCalls) - 1; index >= 0; index-- {
		if s.toolCalls[index].id == id || (id == "" && s.toolCalls[index].id == "") {
			if rawInput := firstRaw(update, "rawInput", "args", "input"); len(rawInput) != 0 {
				_ = json.Unmarshal(rawInput, &s.toolCalls[index].input)
			}
			break
		}
	}
	status := firstString(update, "status")
	var calls []pendingToolCall
	if status == "completed" || status == "failed" {
		calls = s.takeToolCallLocked(id)
	}
	s.mu.Unlock()
	s.emitToolCalls(runID, calls)
	if text := toolResultText(update["content"]); text != "" {
		s.bus.Emit(driver.OutputEvent(s.key, s.SessionID(), runID, driver.AgentEventItem{Kind: driver.ItemToolResult, Text: text}))
	}
}

func (s *session) emitToolCalls(runID driver.RunID, calls []pendingToolCall) {
	for _, call := range calls {
		s.bus.Emit(driver.OutputEvent(s.key, s.SessionID(), runID, driver.AgentEventItem{
			Kind: driver.ItemToolCall, Tool: call.name, ToolInput: call.input,
		}))
	}
}

func (s *session) drainToolCallsLocked() []pendingToolCall {
	calls := s.toolCalls
	s.toolCalls = nil
	return calls
}

func (s *session) takeToolCallLocked(id string) []pendingToolCall {
	for index := len(s.toolCalls) - 1; index >= 0; index-- {
		if s.toolCalls[index].id == id || (id == "" && s.toolCalls[index].id == "") {
			call := s.toolCalls[index]
			s.toolCalls = append(s.toolCalls[:index], s.toolCalls[index+1:]...)
			return []pendingToolCall{call}
		}
	}
	return nil
}

func (s *session) onTransportClosed() {
	s.mu.Lock()
	turn := s.active
	if turn != nil {
		s.active = nil
		s.permission = nil
	}
	s.mu.Unlock()
	if turn != nil {
		runID := turn.runID
		s.bus.Emit(driver.FailedEvent(s.key, s.SessionID(), runID, driver.NewTransportError(s.proc.closedMessage("ACP runtime closed"))))
		close(turn.done)
	}
	s.setState(driver.ProcessState{Phase: driver.PhaseClosed, SessionID: s.SessionID()})
	s.bus.Close()
}

func firstString(values map[string]json.RawMessage, fields ...string) string {
	for _, field := range fields {
		var value string
		if json.Unmarshal(values[field], &value) == nil && value != "" {
			return value
		}
	}
	return ""
}

func firstRaw(values map[string]json.RawMessage, fields ...string) json.RawMessage {
	for _, field := range fields {
		if len(values[field]) != 0 && string(values[field]) != "null" {
			return values[field]
		}
	}
	return nil
}

func updateText(update map[string]json.RawMessage) string {
	if text := firstString(update, "chunk", "text"); text != "" {
		return text
	}
	var content struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(update["content"], &content)
	return content.Text
}

func toolResultText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, block := range blocks {
		if block.Content.Text != "" {
			parts = append(parts, block.Content.Text)
		} else if block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

var _ driver.Session = (*session)(nil)

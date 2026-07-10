package claude

import (
	"encoding/json"
	"os"
)

// mergeEnv returns the current process env with the spec's extra vars appended
// (later entries win, matching exec.Cmd semantics).
func mergeEnv(extra []string) []string {
	if len(extra) == 0 {
		return os.Environ()
	}
	base := os.Environ()
	out := make([]string, 0, len(base)+len(extra))
	out = append(out, base...)
	out = append(out, extra...)
	return out
}

// toolCallAccumulator coalesces Claude's streamed tool-call fragments into whole
// tool calls. A tool_use block arrives as: ToolUseStart (id+name) → zero or more
// InputJsonDelta (partial_json chunks) → ToolUseStop. We buffer per content
// block index and emit the finished call on stop, so callers never see a
// half-built tool call (the SPEC coalescing contract).
type toolCallAccumulator struct {
	byIndex map[int]*pendingToolCall
}

type pendingToolCall struct {
	name    string
	id      string
	jsonBuf string
}

func newToolCallAccumulator() *toolCallAccumulator {
	return &toolCallAccumulator{byIndex: make(map[int]*pendingToolCall)}
}

// start records a new tool_use block at index.
func (a *toolCallAccumulator) start(index int, id, name string) {
	a.byIndex[index] = &pendingToolCall{name: name, id: id}
}

// appendJSON appends a partial_json chunk to the block at index.
func (a *toolCallAccumulator) appendJSON(index int, partial string) {
	if p, ok := a.byIndex[index]; ok {
		p.jsonBuf += partial
	}
}

// finish removes and returns the coalesced call at index, decoding its input.
// Returns ("", nil, false) if index is not a tool_use block (e.g. text/thinking
// content_block_stop, which shares the stop event).
func (a *toolCallAccumulator) finish(index int) (name string, input any, ok bool) {
	p, exists := a.byIndex[index]
	if !exists {
		return "", nil, false
	}
	delete(a.byIndex, index)

	var decoded any
	if p.jsonBuf != "" {
		if err := json.Unmarshal([]byte(p.jsonBuf), &decoded); err != nil {
			// Keep the raw string as input rather than dropping the call.
			decoded = p.jsonBuf
		}
	} else {
		decoded = map[string]any{}
	}
	return p.name, decoded, true
}

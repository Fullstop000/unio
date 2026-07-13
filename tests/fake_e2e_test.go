package tests

import (
	"context"
	"testing"
	"time"

	"github.com/Fullstop000/unio/driver"
	"github.com/Fullstop000/unio/driver/fake"
)

// fakeHarness wires the in-memory fake driver into the shared lifecycle
// scenario. It scripts both turns so the E2E asserts real Output + Completed
// events flow through the whole open→run→prompt→resume→close path.
func fakeHarness(t *testing.T) Harness {
	return Harness{
		Name: "fake",
		NewDriver: func(t *testing.T, ctx context.Context, spec driver.AgentSpec) driver.Driver {
			fd := fake.New(ctx, spec)
			// Queue one scripted turn for the initial session and one for its
			// resumed handle.
			turn := fake.Script{
				Items: []driver.AgentEventItem{
					{Kind: driver.ItemThinking, Text: "planning"},
					{Kind: driver.ItemToolCall, Tool: "read_file", ToolInput: map[string]any{"path": "main.go"}},
					{Kind: driver.ItemToolResult, Text: "package main"},
					{Kind: driver.ItemText, Text: "done"},
				},
				Result: driver.RunResult{
					FinishReason: driver.FinishNatural,
					Usage:        map[string]driver.TokenUsage{"fake-model": {InputTokens: 12, OutputTokens: 8, CostUSD: 0.001}},
					DurationMs:   5,
				},
			}
			// Two open() calls happen (initial + resume), each replays from
			// scriptIdx 0, so two turns of script cover both.
			fd.ScriptNextSession(turn)
			fd.ScriptNextSession(turn)
			return fd
		},
		FirstPrompt:  "refactor the auth module",
		SecondPrompt: "now add tests",
		Timeout:      3 * time.Second,
	}
}

func TestE2E_Fake_FullLifecycle(t *testing.T) {
	RunLifecycle(t, fakeHarness(t))
}

func TestE2E_Fake_Cancel(t *testing.T) {
	CancelScenario(t, fakeHarness(t))
}

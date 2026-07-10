// Command multi drives several DIFFERENT agents (Claude + Codex, each speaking a
// different wire protocol) through the same unio facade, fires one prompt at
// each, and reports token usage across all of them — none of which requires the
// caller to know any agent's protocol.
//
// unio deliberately does NOT build in multi-session aggregation (a caller can do
// it in a few lines), so this example shows exactly that: one goroutine per
// agent forwarding into a shared results channel.
//
// Usage:
//
//	go run ./examples/multi
package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/Fullstop000/unio"
	"github.com/Fullstop000/unio/driver"
)

type outcome struct {
	agent unio.Agent
	res   unio.Result
	err   error
}

// prompt is fired at every installed agent.
const prompt = "Reply with exactly one word: hello"

func main() {
	agents := []unio.Agent{unio.Claude, unio.Codex}
	ctx := context.Background()

	// Fan out: one Run per installed agent, results collected on a channel.
	results := make(chan outcome, len(agents))
	var wg sync.WaitGroup
	for _, a := range agents {
		if !unio.Installed(a) {
			fmt.Printf("skip %s (not installed)\n", a)
			continue
		}
		wg.Add(1)
		go func(a unio.Agent) {
			defer wg.Done()
			res, err := unio.Run(ctx, a, prompt)
			results <- outcome{agent: a, res: res, err: err}
		}(a)
	}
	go func() { wg.Wait(); close(results) }()

	// Fan in: print each answer, accumulate cross-agent token usage.
	totals := map[string]driver.TokenUsage{}
	var totalCost float64
	for o := range results {
		if o.err != nil {
			fmt.Printf("[%s] error: %v\n", o.agent, o.err)
			continue
		}
		fmt.Printf("[%s] %s (finish=%s)\n", o.agent, o.res.Text, o.res.FinishReason)
		for model, u := range o.res.Usage {
			acc := totals[model]
			acc.Add(u)
			totals[model] = acc
			totalCost += u.CostUSD
		}
	}

	fmt.Println("\n=== token usage across all agents ===")
	for model, u := range totals {
		fmt.Printf("  %-24s in=%-7d out=%-6d cost=$%.4f\n", model, u.InputTokens, u.OutputTokens, u.CostUSD)
	}
	fmt.Printf("  total cost: $%.4f\n", totalCost)
}

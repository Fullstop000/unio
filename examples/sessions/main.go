// Command sessions lists Codex history, resumes one returned handle, and
// reads its persisted data. Runtime ordering is not stable; production callers
// should select or sort sessions explicitly.
//
// Requires an installed and authenticated Codex CLI and consumes tokens.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/Fullstop000/unio"
)

func main() {
	ctx := context.Background()
	agent, err := unio.New(ctx, unio.Codex)
	if err != nil {
		log.Fatal(err)
	}
	defer agent.Close()

	sessions, err := agent.ListSessions()
	if err != nil {
		log.Fatal(err)
	}
	for _, info := range sessions {
		fmt.Printf("%s %q messages=%d cwd=%s\n", info.ID, info.Title, info.MessageCount, info.Cwd)
	}
	if len(sessions) == 0 {
		return
	}

	session, err := agent.GetSession(sessions[0].ID)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("selected session: id=%s state=%s\n", session.ID(), session.State())
	result, err := session.Run(unio.Message("Reply with exactly one word: resumed"))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("reply: %s\n", result.Text)

	raw, err := session.Raw()
	if err != nil {
		log.Printf("raw session data unavailable: %v", err)
	} else {
		fmt.Printf("raw: format=%s bytes=%d\n", raw.Format, len(raw.Data))
	}
	stats, err := session.TokenStatistics()
	if err != nil {
		log.Printf("session statistics unavailable: %v", err)
	} else {
		fmt.Printf("session tokens: in=%d out=%d\n", stats.InputTokens, stats.OutputTokens)
	}
}

// Command sessions lists Codex history and obtains a maintained session handle.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/Fullstop000/unio"
)

func main() {
	ctx := context.Background()
	agent, err := unio.New(unio.Codex)
	if err != nil {
		log.Fatal(err)
	}
	defer agent.Close()

	sessions, err := agent.ListSessions(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, info := range sessions {
		fmt.Printf("%s %q messages=%d cwd=%s\n", info.ID, info.Title, info.MessageCount, info.Cwd)
	}
	if len(sessions) == 0 {
		return
	}

	session, err := agent.GetSession(ctx, sessions[0].ID)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("selected session: id=%s state=%s\n", session.ID(), session.State())
}

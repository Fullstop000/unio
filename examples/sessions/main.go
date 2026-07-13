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
}

// Command acp runs one turn through the shared ACP v1 adapter.
//
//	go run ./examples/acp
//
// Requires an installed and authenticated Kimi CLI. Replace unio.Kimi with
// unio.TraeX or unio.OpenCode to use another ACP-native runtime.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/Fullstop000/unio"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agent, err := unio.New(ctx, unio.Kimi)
	if err != nil {
		log.Fatal(err)
	}
	defer agent.Close()
	session, err := agent.NewSession()
	if err != nil {
		log.Fatal(err)
	}
	result, err := session.Run("Reply with exactly one word: hello")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.Text)
}

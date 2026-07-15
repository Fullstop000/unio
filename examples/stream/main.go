// Command stream demonstrates live events, asynchronous interruption, and
// draining the terminal Result before reusing a Session.
//
//	go run ./examples/stream
//
// Requires an installed and authenticated Codex CLI and consumes tokens.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/Fullstop000/unio"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agent, err := unio.New(ctx, unio.Codex)
	if err != nil {
		log.Fatal(err)
	}
	defer agent.Close()

	session, err := agent.NewSession()
	if err != nil {
		log.Fatal(err)
	}
	stream, err := session.Stream("Count upward slowly, one number per line")
	if err != nil {
		log.Fatal(err)
	}

	interruptErr := make(chan error, 1)
	go func() {
		time.Sleep(2 * time.Second)
		interruptErr <- session.Interrupt()
	}()

	for stream.Next() {
		event := stream.Event()
		switch event.Kind {
		case unio.KindText, unio.KindThinking, unio.KindToolResult:
			fmt.Print(event.Text)
		case unio.KindToolCall:
			fmt.Printf("\ntool=%s input=%v\n", event.Tool, event.ToolInput)
		}
	}
	result, err := stream.Result()
	if err != nil {
		log.Fatal(err)
	}
	if err := <-interruptErr; err != nil {
		log.Fatal(err)
	}
	fmt.Printf("\ninterrupted=%t state=%s\n", result.Interrupted, session.State())
}

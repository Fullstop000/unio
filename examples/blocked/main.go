// Command blocked demonstrates runtime-advertised approval options and repeated
// blocking. The selected Codex configuration may complete without blocking.
//
//	go run ./examples/blocked
//
// Requires an installed and authenticated Codex CLI and consumes tokens.
package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings"

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

	result, err := session.Run("Run pwd and report the result")
	reader := bufio.NewReader(os.Stdin)
	for err == nil && result.Blocked != nil {
		fmt.Printf("blocked: %s\n", result.Blocked.Message)
		for _, option := range result.Blocked.Options {
			fmt.Printf("  %s: %s\n", option.Value, option.Label)
		}
		fmt.Print("response: ")
		input, readErr := reader.ReadString('\n')
		if readErr != nil {
			log.Fatal(readErr)
		}
		result, err = session.Continue(strings.TrimSpace(input))
	}
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.Text)
}

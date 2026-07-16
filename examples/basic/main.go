// Command basic is the smallest runnable unio example: create an Agent and
// Session, send one prompt, and print the answer plus token usage.
//
//	go run ./examples/basic
//
// Requires the `claude` CLI installed and logged in.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/Fullstop000/unio"
)

func main() {
	agent, err := unio.New(context.Background(), unio.Claude)
	if err != nil {
		log.Fatal(err)
	}
	defer agent.Close()
	session, err := agent.NewSession()
	if err != nil {
		log.Fatal(err)
	}
	res, err := session.Run(unio.Message("Reply with exactly one word: ping"))
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("answer:", res.Text)
	fmt.Printf("session: %s\n", res.SessionID)
	for model, u := range res.Usage {
		fmt.Printf("usage[%s]: in=%d out=%d cost=$%.4f\n", model, u.InputTokens, u.OutputTokens, u.CostUSD)
	}
}

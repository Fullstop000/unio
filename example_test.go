package unio_test

import (
	"context"
	"fmt"

	"github.com/Fullstop000/unio"
)

func Example() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agent, err := unio.New(ctx, unio.Codex, unio.WithCwd("/path/to/repo"))
	if err != nil {
		return
	}
	defer agent.Close()

	session, err := agent.NewSession()
	if err != nil {
		return
	}
	result, err := session.Run(unio.Message("Explain this repository"))
	if err != nil {
		return
	}
	fmt.Println(result.Text)
}

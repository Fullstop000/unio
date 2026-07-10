// Command sessions shows two host-side session operations:
// list runtime-stored sessions and get a live session by caller-owned key.
//
//	go run ./examples/sessions
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/Fullstop000/unio/driver"
	"github.com/Fullstop000/unio/driver/fake"
)

func main() {
	ctx := context.Background()
	d := fake.New()
	d.SetStoredSessions([]driver.StoredSessionMeta{
		{
			SessionID:    "claude-2026-07-10-001",
			Title:        "refactor auth",
			StartedAt:    time.Date(2026, 7, 10, 10, 0, 0, 0, time.Local),
			MessageCount: 8,
			Cwd:          "/repo/api",
		},
		{
			SessionID:    "codex-2026-07-10-002",
			Title:        "fix flaky test",
			StartedAt:    time.Date(2026, 7, 10, 11, 30, 0, 0, time.Local),
			MessageCount: 5,
			Cwd:          "/repo/web",
		},
	})

	stored, err := d.ListSessions(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("stored sessions:")
	for _, s := range stored {
		fmt.Printf("- %s %q messages=%d cwd=%s\n", s.SessionID, s.Title, s.MessageCount, s.Cwd)
	}

	live := map[driver.SessionKey]driver.Session{}
	key := driver.SessionKey("workspace-a/session-1")
	att, err := d.OpenSession(ctx, key, driver.AgentSpec{Cwd: "/repo/api"}, driver.OpenParams{})
	if err != nil {
		log.Fatal(err)
	}
	defer att.Session.Close(ctx)
	if err := att.Session.Run(ctx, nil); err != nil {
		log.Fatal(err)
	}
	live[key] = att.Session

	s, ok := live[key]
	if !ok {
		log.Fatalf("session %q not found", key)
	}
	fmt.Printf("live session by key: key=%s runtime_id=%s phase=%s\n",
		s.Key(), s.SessionID(), s.ProcessState().Phase)
}

package codex

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Fullstop000/unio/driver"
)

func TestListSessionsReadsCodexHistory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "2026", "07", "10")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "" +
		`{"type":"session_meta","timestamp":"2026-07-10T02:00:00Z","payload":{"id":"thread-1","cwd":"/repo/api","timestamp":"2026-07-10T02:00:00Z"}}` + "\n" +
		`{"type":"event_msg","timestamp":"2026-07-10T02:00:01Z","payload":{"type":"user_message","message":"Refactor auth"}}` + "\n" +
		`{"type":"response_item","timestamp":"2026-07-10T02:00:02Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Inspecting"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "rollout-thread-1.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	other := `{"type":"session_meta","timestamp":"2026-07-10T01:00:00Z","payload":{"id":"thread-2","cwd":"/repo/other","timestamp":"2026-07-10T01:00:00Z"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "rollout-thread-2.jsonl"), []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}
	original := codexSessionsRoot
	codexSessionsRoot = func() (string, error) { return root, nil }
	t.Cleanup(func() { codexSessionsRoot = original })

	got, err := New().ListSessions(context.Background(), driver.ListSessionsParams{Cwd: "/repo/api"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SessionID != "thread-1" || got[0].Title != "Refactor auth" || got[0].Cwd != "/repo/api" || got[0].MessageCount != 2 || got[0].UpdatedAt.IsZero() {
		t.Fatalf("sessions = %+v", got)
	}
	all, err := New().ListSessions(context.Background(), driver.ListSessionsParams{})
	if err != nil || len(all) != 2 {
		t.Fatalf("all sessions = %+v, err = %v", all, err)
	}
}

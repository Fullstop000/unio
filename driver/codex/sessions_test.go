package codex

import (
	"context"
	"os"
	"path/filepath"
	"testing"
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
	original := codexSessionsRoot
	codexSessionsRoot = func() (string, error) { return root, nil }
	t.Cleanup(func() { codexSessionsRoot = original })

	got, err := New().ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SessionID != "thread-1" || got[0].Title != "Refactor auth" || got[0].Cwd != "/repo/api" || got[0].MessageCount != 2 {
		t.Fatalf("sessions = %+v", got)
	}
}

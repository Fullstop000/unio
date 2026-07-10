package claude

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestListSessionsReadsClaudeHistory(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "-repo-api")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "" +
		`{"type":"last-prompt","sessionId":"session-1","lastPrompt":"Refactor auth"}` + "\n" +
		`{"type":"user","sessionId":"session-1","cwd":"/repo/api","message":{"role":"user","content":"Refactor auth"}}` + "\n" +
		`{"type":"assistant","sessionId":"session-1","message":{"role":"assistant","content":"I will inspect it."}}` + "\n"
	if err := os.WriteFile(filepath.Join(project, "session-1.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	original := claudeSessionsRoot
	claudeSessionsRoot = func() (string, error) { return root, nil }
	t.Cleanup(func() { claudeSessionsRoot = original })

	got, err := New().ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SessionID != "session-1" || got[0].Title != "Refactor auth" || got[0].Cwd != "/repo/api" || got[0].MessageCount != 2 {
		t.Fatalf("sessions = %+v", got)
	}
}

package claude

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Fullstop000/unio/driver"
)

func TestRawSessionDataAndTokenStatistics(t *testing.T) {
	root := t.TempDir()
	previous := claudeSessionsRoot
	claudeSessionsRoot = func() (string, error) { return root, nil }
	t.Cleanup(func() { claudeSessionsRoot = previous })

	data := []byte("" +
		`{"type":"user","sessionId":"session-id"}` + "\n" +
		`{"type":"assistant","message":{"id":"message-1","usage":{"input_tokens":10,"output_tokens":2,"cache_read_input_tokens":6,"cache_creation_input_tokens":1}}}` + "\n" +
		`{"type":"assistant","message":{"id":"message-1","usage":{"input_tokens":10,"output_tokens":2,"cache_read_input_tokens":6,"cache_creation_input_tokens":1}}}` + "\n" +
		`{"type":"assistant","message":{"id":"message-2","usage":{"input_tokens":20,"output_tokens":3,"cache_read_input_tokens":8,"cache_creation_input_tokens":4}}}` + "\n")
	path := filepath.Join(root, "workspace", "session-id.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	d := New()
	dataSource := d.NewSessionData(context.Background(), driver.AgentSpec{}, "session-id")
	raw, err := dataSource.Raw()
	if err != nil || raw.Format != driver.SessionDataJSONL || string(raw.Data) != string(data) {
		t.Fatalf("raw = %+v, error = %v", raw, err)
	}
	got, err := dataSource.TokenStatistics()
	if err != nil {
		t.Fatal(err)
	}
	if got.InputTokens != 49 || got.OutputTokens != 5 || got.CacheReadTokens != 14 || got.CacheWriteTokens != 5 {
		t.Fatalf("statistics = %+v", got)
	}

	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := dataSource.TokenStatistics(); !errors.Is(err, driver.NewProtocolError("")) {
		t.Fatalf("incomplete statistics error = %v; want protocol", err)
	}
}

func TestRawSessionDataPreservesCancellation(t *testing.T) {
	root := t.TempDir()
	previous := claudeSessionsRoot
	claudeSessionsRoot = func() (string, error) { return root, nil }
	t.Cleanup(func() { claudeSessionsRoot = previous })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New().NewSessionData(ctx, driver.AgentSpec{}, "session-id").Raw()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v; want context.Canceled", err)
	}
}

package codex

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
	previous := codexSessionsRoot
	codexSessionsRoot = func() (string, error) { return root, nil }
	t.Cleanup(func() { codexSessionsRoot = previous })

	data := []byte("" +
		`{"type":"session_meta","payload":{"id":"session-id"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":10,"cached_input_tokens":6,"output_tokens":2}}}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":30,"cached_input_tokens":18,"output_tokens":5}}}}` + "\n")
	path := filepath.Join(root, "2026", "07", "13", "rollout-2026-07-13T00-00-00-session-id.jsonl")
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
	if got.InputTokens != 30 || got.OutputTokens != 5 || got.CacheReadTokens != 18 {
		t.Fatalf("statistics = %+v", got)
	}
}

func TestRawSessionDataPreservesCancellation(t *testing.T) {
	root := t.TempDir()
	previous := codexSessionsRoot
	codexSessionsRoot = func() (string, error) { return root, nil }
	t.Cleanup(func() { codexSessionsRoot = previous })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New().NewSessionData(ctx, driver.AgentSpec{}, "session-id").Raw()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v; want context.Canceled", err)
	}
}

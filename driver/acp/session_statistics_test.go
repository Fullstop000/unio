package acp

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Fullstop000/unio/driver"
)

func TestParseKimiStatisticsCurrentFormat(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"usage.record","model":"m","usage":{"inputOther":10,"output":3,"inputCacheRead":20,"inputCacheCreation":4},"usageScope":"turn"}`,
		`{"type":"context.append_loop_event","event":{"type":"step.end","usage":{"inputOther":999}}}`,
		`{"type":"usage.record","model":"m","usage":{"inputOther":2,"output":1,"inputCacheRead":5,"inputCacheCreation":0},"usageScope":"turn"}`,
	}, "\n")
	got, err := parseKimiStatistics(context.Background(), strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if got.InputTokens != 41 || got.OutputTokens != 4 || got.CacheReadTokens != 25 || got.CacheWriteTokens != 4 {
		t.Fatalf("statistics = %+v", got)
	}
}

func TestParseKimiStatisticsLegacyFormat(t *testing.T) {
	input := strings.Join([]string{
		`{"message":{"type":"StatusUpdate","payload":{"token_usage":{"input_other":7,"output":2,"input_cache_read":11,"input_cache_creation":3}}}}`,
		`{"message":{"type":"TurnEnd","payload":{}}}`,
	}, "\n")
	got, err := parseKimiStatistics(context.Background(), strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if got.InputTokens != 21 || got.OutputTokens != 2 || got.CacheReadTokens != 11 || got.CacheWriteTokens != 3 {
		t.Fatalf("statistics = %+v", got)
	}
}

func TestParseTraeXStatistics(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":20,"cached_input_tokens":12,"cache_creation_input_tokens":3,"output_tokens":4}}}}`,
		`{"type":"event_msg","payload":{"type":"task_complete"}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":9,"cached_input_tokens":5,"cache_creation_input_tokens":1,"output_tokens":2}}}}`,
	}, "\n")
	got, err := parseTraeXStatistics(context.Background(), strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if got.InputTokens != 29 || got.OutputTokens != 6 || got.CacheReadTokens != 17 || got.CacheWriteTokens != 4 {
		t.Fatalf("statistics = %+v", got)
	}
}

func TestStatisticsParsersHonorCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := parseKimiStatistics(ctx, strings.NewReader(`{"type":"usage.record"}`)); err != context.Canceled {
		t.Fatalf("Kimi error = %v", err)
	}
	if _, err := parseTraeXStatistics(ctx, strings.NewReader(`{"type":"event_msg"}`)); err != context.Canceled {
		t.Fatalf("TraeX error = %v", err)
	}
}

func TestStatisticsParsersRejectIncompleteSessionData(t *testing.T) {
	tests := []struct {
		name  string
		parse func(context.Context, io.Reader) (driver.TokenUsage, error)
		data  string
	}{
		{
			name:  "Kimi partial JSONL record",
			parse: parseKimiStatistics,
			data:  `{"type":"usage.record"`,
		},
		{
			name:  "Kimi unfinished step",
			parse: parseKimiStatistics,
			data:  `{"type":"context.append_loop_event","event":{"type":"step.begin","uuid":"step","turnId":1}}` + "\n",
		},
		{
			name:  "TraeX partial JSONL record",
			parse: parseTraeXStatistics,
			data:  `{"type":"event_msg"`,
		},
		{
			name:  "TraeX unfinished task",
			parse: parseTraeXStatistics,
			data:  `{"type":"event_msg","payload":{"type":"task_started"}}` + "\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.parse(context.Background(), strings.NewReader(tt.data))
			if !errors.Is(err, driver.NewProtocolError("")) {
				t.Fatalf("error = %v; want protocol", err)
			}
		})
	}
}

func TestFindKimiRawDataFromLegacySessionPath(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".kimi", "sessions", "workspace", "abc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := `{"message":{"type":"StatusUpdate","payload":{"token_usage":{"input_other":7,"output":2,"input_cache_read":11,"input_cache_creation":3}}}}`
	if err := os.WriteFile(filepath.Join(dir, "wire.jsonl"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	path, err := findKimiWire(context.Background(), home, "ses_abc")
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	got, err := parseKimiStatistics(context.Background(), file)
	if err != nil || got.InputTokens != 21 || got.OutputTokens != 2 {
		t.Fatalf("statistics = %+v, error = %v", got, err)
	}
}

func TestFindTraeXRawDataFromCurrentSessionPath(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".trae", "cli", "sessions", "2026", "07", "13")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := `{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":20,"cached_input_tokens":12,"output_tokens":4}}}}`
	path := filepath.Join(dir, "rollout-2026-07-13T00-00-00-session-id.jsonl")
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	gotPath, err := findTraeXRollout(context.Background(), home, "session-id")
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(gotPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	got, err := parseTraeXStatistics(context.Background(), file)
	if err != nil || got.InputTokens != 20 || got.OutputTokens != 4 {
		t.Fatalf("statistics = %+v, error = %v", got, err)
	}
}

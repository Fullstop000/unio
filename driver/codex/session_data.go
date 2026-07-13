package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Fullstop000/unio/driver"
)

func (s *session) Raw() (driver.RawSessionData, error) {
	return readCodexSessionData(s.ctx, s.dataSessionID())
}

func (s *session) TokenStatistics() (driver.TokenUsage, error) {
	raw, err := s.Raw()
	if err != nil {
		return driver.TokenUsage{}, err
	}
	return parseCodexTokenStatistics(s.ctx, raw)
}

func (s *session) dataSessionID() driver.SessionID {
	if sessionID := s.SessionID(); sessionID != "" {
		return sessionID
	}
	return s.resume
}

func readCodexSessionData(ctx context.Context, sessionID driver.SessionID) (driver.RawSessionData, error) {
	path, err := findCodexSession(ctx, string(sessionID))
	if err != nil {
		return driver.RawSessionData{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return driver.RawSessionData{}, driver.NewProtocolError("codex: read session data: " + err.Error())
	}
	if err := ctx.Err(); err != nil {
		return driver.RawSessionData{}, err
	}
	return driver.RawSessionData{Format: driver.SessionDataJSONL, Data: data}, nil
}

func findCodexSession(ctx context.Context, sessionID string) (string, error) {
	root, err := codexSessionsRoot()
	if err != nil {
		return "", driver.NewTransportError("codex: locate session history: " + err.Error())
	}
	var found string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), "-"+sessionID+".jsonl") {
			found = path
			return fs.SkipAll
		}
		return nil
	})
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return "", driver.NewTransportError("codex: read session history: " + err.Error())
	}
	if found == "" {
		return "", driver.NewSessionNotFoundError(driver.SessionID(sessionID))
	}
	return found, nil
}

func parseCodexTokenStatistics(ctx context.Context, raw driver.RawSessionData) (driver.TokenUsage, error) {
	if raw.Format != driver.SessionDataJSONL {
		return driver.TokenUsage{}, driver.NewProtocolError("codex: unsupported session data format: " + string(raw.Format))
	}
	var total driver.TokenUsage
	pendingTasks := 0
	scanner := bufio.NewScanner(bytes.NewReader(raw.Data))
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return driver.TokenUsage{}, err
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var record struct {
			Type    string `json:"type"`
			Payload struct {
				Type string `json:"type"`
				Info struct {
					Total struct {
						InputTokens       int64 `json:"input_tokens"`
						OutputTokens      int64 `json:"output_tokens"`
						CachedInputTokens int64 `json:"cached_input_tokens"`
					} `json:"total_token_usage"`
				} `json:"info"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			return driver.TokenUsage{}, driver.NewProtocolError("codex: parse session data: invalid JSONL record")
		}
		if record.Type != "event_msg" {
			continue
		}
		switch record.Payload.Type {
		case "task_started":
			pendingTasks++
		case "task_complete", "turn_aborted":
			if pendingTasks > 0 {
				pendingTasks--
			}
		case "token_count":
			usage := record.Payload.Info.Total
			total = driver.TokenUsage{
				InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
				CacheReadTokens: usage.CachedInputTokens,
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return driver.TokenUsage{}, driver.NewProtocolError("codex: parse session data: " + err.Error())
	}
	if pendingTasks != 0 {
		return driver.TokenUsage{}, driver.NewProtocolError("codex: latest task is not fully persisted yet")
	}
	return total, nil
}

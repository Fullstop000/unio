package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Fullstop000/unio/driver"
)

func (d *Driver) NewSessionData(ctx context.Context, _ driver.AgentSpec, sessionID driver.SessionID) *driver.SessionData {
	return driver.NewSessionData(
		ctx,
		func(ctx context.Context) (driver.RawSessionData, error) {
			return readClaudeSessionData(ctx, sessionID)
		},
		parseClaudeTokenStatistics,
	)
}

func readClaudeSessionData(ctx context.Context, sessionID driver.SessionID) (driver.RawSessionData, error) {
	path, err := findClaudeSession(ctx, string(sessionID))
	if err != nil {
		return driver.RawSessionData{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return driver.RawSessionData{}, driver.NewProtocolError("claude: read session data: " + err.Error())
	}
	if err := ctx.Err(); err != nil {
		return driver.RawSessionData{}, err
	}
	return driver.RawSessionData{Format: driver.SessionDataJSONL, Data: data}, nil
}

func findClaudeSession(ctx context.Context, sessionID string) (string, error) {
	root, err := claudeSessionsRoot()
	if err != nil {
		return "", driver.NewTransportError("claude: locate session history: " + err.Error())
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
		if !entry.IsDir() && entry.Name() == sessionID+".jsonl" {
			found = path
			return fs.SkipAll
		}
		return nil
	})
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return "", driver.NewTransportError("claude: read session history: " + err.Error())
	}
	if found == "" {
		return "", driver.NewSessionNotFoundError(driver.SessionID(sessionID))
	}
	return found, nil
}

func parseClaudeTokenStatistics(ctx context.Context, raw driver.RawSessionData) (driver.TokenUsage, error) {
	if raw.Format != driver.SessionDataJSONL {
		return driver.TokenUsage{}, driver.NewProtocolError("claude: unsupported session data format: " + string(raw.Format))
	}
	var total driver.TokenUsage
	byMessage := make(map[string]driver.TokenUsage)
	pendingResponse := false
	scanner := bufio.NewScanner(bytes.NewReader(raw.Data))
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return driver.TokenUsage{}, err
		}
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var record struct {
			Type        string `json:"type"`
			IsSidechain bool   `json:"isSidechain"`
			Message     struct {
				ID    string `json:"id"`
				Usage struct {
					InputTokens      int64 `json:"input_tokens"`
					OutputTokens     int64 `json:"output_tokens"`
					CacheReadTokens  int64 `json:"cache_read_input_tokens"`
					CacheWriteTokens int64 `json:"cache_creation_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			return driver.TokenUsage{}, driver.NewProtocolError("claude: parse session data: invalid JSONL record")
		}
		if record.Type == "user" && !record.IsSidechain {
			pendingResponse = true
			continue
		}
		if record.Type != "assistant" {
			continue
		}
		if !record.IsSidechain {
			pendingResponse = false
		}
		usage := driver.TokenUsage{
			InputTokens:     record.Message.Usage.InputTokens + record.Message.Usage.CacheReadTokens + record.Message.Usage.CacheWriteTokens,
			OutputTokens:    record.Message.Usage.OutputTokens,
			CacheReadTokens: record.Message.Usage.CacheReadTokens, CacheWriteTokens: record.Message.Usage.CacheWriteTokens,
		}
		if record.Message.ID == "" {
			total.Add(usage)
		} else {
			byMessage[record.Message.ID] = usage
		}
	}
	if err := scanner.Err(); err != nil {
		return driver.TokenUsage{}, driver.NewProtocolError("claude: parse session data: " + err.Error())
	}
	if pendingResponse {
		return driver.TokenUsage{}, driver.NewProtocolError("claude: latest turn is not fully persisted yet")
	}
	for _, usage := range byMessage {
		total.Add(usage)
	}
	return total, nil
}

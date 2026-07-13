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

func (d *Driver) NewSessionData(ctx context.Context, _ driver.AgentSpec, sessionID driver.SessionID) driver.SessionData {
	return driver.NewSessionData(
		ctx,
		func(ctx context.Context) (driver.RawSessionData, error) {
			return readCodexSessionData(ctx, sessionID)
		},
		parseCodexTokenStatistics,
	)
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
	scanner := bufio.NewScanner(bytes.NewReader(raw.Data))
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return driver.TokenUsage{}, err
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
		if json.Unmarshal(scanner.Bytes(), &record) != nil || record.Type != "event_msg" || record.Payload.Type != "token_count" {
			continue
		}
		usage := record.Payload.Info.Total
		total = driver.TokenUsage{
			InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
			CacheReadTokens: usage.CachedInputTokens,
		}
	}
	if err := scanner.Err(); err != nil {
		return driver.TokenUsage{}, driver.NewProtocolError("codex: parse session data: " + err.Error())
	}
	return total, nil
}

package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/Fullstop000/unio/driver"
)

var codexSessionsRoot = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex", "sessions"), nil
}

func listStoredSessions(ctx context.Context, cwd string) ([]driver.StoredSessionMeta, error) {
	root, err := codexSessionsRoot()
	if err != nil {
		return nil, driver.NewTransportError("codex: locate session history: " + err.Error())
	}
	var out []driver.StoredSessionMeta
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
		if entry.IsDir() || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		meta, ok := readCodexSession(path)
		if ok && (cwd == "" || filepath.Clean(meta.Cwd) == filepath.Clean(cwd)) {
			out = append(out, meta)
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, driver.NewTransportError("codex: read session history: " + err.Error())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].SessionID < out[j].SessionID
		}
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	return out, nil
}

func readCodexSession(path string) (driver.StoredSessionMeta, bool) {
	file, err := os.Open(path)
	if err != nil {
		return driver.StoredSessionMeta{}, false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return driver.StoredSessionMeta{}, false
	}
	meta := driver.StoredSessionMeta{StartedAt: info.ModTime(), UpdatedAt: info.ModTime()}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		var record struct {
			Type      string          `json:"type"`
			Timestamp string          `json:"timestamp"`
			Payload   json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			continue
		}
		switch record.Type {
		case "session_meta":
			var payload struct {
				ID        string `json:"id"`
				Cwd       string `json:"cwd"`
				Timestamp string `json:"timestamp"`
			}
			if json.Unmarshal(record.Payload, &payload) == nil {
				meta.SessionID = payload.ID
				meta.Cwd = payload.Cwd
				if started, err := time.Parse(time.RFC3339Nano, payload.Timestamp); err == nil {
					meta.StartedAt = started
				}
			}
		case "event_msg":
			var payload struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			}
			if json.Unmarshal(record.Payload, &payload) != nil {
				continue
			}
			if payload.Type == "user_message" || payload.Type == "agent_message" {
				meta.MessageCount++
			}
			if payload.Type == "user_message" && meta.Title == "" {
				meta.Title = truncateRunes(payload.Message, 120)
			}
		case "response_item":
			var payload struct {
				Type string `json:"type"`
				Role string `json:"role"`
			}
			if json.Unmarshal(record.Payload, &payload) == nil && payload.Type == "message" && (payload.Role == "user" || payload.Role == "assistant") {
				meta.MessageCount++
			}
		}
	}
	return meta, meta.SessionID != ""
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

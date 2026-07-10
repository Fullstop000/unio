package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Fullstop000/unio/driver"
)

var claudeSessionsRoot = func() (string, error) {
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

func listStoredSessions(ctx context.Context, cwd string) ([]driver.StoredSessionMeta, error) {
	root, err := claudeSessionsRoot()
	if err != nil {
		return nil, driver.NewTransportError("claude: locate session history: " + err.Error())
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
		meta, ok := readClaudeSession(path)
		if ok && (cwd == "" || filepath.Clean(meta.Cwd) == filepath.Clean(cwd)) {
			out = append(out, meta)
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, driver.NewTransportError("claude: read session history: " + err.Error())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].SessionID < out[j].SessionID
		}
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	return out, nil
}

func readClaudeSession(path string) (driver.StoredSessionMeta, bool) {
	file, err := os.Open(path)
	if err != nil {
		return driver.StoredSessionMeta{}, false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return driver.StoredSessionMeta{}, false
	}
	meta := driver.StoredSessionMeta{
		SessionID: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
		StartedAt: info.ModTime(),
		UpdatedAt: info.ModTime(),
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		var record struct {
			Type       string `json:"type"`
			SessionID  string `json:"sessionId"`
			Cwd        string `json:"cwd"`
			LastPrompt string `json:"lastPrompt"`
		}
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			continue
		}
		if record.SessionID != "" {
			meta.SessionID = record.SessionID
		}
		if record.Cwd != "" {
			meta.Cwd = record.Cwd
		}
		if record.LastPrompt != "" {
			meta.Title = record.LastPrompt
		}
		if record.Type == "user" || record.Type == "assistant" {
			meta.MessageCount++
		}
	}
	return meta, meta.SessionID != ""
}

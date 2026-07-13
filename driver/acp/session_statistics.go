package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Fullstop000/unio/driver"
)

func (s *session) Raw() (driver.RawSessionData, error) {
	return readACPSessionData(s.ctx, s.proc.cfg.name, s.dataSessionID())
}

func (s *session) TokenStatistics() (driver.TokenUsage, error) {
	raw, err := s.Raw()
	if err != nil {
		return driver.TokenUsage{}, err
	}
	return parseACPSessionTokenStatistics(s.ctx, s.proc.cfg.name, raw)
}

func (s *session) dataSessionID() driver.SessionID {
	if sessionID := s.SessionID(); sessionID != "" {
		return sessionID
	}
	return s.resume
}

func readACPSessionData(ctx context.Context, runtime string, sessionID driver.SessionID) (driver.RawSessionData, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return driver.RawSessionData{}, driver.NewProtocolError("acp: locate home directory: " + err.Error())
	}
	var path string
	switch runtime {
	case string(Kimi):
		path, err = findKimiWire(ctx, home, string(sessionID))
	case string(TraeX):
		path, err = findTraeXRollout(ctx, home, string(sessionID))
	default:
		return driver.RawSessionData{}, driver.NewUnsupportedError("acp: raw session data are not supported by " + runtime)
	}
	if err != nil {
		return driver.RawSessionData{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return driver.RawSessionData{}, driver.NewProtocolError("acp: read session data: " + err.Error())
	}
	if err := ctx.Err(); err != nil {
		return driver.RawSessionData{}, err
	}
	return driver.RawSessionData{Format: driver.SessionDataJSONL, Data: data}, nil
}

func parseACPSessionTokenStatistics(ctx context.Context, runtime string, raw driver.RawSessionData) (driver.TokenUsage, error) {
	if raw.Format != driver.SessionDataJSONL {
		return driver.TokenUsage{}, driver.NewProtocolError("acp: unsupported session data format: " + string(raw.Format))
	}
	switch runtime {
	case string(Kimi):
		return parseKimiStatistics(ctx, bytes.NewReader(raw.Data))
	case string(TraeX):
		return parseTraeXStatistics(ctx, bytes.NewReader(raw.Data))
	default:
		return driver.TokenUsage{}, driver.NewUnsupportedError("acp: session token statistics are not supported by " + runtime)
	}
}

func findKimiWire(ctx context.Context, home, sessionID string) (string, error) {
	indexPath := filepath.Join(home, ".kimi-code", "session_index.jsonl")
	if file, err := os.Open(indexPath); err == nil {
		defer file.Close()
		var matched string
		scanner := sessionScanner(file)
		for scanner.Scan() {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			var item struct {
				SessionID  string `json:"sessionId"`
				SessionDir string `json:"sessionDir"`
			}
			if json.Unmarshal(scanner.Bytes(), &item) == nil && item.SessionID == sessionID {
				matched = filepath.Join(item.SessionDir, "agents", "main", "wire.jsonl")
			}
		}
		if matched != "" {
			if _, err := os.Stat(matched); err == nil {
				return matched, nil
			}
		}
	}

	want := strings.TrimPrefix(sessionID, "ses_")
	for _, root := range []string{filepath.Join(home, ".kimi-code", "sessions"), filepath.Join(home, ".kimi", "sessions")} {
		var found string
		walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || found != "" || !entry.IsDir() {
				return nil
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			name := entry.Name()
			if name != sessionID && name != want {
				return nil
			}
			for _, candidate := range []string{
				filepath.Join(path, "agents", "main", "wire.jsonl"),
				filepath.Join(path, "wire.jsonl"),
			} {
				if _, statErr := os.Stat(candidate); statErr == nil {
					found = candidate
					return fs.SkipAll
				}
			}
			return nil
		})
		if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
			return "", walkErr
		}
		if found != "" {
			return found, nil
		}
	}
	return "", driver.NewSessionNotFoundError(driver.SessionID(sessionID))
}

func parseKimiStatistics(ctx context.Context, input io.Reader) (driver.TokenUsage, error) {
	var total driver.TokenUsage
	openSteps := make(map[string]map[string]struct{})
	scanner := sessionScanner(input)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return driver.TokenUsage{}, err
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var record struct {
			Type       string          `json:"type"`
			UsageScope string          `json:"usageScope"`
			Usage      kimiUsage       `json:"usage"`
			TurnID     json.RawMessage `json:"turnId"`
			Event      struct {
				Type   string          `json:"type"`
				TurnID json.RawMessage `json:"turnId"`
				UUID   string          `json:"uuid"`
			} `json:"event"`
			Message struct {
				Type    string `json:"type"`
				Payload struct {
					Usage kimiUsage `json:"token_usage"`
				} `json:"payload"`
			} `json:"message"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			return driver.TokenUsage{}, driver.NewProtocolError("acp: parse Kimi session data: invalid JSONL record")
		}
		switch {
		case record.Type == "context.append_loop_event" && record.Event.Type == "step.begin" && record.Event.UUID != "":
			turnID := string(bytes.TrimSpace(record.Event.TurnID))
			if openSteps[turnID] == nil {
				openSteps[turnID] = make(map[string]struct{})
			}
			openSteps[turnID][record.Event.UUID] = struct{}{}
		case record.Type == "context.append_loop_event" && record.Event.Type == "step.end" && record.Event.UUID != "":
			for turnID, steps := range openSteps {
				delete(steps, record.Event.UUID)
				if len(steps) == 0 {
					delete(openSteps, turnID)
				}
			}
		case record.Type == "turn.cancel":
			delete(openSteps, string(bytes.TrimSpace(record.TurnID)))
		}
		switch {
		case record.Type == "usage.record" && record.UsageScope == "turn":
			total.Add(record.Usage.tokenUsage())
		case record.Message.Type == "StatusUpdate":
			total.Add(record.Message.Payload.Usage.tokenUsage())
		}
	}
	if err := scanner.Err(); err != nil {
		return driver.TokenUsage{}, driver.NewProtocolError("acp: parse Kimi session data: " + err.Error())
	}
	if len(openSteps) != 0 {
		return driver.TokenUsage{}, driver.NewProtocolError("acp: latest Kimi step is not fully persisted yet")
	}
	return total, nil
}

type kimiUsage struct {
	InputOther         int64 `json:"inputOther"`
	Output             int64 `json:"output"`
	InputCacheRead     int64 `json:"inputCacheRead"`
	InputCacheCreation int64 `json:"inputCacheCreation"`
}

func (u *kimiUsage) UnmarshalJSON(data []byte) error {
	type camel kimiUsage
	var values struct {
		camel
		InputOtherSnake         int64 `json:"input_other"`
		InputCacheReadSnake     int64 `json:"input_cache_read"`
		InputCacheCreationSnake int64 `json:"input_cache_creation"`
	}
	if err := json.Unmarshal(data, &values); err != nil {
		return err
	}
	*u = kimiUsage(values.camel)
	if u.InputOther == 0 {
		u.InputOther = values.InputOtherSnake
	}
	if u.InputCacheRead == 0 {
		u.InputCacheRead = values.InputCacheReadSnake
	}
	if u.InputCacheCreation == 0 {
		u.InputCacheCreation = values.InputCacheCreationSnake
	}
	return nil
}

func (u kimiUsage) tokenUsage() driver.TokenUsage {
	return driver.TokenUsage{
		InputTokens:  u.InputOther + u.InputCacheRead + u.InputCacheCreation,
		OutputTokens: u.Output, CacheReadTokens: u.InputCacheRead,
		CacheWriteTokens: u.InputCacheCreation,
	}
}

func findTraeXRollout(ctx context.Context, home, sessionID string) (string, error) {
	var rollout string
	errFound := errors.New("found")
	for _, root := range []string{
		filepath.Join(home, ".trae", "cli", "sessions"),
		filepath.Join(home, ".trae", "sessions"),
	} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), "-"+sessionID+".jsonl") {
				rollout = path
				return errFound
			}
			return nil
		})
		if err != nil && !errors.Is(err, errFound) {
			return "", err
		}
		if rollout != "" {
			break
		}
	}
	if rollout == "" {
		return "", driver.NewSessionNotFoundError(driver.SessionID(sessionID))
	}
	return rollout, nil
}

func parseTraeXStatistics(ctx context.Context, input io.Reader) (driver.TokenUsage, error) {
	var total driver.TokenUsage
	pendingTasks := 0
	scanner := sessionScanner(input)
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
					Last struct {
						InputTokens       int64 `json:"input_tokens"`
						OutputTokens      int64 `json:"output_tokens"`
						CachedInputTokens int64 `json:"cached_input_tokens"`
						CacheCreation     int64 `json:"cache_creation_input_tokens"`
					} `json:"last_token_usage"`
				} `json:"info"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			return driver.TokenUsage{}, driver.NewProtocolError("acp: parse TraeX session data: invalid JSONL record")
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
			last := record.Payload.Info.Last
			total.Add(driver.TokenUsage{
				InputTokens: last.InputTokens, OutputTokens: last.OutputTokens,
				CacheReadTokens: last.CachedInputTokens, CacheWriteTokens: last.CacheCreation,
			})
		}
	}
	if err := scanner.Err(); err != nil {
		return driver.TokenUsage{}, driver.NewProtocolError("acp: parse TraeX session data: " + err.Error())
	}
	if pendingTasks != 0 {
		return driver.TokenUsage{}, driver.NewProtocolError("acp: latest task is not fully persisted yet")
	}
	return total, nil
}

func sessionScanner(input io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	return scanner
}

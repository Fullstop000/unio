package supportmatrix

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type Capability string

type Level uint8

const (
	Unsupported Level = iota
	Supported
	Partial
)

const (
	CapNew                Capability = "new"
	CapWithCwd            Capability = "with_cwd"
	CapWithModel          Capability = "with_model"
	CapWithSystemPrompt   Capability = "with_system_prompt"
	CapWithExtraArgs      Capability = "with_extra_args"
	CapWithEnv            Capability = "with_env"
	CapNewSession         Capability = "new_session"
	CapListSessions       Capability = "list_sessions"
	CapSessionsIn         Capability = "sessions_in"
	CapAllSessions        Capability = "all_sessions"
	CapGetSession         Capability = "get_session"
	CapAgentClose         Capability = "agent_close"
	CapSessionID          Capability = "session_id"
	CapSessionState       Capability = "session_state"
	CapSessionRun         Capability = "session_run"
	CapSessionStream      Capability = "session_stream"
	CapSessionInterrupt   Capability = "session_interrupt"
	CapSessionContinue    Capability = "session_continue"
	CapStreamNext         Capability = "stream_next"
	CapStreamEvent        Capability = "stream_event"
	CapStreamResult       Capability = "stream_result"
	CapEventThinking      Capability = "event_thinking"
	CapEventText          Capability = "event_text"
	CapEventToolCall      Capability = "event_tool_call"
	CapEventToolResult    Capability = "event_tool_result"
	CapResultText         Capability = "result_text"
	CapResultThinking     Capability = "result_thinking"
	CapResultToolCalls    Capability = "result_tool_calls"
	CapResultSessionID    Capability = "result_session_id"
	CapResultUsage        Capability = "result_usage"
	CapResultDuration     Capability = "result_duration"
	CapResultInterrupted  Capability = "result_interrupted"
	CapResultBlocked      Capability = "result_blocked"
	CapUsageInput         Capability = "usage_input"
	CapUsageOutput        Capability = "usage_output"
	CapUsageCacheRead     Capability = "usage_cache_read"
	CapUsageCacheWrite    Capability = "usage_cache_write"
	CapUsageCost          Capability = "usage_cost"
	CapSessionInfoID      Capability = "session_info_id"
	CapSessionInfoTitle   Capability = "session_info_title"
	CapSessionInfoCwd     Capability = "session_info_cwd"
	CapSessionInfoStarted Capability = "session_info_started"
	CapSessionInfoUpdated Capability = "session_info_updated"
	CapSessionInfoCount   Capability = "session_info_count"
	CapBlockedUserInput   Capability = "blocked_user_input"
	CapBlockedTool        Capability = "blocked_tool"
	CapBlockedPermission  Capability = "blocked_permission"
	CapBlockedAuth        Capability = "blocked_auth"
	CapBlockedExternal    Capability = "blocked_external"
)

type Profile struct {
	Kind    string
	Label   string
	Support map[Capability]Level
}

type Row struct {
	Capability Capability
	Label      string
	Note       string
}

type Section struct {
	Title       string
	FirstColumn string
	Rows        []Row
}

var sections = []Section{
	{Title: "Configuration", FirstColumn: "Feature", Rows: []Row{
		{CapNew, "`agent.initialize`", "Checks CLI availability only; authentication errors may surface on the first turn"},
		{CapWithCwd, "`agent.configure.working_directory`", ""},
		{CapWithModel, "`agent.configure.model`", "OpenCode selects the model through ACP session configuration"},
		{CapWithSystemPrompt, "`agent.configure.system_prompt`", "ACP agents prepend the system prompt to the first user prompt"},
		{CapWithExtraArgs, "`agent.configure.runtime_arguments`", "Codex app-server arguments are fixed"},
		{CapWithEnv, "`agent.configure.environment`", ""},
	}},
	{Title: "Agent Lifecycle", FirstColumn: "Feature", Rows: []Row{
		{CapNewSession, "`session.create`", ""},
		{CapListSessions, "`session.list`", ""},
		{CapSessionsIn, "`session.list.workspace`", "Filters sessions by working directory"},
		{CapAllSessions, "`session.list.all`", "Removes the working-directory filter"},
		{CapGetSession, "`session.retrieve`", ""},
		{CapAgentClose, "`agent.close`", ""},
	}},
	{Title: "Session Lifecycle", FirstColumn: "Feature", Rows: []Row{
		{CapSessionID, "`session.identity`", "Empty until the first turn starts for a new session"},
		{CapSessionState, "`session.state`", "Claude does not enter the blocked state"},
		{CapSessionRun, "`turn.run`", ""},
		{CapSessionStream, "`turn.stream`", ""},
		{CapSessionInterrupt, "`turn.interrupt`", "Claude terminates its process and resumes automatically on the next turn"},
		{CapSessionContinue, "`turn.continue`", "Codex supports command and file approvals only; ACP uses runtime-provided option IDs"},
	}},
	{Title: "Stream Consumption", FirstColumn: "Feature", Rows: []Row{
		{CapStreamNext, "`stream.advance`", ""},
		{CapStreamEvent, "`stream.current_event`", ""},
		{CapStreamResult, "`stream.collect_result`", ""},
	}},
	{Title: "Event Types", FirstColumn: "Feature", Rows: []Row{
		{CapEventThinking, "`event.thinking`", "Emission depends on the selected model and runtime configuration"},
		{CapEventText, "`event.text`", ""},
		{CapEventToolCall, "`event.tool_call`", "Codex maps commands to `shell` and MCP tools to `server/tool`"},
		{CapEventToolResult, "`event.tool_result`", "Codex maps command output only, excluding MCP results"},
	}},
	{Title: "Turn Result", FirstColumn: "Feature", Rows: []Row{
		{CapResultText, "`result.text`", ""},
		{CapResultThinking, "`result.thinking`", ""},
		{CapResultToolCalls, "`result.tool_calls`", ""},
		{CapResultSessionID, "`result.session_identity`", ""},
		{CapResultUsage, "`result.usage`", "Absent when the runtime does not report usage"},
		{CapResultDuration, "`result.duration`", "Zero when the runtime does not report duration"},
		{CapResultInterrupted, "`result.interrupted`", ""},
		{CapResultBlocked, "`result.blocked`", "Codex supports tool and file approvals; ACP supports tool approval"},
	}},
	{Title: "Token Usage", FirstColumn: "Feature", Rows: []Row{
		{CapUsageInput, "`usage.input_tokens`", ""},
		{CapUsageOutput, "`usage.output_tokens`", ""},
		{CapUsageCacheRead, "`usage.cache_read_tokens`", ""},
		{CapUsageCacheWrite, "`usage.cache_write_tokens`", ""},
		{CapUsageCost, "`usage.cost`", ""},
	}},
	{Title: "Session Metadata", FirstColumn: "Feature", Rows: []Row{
		{CapSessionInfoID, "`session.metadata.identity`", ""},
		{CapSessionInfoTitle, "`session.metadata.title`", ""},
		{CapSessionInfoCwd, "`session.metadata.working_directory`", ""},
		{CapSessionInfoStarted, "`session.metadata.started_at`", "Claude uses the history file modification time; ACP does not map this field"},
		{CapSessionInfoUpdated, "`session.metadata.updated_at`", ""},
		{CapSessionInfoCount, "`session.metadata.message_count`", "ACP reads runtime `_meta.messageCount`"},
	}},
	{Title: "Blocking Reasons", FirstColumn: "Feature", Rows: []Row{
		{CapBlockedUserInput, "`blocking.user_input`", ""},
		{CapBlockedTool, "`blocking.tool_approval`", ""},
		{CapBlockedPermission, "`blocking.permission`", ""},
		{CapBlockedAuth, "`blocking.authentication`", ""},
		{CapBlockedExternal, "`blocking.external`", ""},
	}},
}

var commonCapabilities = []Capability{
	CapNew, CapWithCwd, CapWithModel, CapWithSystemPrompt, CapWithEnv,
	CapNewSession, CapListSessions, CapSessionsIn, CapAllSessions, CapGetSession, CapAgentClose,
	CapSessionID, CapSessionState, CapSessionRun, CapSessionStream, CapSessionInterrupt,
	CapStreamNext, CapStreamEvent, CapStreamResult,
	CapEventThinking, CapEventText, CapEventToolCall,
	CapResultText, CapResultThinking, CapResultToolCalls, CapResultSessionID, CapResultInterrupted,
	CapSessionInfoID, CapSessionInfoTitle, CapSessionInfoCwd, CapSessionInfoUpdated, CapSessionInfoCount,
}

func baseProfile(kind, label string) Profile {
	p := Profile{Kind: kind, Label: label, Support: make(map[Capability]Level)}
	p.set(Supported, commonCapabilities...)
	return p
}

func (p Profile) set(level Level, capabilities ...Capability) {
	for _, capability := range capabilities {
		p.Support[capability] = level
	}
}

func claudeProfile() Profile {
	p := baseProfile("claude", "Claude Code")
	p.set(Supported, CapWithExtraArgs, CapResultUsage, CapResultDuration,
		CapUsageInput, CapUsageOutput, CapUsageCacheRead, CapUsageCacheWrite, CapUsageCost)
	p.set(Partial, CapSessionInfoStarted)
	p.set(Unsupported, CapSessionContinue, CapEventToolResult, CapResultBlocked,
		CapBlockedUserInput, CapBlockedTool, CapBlockedPermission, CapBlockedAuth, CapBlockedExternal)
	return p
}

func codexProfile() Profile {
	p := baseProfile("codex", "Codex")
	p.set(Partial, CapSessionContinue, CapEventToolResult, CapResultUsage, CapResultBlocked)
	p.set(Supported, CapUsageInput, CapUsageOutput, CapUsageCacheRead,
		CapSessionInfoStarted, CapBlockedTool, CapBlockedPermission)
	p.set(Unsupported, CapWithExtraArgs, CapResultDuration, CapUsageCacheWrite, CapUsageCost,
		CapBlockedUserInput, CapBlockedAuth, CapBlockedExternal)
	return p
}

func acpProfile(kind, label string) Profile {
	p := baseProfile(kind, label)
	p.set(Supported, CapWithExtraArgs, CapSessionContinue, CapEventToolResult, CapResultBlocked, CapBlockedTool)
	p.set(Unsupported, CapResultUsage, CapResultDuration,
		CapUsageInput, CapUsageOutput, CapUsageCacheRead, CapUsageCacheWrite, CapUsageCost,
		CapSessionInfoStarted, CapBlockedUserInput, CapBlockedPermission, CapBlockedAuth, CapBlockedExternal)
	return p
}

func Profiles() []Profile {
	return []Profile{
		claudeProfile(),
		codexProfile(),
		acpProfile("kimi", "Kimi"),
		acpProfile("traex", "TraeX"),
		acpProfile("opencode", "OpenCode"),
	}
}

var canonicalFeaturePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

func Validate(profiles []Profile) error {
	want := make(map[Capability]struct{})
	featureIDs := make(map[string]struct{})
	for _, section := range sections {
		for _, row := range section.Rows {
			if _, duplicate := want[row.Capability]; duplicate {
				return fmt.Errorf("duplicate capability %q", row.Capability)
			}
			want[row.Capability] = struct{}{}
			featureID := strings.Trim(row.Label, "`")
			if row.Label != "`"+featureID+"`" || !canonicalFeaturePattern.MatchString(featureID) {
				return fmt.Errorf("invalid canonical feature identifier %q", row.Label)
			}
			if _, duplicate := featureIDs[featureID]; duplicate {
				return fmt.Errorf("duplicate canonical feature identifier %q", featureID)
			}
			featureIDs[featureID] = struct{}{}
		}
	}
	seenKinds := make(map[string]struct{})
	for _, profile := range profiles {
		if _, duplicate := seenKinds[profile.Kind]; duplicate {
			return fmt.Errorf("duplicate agent kind %q", profile.Kind)
		}
		seenKinds[profile.Kind] = struct{}{}
		for capability := range want {
			if _, ok := profile.Support[capability]; !ok {
				return fmt.Errorf("agent %q is missing capability %q", profile.Kind, capability)
			}
		}
		for capability := range profile.Support {
			if _, ok := want[capability]; !ok {
				return fmt.Errorf("agent %q declares unknown capability %q", profile.Kind, capability)
			}
		}
	}
	return nil
}

func Markdown() ([]byte, error) {
	profiles := Profiles()
	if err := Validate(profiles); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.WriteString("<!-- Code generated by scripts/generate-support-matrix.sh; DO NOT EDIT. -->\n\n")
	out.WriteString("# unio SDK Feature Support Matrix\n\n## Support Overview\n\n")
	writeHeader(&out, "Agent", []string{"Execution", "Session Listing", "Session Resume", "Interruption", "Blocking", "Tool Results", "Usage"}, false)
	overview := []Capability{CapSessionRun, CapListSessions, CapGetSession, CapSessionInterrupt, CapSessionContinue, CapEventToolResult, CapResultUsage}
	for _, profile := range profiles {
		cells := make([]string, 0, len(overview))
		for _, capability := range overview {
			cells = append(cells, overviewCell(profile, capability))
		}
		writeRow(&out, profile.Label, cells, noNote)
	}
	out.WriteString("\n| Marker | Meaning |\n| --- | --- |\n| ✅ | Supported |\n| ⚠️ | Partially supported; see notes |\n| ❌ | Unsupported |\n")

	for _, section := range sections {
		out.WriteString("\n## " + section.Title + "\n\n")
		hasNotes := false
		for _, row := range section.Rows {
			hasNotes = hasNotes || row.Note != ""
		}
		agents := make([]string, 0, len(profiles))
		for _, profile := range profiles {
			agents = append(agents, profile.Label)
		}
		writeHeader(&out, section.FirstColumn, agents, hasNotes)
		for _, row := range section.Rows {
			cells := make([]string, 0, len(profiles))
			for _, profile := range profiles {
				cells = append(cells, marker(profile.Support[row.Capability]))
			}
			writeRow(&out, row.Label, cells, conditionalNote(row.Note, hasNotes))
		}
	}

	out.WriteString("\n## Cross-Language Contracts\n\n")
	kinds := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		kinds = append(kinds, "`"+agentSymbol(profile.Kind)+"`")
	}
	out.WriteString("- Agent kinds: " + strings.Join(kinds, ", ") + "\n")
	out.WriteString("- Session states: `idle`, `running`, `blocked`\n")
	out.WriteString("- Event kinds: `thinking`, `text`, `tool_call`, `tool_result`\n")
	out.WriteString("- Blocking reasons: `user_input`, `tool_approval`, `permission`, `authentication`, `external`\n")
	out.WriteString("- Error kinds: `transport`, `protocol`, `timeout`, `runtime_reported`, `unsupported`, `not_installed`, `invalid_state`, `session_not_found`\n")
	return out.Bytes(), nil
}

func ProfileKinds() []string {
	profiles := Profiles()
	kinds := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		kinds = append(kinds, profile.Kind)
	}
	sort.Strings(kinds)
	return kinds
}

func overviewCell(profile Profile, capability Capability) string {
	level := profile.Support[capability]
	if capability == CapSessionContinue && profile.Kind == "codex" {
		return "⚠️ Approvals only"
	}
	if capability == CapEventToolResult && profile.Kind == "codex" {
		return "⚠️ Command output only"
	}
	if capability == CapResultUsage {
		switch profile.Kind {
		case "claude":
			return "✅ Includes cache writes, cost, and duration"
		case "codex":
			return "⚠️ No cache writes, cost, or duration"
		}
	}
	return marker(level)
}

func marker(level Level) string {
	switch level {
	case Supported:
		return "✅"
	case Partial:
		return "⚠️"
	default:
		return "❌"
	}
}

func agentSymbol(kind string) string {
	switch kind {
	case "claude":
		return "Claude"
	case "codex":
		return "Codex"
	case "kimi":
		return "Kimi"
	case "traex":
		return "TraeX"
	case "opencode":
		return "OpenCode"
	default:
		return kind
	}
}

const noNote = "\x00"

func writeHeader(out *bytes.Buffer, first string, columns []string, notes bool) {
	out.WriteString("| " + first)
	for _, column := range columns {
		out.WriteString(" | " + column)
	}
	if notes {
		out.WriteString(" | Notes")
	}
	out.WriteString(" |\n| ---")
	for range columns {
		out.WriteString(" | :---:")
	}
	if notes {
		out.WriteString(" | ---")
	}
	out.WriteString(" |\n")
}

func writeRow(out *bytes.Buffer, first string, cells []string, note string) {
	out.WriteString("| " + first)
	for _, cell := range cells {
		out.WriteString(" | " + cell)
	}
	if note != noNote {
		out.WriteString(" | " + note)
	}
	out.WriteString(" |\n")
}

func conditionalNote(note string, enabled bool) string {
	if enabled {
		return note
	}
	return noNote
}

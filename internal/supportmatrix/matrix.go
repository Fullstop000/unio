package supportmatrix

import (
	"bytes"
	"fmt"
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
	{Title: "创建与配置", FirstColumn: "API", Rows: []Row{
		{CapNew, "`New(kind, opts...)`", "仅检查 CLI 是否存在；登录错误可能在首次运行时返回"},
		{CapWithCwd, "`WithCwd(dir)`", ""},
		{CapWithModel, "`WithModel(model)`", "OpenCode 通过 ACP session config 设置模型"},
		{CapWithSystemPrompt, "`WithSystemPrompt(prompt)`", "ACP Agent 在首次提示词前拼接 system prompt"},
		{CapWithExtraArgs, "`WithExtraArgs(args...)`", "Codex app-server 启动参数固定"},
		{CapWithEnv, "`WithEnv(env...)`", ""},
	}},
	{Title: "Agent", FirstColumn: "API", Rows: []Row{
		{CapNewSession, "`(*Agent).NewSession(ctx)`", ""},
		{CapListSessions, "`(*Agent).ListSessions(ctx, opts...)`", ""},
		{CapSessionsIn, "`SessionsIn(dir)`", "按工作目录筛选会话"},
		{CapAllSessions, "`AllSessions()`", "取消工作目录筛选"},
		{CapGetSession, "`(*Agent).GetSession(ctx, id)`", ""},
		{CapAgentClose, "`(*Agent).Close()`", ""},
	}},
	{Title: "Session", FirstColumn: "API", Rows: []Row{
		{CapSessionID, "`(*Session).ID()`", "新会话首次运行前为空"},
		{CapSessionState, "`(*Session).State()`", "Claude 不会进入 `Blocked`"},
		{CapSessionRun, "`(*Session).Run(ctx, prompt)`", ""},
		{CapSessionStream, "`(*Session).Stream(ctx, prompt)`", ""},
		{CapSessionInterrupt, "`(*Session).Interrupt(ctx)`", "Claude 终止进程后在下一轮自动恢复"},
		{CapSessionContinue, "`(*Session).Continue(ctx, input)`", "Codex 仅支持命令和文件审批；ACP 使用运行时返回的 option ID"},
	}},
	{Title: "Stream", FirstColumn: "API", Rows: []Row{
		{CapStreamNext, "`(*Stream).Next()`", ""},
		{CapStreamEvent, "`(*Stream).Event()`", ""},
		{CapStreamResult, "`(*Stream).Result()`", ""},
	}},
	{Title: "事件", FirstColumn: "`Event.Kind`", Rows: []Row{
		{CapEventThinking, "`KindThinking`", "是否产生取决于模型和配置"},
		{CapEventText, "`KindText`", ""},
		{CapEventToolCall, "`KindToolCall`", "Codex 命令映射为 `shell`，MCP 工具映射为 `server/tool`"},
		{CapEventToolResult, "`KindToolResult`", "Codex 仅映射命令输出，不包含 MCP 结果"},
	}},
	{Title: "Result", FirstColumn: "字段", Rows: []Row{
		{CapResultText, "`Text`", ""},
		{CapResultThinking, "`Thinking`", ""},
		{CapResultToolCalls, "`ToolCalls`", ""},
		{CapResultSessionID, "`SessionID`", ""},
		{CapResultUsage, "`Usage`", "运行时未上报时为 `nil`"},
		{CapResultDuration, "`DurationMs`", "未上报时为 `0`"},
		{CapResultInterrupted, "`Interrupted`", ""},
		{CapResultBlocked, "`Blocked`", "Codex 仅支持工具和文件审批；ACP 支持工具审批"},
	}},
	{Title: "Usage", FirstColumn: "`driver.TokenUsage` 字段", Rows: []Row{
		{CapUsageInput, "`InputTokens`", ""},
		{CapUsageOutput, "`OutputTokens`", ""},
		{CapUsageCacheRead, "`CacheReadTokens`", ""},
		{CapUsageCacheWrite, "`CacheWriteTokens`", ""},
		{CapUsageCost, "`CostUSD`", ""},
	}},
	{Title: "SessionInfo", FirstColumn: "字段", Rows: []Row{
		{CapSessionInfoID, "`ID`", ""},
		{CapSessionInfoTitle, "`Title`", ""},
		{CapSessionInfoCwd, "`Cwd`", ""},
		{CapSessionInfoStarted, "`StartedAt`", "Claude 使用历史文件修改时间；ACP 未映射该字段"},
		{CapSessionInfoUpdated, "`UpdatedAt`", ""},
		{CapSessionInfoCount, "`MessageCount`", "ACP 从运行时 `_meta.messageCount` 读取"},
	}},
	{Title: "阻塞类型", FirstColumn: "`BlockedKind`", Rows: []Row{
		{CapBlockedUserInput, "`BlockedUserInput`", ""},
		{CapBlockedTool, "`BlockedToolApproval`", ""},
		{CapBlockedPermission, "`BlockedPermission`", ""},
		{CapBlockedAuth, "`BlockedAuthentication`", ""},
		{CapBlockedExternal, "`BlockedExternal`", ""},
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

func Validate(profiles []Profile) error {
	want := make(map[Capability]struct{})
	for _, section := range sections {
		for _, row := range section.Rows {
			if _, duplicate := want[row.Capability]; duplicate {
				return fmt.Errorf("duplicate capability %q", row.Capability)
			}
			want[row.Capability] = struct{}{}
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
	out.WriteString("<!-- Code generated by go generate; DO NOT EDIT. -->\n\n")
	out.WriteString("# unio 一层 API 支持矩阵\n\n## 支持概览\n\n")
	writeHeader(&out, "Agent", []string{"运行/流式", "会话列表", "会话恢复", "中断", "阻塞/继续", "工具结果流", "用量"}, false)
	overview := []Capability{CapSessionRun, CapListSessions, CapGetSession, CapSessionInterrupt, CapSessionContinue, CapEventToolResult, CapResultUsage}
	for _, profile := range profiles {
		cells := make([]string, 0, len(overview))
		for _, capability := range overview {
			cells = append(cells, overviewCell(profile, capability))
		}
		writeRow(&out, profile.Label, cells, noNote)
	}
	out.WriteString("\n| 标记 | 含义 |\n| --- | --- |\n| ✅ | 支持 |\n| ⚠️ | 部分支持，限制见备注 |\n| ❌ | 不支持 |\n")

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

	out.WriteString("\n## 公共契约\n\n")
	kinds := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		kinds = append(kinds, "`"+agentSymbol(profile.Kind)+"`")
	}
	out.WriteString("- Agent：`AgentKind`（" + strings.Join(kinds, "、") + "）\n")
	out.WriteString("- 状态：`SessionState`（`Idle`、`Running`、`Blocked`）\n")
	out.WriteString("- 数据类型：`Event`、`ToolCall`、`Result`、`SessionInfo`、`BlockedReason`、`BlockOption`\n")
	out.WriteString("- 会话筛选：`ListSessionsOption`、`SessionsIn`、`AllSessions`\n")
	out.WriteString("- 哨兵错误：`ErrInvalidState`、`ErrSessionNotFound`\n")
	out.WriteString("- 错误类别：`transport`、`protocol`、`timeout`、`runtime_reported`、`unsupported`、`not_installed`、`invalid_state`、`session_not_found`\n")
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
		return "⚠️ 仅审批"
	}
	if capability == CapEventToolResult && profile.Kind == "codex" {
		return "⚠️ 仅命令输出"
	}
	if capability == CapResultUsage {
		switch profile.Kind {
		case "claude":
			return "✅ 含缓存写入、成本、耗时"
		case "codex":
			return "⚠️ 无缓存写入、成本、耗时"
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
		out.WriteString(" | 备注")
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

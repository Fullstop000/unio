# unio 一层 API 支持矩阵

## 支持概览

| Agent | 运行/流式 | 历史会话/恢复 | 中断 | 阻塞/继续 | 工具结果流 | 用量 |
| --- | :---: | :---: | :---: | :---: | :---: | --- |
| Claude Code | ✅ | ✅ | ✅ | ❌ | ❌ | ✅ 含缓存写入、成本、耗时 |
| Codex | ✅ | ✅ | ✅ | ⚠️ 仅审批 | ⚠️ 仅命令输出 | ⚠️ 无缓存写入、成本、耗时 |

| 标记 | 含义 |
| --- | --- |
| ✅ | 支持 |
| ⚠️ | 部分支持，限制见备注 |
| ❌ | 不支持 |

## 创建与配置

| API | Claude Code | Codex | 备注 |
| --- | :---: | :---: | --- |
| `New(kind, opts...)` | ✅ | ✅ | 仅检查 CLI 是否存在；登录错误可能在首次运行时返回 |
| `WithCwd(dir)` | ✅ | ✅ | |
| `WithModel(model)` | ✅ | ✅ | |
| `WithSystemPrompt(prompt)` | ✅ | ✅ | |
| `WithExtraArgs(args...)` | ✅ | ❌ | Codex app-server 启动参数当前固定 |
| `WithEnv(env...)` | ✅ | ✅ | |

## Agent

| API | Claude Code | Codex | 备注 |
| --- | :---: | :---: | --- |
| `(*Agent).NewSession(ctx)` | ✅ | ✅ | |
| `(*Agent).ListSessions(ctx)` | ✅ | ✅ | 读取本地 JSONL 历史 |
| `(*Agent).GetSession(ctx, id)` | ✅ | ✅ | 下一次运行时自动恢复 |
| `(*Agent).Close()` | ✅ | ✅ | |

## Session

| API | Claude Code | Codex | 备注 |
| --- | :---: | :---: | --- |
| `(*Session).ID()` | ✅ | ✅ | 新会话首次运行前为空 |
| `(*Session).State()` | ✅ | ✅ | Claude 不会进入 `Blocked` |
| `(*Session).Run(ctx, prompt)` | ✅ | ✅ | |
| `(*Session).Stream(ctx, prompt)` | ✅ | ✅ | |
| `(*Session).Interrupt(ctx)` | ✅ | ✅ | Claude 终止进程后在下一轮自动恢复 |
| `(*Session).Continue(ctx, input)` | ❌ | ⚠️ | Codex 仅支持命令和文件审批；输入为 `allow_once`、`deny` 或 `cancel` |

## Stream

| API | Claude Code | Codex |
| --- | :---: | :---: |
| `(*Stream).Next()` | ✅ | ✅ |
| `(*Stream).Event()` | ✅ | ✅ |
| `(*Stream).Result()` | ✅ | ✅ |

## 事件

| `Event.Kind` | Claude Code | Codex | 备注 |
| --- | :---: | :---: | --- |
| `KindThinking` | ✅ | ✅ | 是否产生取决于模型和配置 |
| `KindText` | ✅ | ✅ | |
| `KindToolCall` | ✅ | ✅ | Codex 命令映射为 `shell`，MCP 工具映射为 `server/tool` |
| `KindToolResult` | ❌ | ⚠️ | Codex 仅映射命令输出，不包含 MCP 结果 |

`Event.Text` 承载文本、思考和工具结果；`Event.Tool`、`Event.ToolInput` 承载工具调用。

## Result

| 字段 | Claude Code | Codex | 备注 |
| --- | :---: | :---: | --- |
| `Text` | ✅ | ✅ | |
| `Thinking` | ✅ | ✅ | |
| `ToolCalls` | ✅ | ✅ | |
| `SessionID` | ✅ | ✅ | |
| `Usage` | ✅ | ⚠️ | 运行时未上报时为 `nil` |
| `DurationMs` | ✅ | ❌ | Codex 当前为 `0` |
| `Interrupted` | ✅ | ✅ | |
| `Blocked` | ❌ | ⚠️ | Codex 仅支持工具和文件审批 |

### Usage

`Result.Usage` 的 key 为模型名；未指定模型时为 `claude` 或 `codex`。

| `driver.TokenUsage` 字段 | Claude Code | Codex |
| --- | :---: | :---: |
| `InputTokens` | ✅ | ✅ |
| `OutputTokens` | ✅ | ✅ |
| `CacheReadTokens` | ✅ | ✅ |
| `CacheWriteTokens` | ✅ | ❌ |
| `CostUSD` | ✅ | ❌ |

## SessionInfo

| 字段 | Claude Code | Codex | 备注 |
| --- | :---: | :---: | --- |
| `ID` | ✅ | ✅ | |
| `Title` | ✅ | ✅ | Claude 取 `lastPrompt`；Codex 取第一条用户消息 |
| `Cwd` | ✅ | ✅ | |
| `StartedAt` | ⚠️ | ✅ | Claude 使用历史文件修改时间 |
| `UpdatedAt` | ✅ | ✅ | 使用历史文件修改时间 |
| `MessageCount` | ✅ | ✅ | |

## 阻塞类型

| `BlockedKind` | Claude Code | Codex |
| --- | :---: | :---: |
| `BlockedUserInput` | ❌ | ❌ |
| `BlockedToolApproval` | ❌ | ✅ |
| `BlockedPermission` | ❌ | ✅ |
| `BlockedAuthentication` | ❌ | ❌ |
| `BlockedExternal` | ❌ | ❌ |

## 公共契约

- Agent：`AgentKind`（`Claude`、`Codex`）
- 状态：`SessionState`（`Idle`、`Running`、`Blocked`）
- 数据类型：`Event`、`ToolCall`、`Result`、`SessionInfo`、`BlockedReason`、`BlockOption`
- 哨兵错误：`ErrInvalidState`、`ErrSessionNotFound`
- 错误类别：`transport`、`protocol`、`timeout`、`runtime_reported`、`unsupported`、`not_installed`、`invalid_state`、`session_not_found`

# 数据来源

agent-events 只读取各 Agent 写在本机的日志和数据库，不修改它们。

## 采集

服务每 3 秒检查一次数据源的大小和修改时间，只重新解析有变化的；请求确实增加或变化时才通知页面刷新。采集结果保存在数据目录的 `events.db` 中，日志被 Agent 清理后（如 Claude Code 默认保留 30 天）历史仍然保留。

升级到解析规则有变化的版本后，用 `agent-events -rebuild` 重新读取全部日志。

## 各 Agent

路径中的 `~` 为用户主目录。环境变量可写多个路径，用逗号分隔，设置后替换默认位置。

| Agent | 默认位置 | 环境变量 | 一条记录 | 延迟 |
| --- | --- | --- | --- | --- |
| Claude Code | `~/.claude/projects`、`~/.config/claude/projects` 下的 `*.jsonl` | `CLAUDE_CONFIG_DIR` | 一次请求 | 从 `api-request` 记录到最后一个内容块 |
| Codex | `~/.codex/sessions`、`~/.codex/archived_sessions` 下的 `rollout-*.jsonl` 及其 `.zst` 压缩版本 | `CODEX_HOME` | 一次请求 | 从用户消息或上一次工具调用完成到本次响应；每轮第一个请求有首字时间 |
| OpenCode | `~/.local/share/opencode` 下的 `opencode.db`、`opencode-*.db` 和 `storage/message/**/*.json` | `OPENCODE_DATA_DIR`、`XDG_DATA_HOME` | 一次请求 | 从请求开始到响应结束，工具执行时间不计入 |
| Kilo Code | `~/.local/share/kilo/kilo.db` | `KILO_DATA_DIR` | 一次请求 | 同 OpenCode |
| Antigravity | `~/.gemini/antigravity`、`antigravity-cli`、`antigravity-ide`、`antigravity-backup`、`antigravity-acp` 和 `~/.config/antigravity` 下的 `conversations/*.db` | `ANTIGRAVITY_DATA_DIR` | 一次请求 | 记录中的生成耗时 |
| Gemini CLI | `~/.gemini/tmp/*/chats/` 下的 `*.json`、`*.jsonl` | `GEMINI_DATA_DIR` | 一次请求 | — |
| Qwen Code | `~/.qwen/projects/*/chats/*.jsonl` | `QWEN_DATA_DIR` | 一次请求 | — |
| GitHub Copilot CLI | `~/.copilot/otel/**/*.jsonl`、`~/.copilot/session-state/*/events.jsonl` | `COPILOT_HOME`、`COPILOT_OTEL_FILE_EXPORTER_PATH` | OpenTelemetry 导出为一次请求，会话快照为会话汇总 | OpenTelemetry span 的起止时间 |
| Amp | `~/.local/share/amp/threads/T-*.json` | `AMP_DATA_DIR` | 一次请求 | — |
| Codebuff | `~/.config/manicode`、`manicode-dev`、`manicode-staging` 下的 `projects/**/chat-messages.json` | `CODEBUFF_DATA_DIR` | 一次请求 | — |
| Factory Droid | `~/.factory/sessions/**/*.settings.json` | `DROID_SESSIONS_DIR` | 会话汇总 | — |
| Goose | `~/.local/share/goose`、`~/Library/Application Support/goose`、`~/.local/share/Block/goose` 下的 `sessions/sessions.db` | `GOOSE_PATH_ROOT` | 会话汇总 | — |
| Grok Build | `~/.grok/sessions/**/updates.jsonl` | `GROK_HOME` | 轮次汇总，按模型分开 | — |
| Hermes | `~/.hermes/state.db` | `HERMES_HOME` | 会话汇总 | — |
| Kimi | `~/.kimi`、`~/.kimi-code` 下的 `sessions/**/wire.jsonl` | `KIMI_DATA_DIR` | Kimi CLI 为一次请求，Kimi Code 为轮次汇总 | — |
| OpenClaw | `~/.openclaw` 及旧名称目录 `~/.clawdbot`、`~/.moltbot`、`~/.moldbot` 下的 `*.jsonl` 和 `agents/*/agent/openclaw-agent.sqlite` | `OPENCLAW_DIR` | 一次请求 | — |
| Pi | `~/.pi/agent/sessions/**/*.jsonl`，`subagent-artifacts` 下的调试记录除外 | `PI_AGENT_DIR` | 一次请求 | — |
| ZCode | `~/.zcode/cli/db/db.sqlite`，只取已完成的请求 | `ZCODE_HOME` | 一次请求 | — |

部分 Agent 只记录一轮对话或整个会话的累计用量，这些记录在「会话」列标为「轮次汇总」或「会话汇总」，不参与速度统计。

## Token 口径

各 Agent 的用量统一拆成互不重叠的四部分：非缓存输入、缓存读取、缓存写入、输出（含推理）。

- 输入已包含缓存的记录（Codex、Copilot、Grok、ZCode 等）先减去缓存读写。
- Gemini CLI 和 Qwen Code 的输入通常包含缓存读取，按总数是否吻合判断后再拆分。
- 推理 Token 单独记录的（OpenCode、Droid、Hermes 等）计入输出。
- Claude Code 的缓存写入区分 5 分钟和 1 小时两种有效期，分别计价。

TPS = 输出 Token / 总延迟。

## 重复记录

同一个请求可能出现在多个数据源中，按请求标识只保留一条：

- Claude Code 续接会话会把之前的响应写进新的记录文件，`/btw` 旁支对话会以新的请求 ID 重放主对话；Anthropic 的消息 ID 唯一标识一个响应。
- Codex 的分叉会话和子代理会重放上级会话的用量，重放的记录带着新的时间；旧版本只有 `token_count` 的记录按累计用量识别。
- OpenCode V2 分叉会话以新的消息 ID 复制上级会话的消息，按分叉点排除复制部分。
- Pi 和 OpenClaw 分叉会话复制上级会话的消息，按时间、模型和用量识别。
- Copilot CLI 会把部分请求导出两次；有 OpenTelemetry 记录的会话不再计入会话快照。

重复时优先保留带有延迟信息的记录，其次保留时间最早的，即原始记录。
